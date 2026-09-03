package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// maxResponseCaptureBytes is the maximum size of response body to capture for logging
const maxResponseCaptureBytes = 65000

const (
	maxUsageTailCaptureBytes     = maxResponseCaptureBytes
	maxCodexStreamLineBytes      = 1 * 1024 * 1024
	maxRetryableStreamProbeBytes = 64 * 1024
	maxCodexStreamCollectBytes   = 8 * 1024 * 1024
	errCodexStreamCollectorLimit = "codex forced stream collector exceeded size limit"
)

type tailUsageCapture struct {
	buf   []byte
	limit int
}

type headResponseCapture struct {
	buf       []byte
	limit     int
	truncated bool
}

type limitedResponseCaptureWriter struct {
	writer    io.Writer
	limit     int
	capture   strings.Builder
	truncated bool
}

type streamFlushWriter struct {
	writer      io.Writer
	flusher     http.Flusher
	writeErr    *error
	onFirstByte func()
}

type firstByteWriter struct {
	writer      io.Writer
	onFirstByte func()
}

type firstByteReadCloser struct {
	io.ReadCloser
	onFirstByte func()
}

func (r firstByteReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.onFirstByte != nil {
		r.onFirstByte()
	}
	return n, err
}

func (w firstByteWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 && w.onFirstByte != nil {
		w.onFirstByte()
	}
	return n, err
}

func (w streamFlushWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil && w.writeErr != nil {
		*w.writeErr = err
	}
	if n > 0 && w.onFirstByte != nil {
		w.onFirstByte()
	}
	if n > 0 && w.flusher != nil {
		w.flusher.Flush()
	}
	return n, err
}

func newLimitedResponseCaptureWriter(writer io.Writer, limit int) *limitedResponseCaptureWriter {
	return &limitedResponseCaptureWriter{
		writer: writer,
		limit:  limit,
	}
}

func (w *limitedResponseCaptureWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 && w.limit > 0 && w.capture.Len() < w.limit {
		toCapture := p[:n]
		if remaining := w.limit - w.capture.Len(); len(toCapture) > remaining {
			toCapture = toCapture[:remaining]
			w.truncated = true
		}
		_, _ = w.capture.Write(toCapture)
	} else if n > 0 && w.limit > 0 {
		w.truncated = true
	}
	return n, err
}

func (w *limitedResponseCaptureWriter) String() string {
	if w == nil {
		return ""
	}
	return strings.ToValidUTF8(w.capture.String(), "")
}

type sseLogicalFailureCapture struct {
	pending             []byte
	statusCode          int
	errorCode           string
	errorMessage        string
	meaningfulSeen      bool
	firstSemanticFailed bool
	terminalSeen        bool
	unverified          bool
	disabled            bool
}

type logicalFailureProbeResult struct {
	statusCode   int
	errorCode    string
	errorMessage string
	failed       bool
}

type replayReadCloser struct {
	reader io.Reader
	closer io.Closer
}

type replayErrorReader struct {
	err error
}

func (r *replayErrorReader) Read(_ []byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	err := r.err
	r.err = nil
	return 0, err
}

func (r *replayReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *replayReadCloser) Close() error               { return r.closer.Close() }

func installResponseBodyReplay(resp *http.Response, remaining io.Reader, prefix []byte, probeErr error) {
	originalBody := resp.Body
	replayReaders := []io.Reader{bytes.NewReader(prefix)}
	if probeErr != nil {
		replayReaders = append(replayReaders, &replayErrorReader{err: probeErr})
	}
	replayReaders = append(replayReaders, remaining)
	resp.Body = &replayReadCloser{reader: io.MultiReader(replayReaders...), closer: originalBody}
}

func isEventStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	mediaType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

func hasUnsupportedResponseContentEncoding(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	contentEncoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	return contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity")
}

// retryableStreamProbe buffers only the leading SSE event. A logical failure can
// be retried safely only before any meaningful event has reached the downstream.
// Do not detach Body.Read behind a probe-only timer: net/http response bodies have
// no portable, non-destructive per-read deadline. Abandoning that read would race
// the downstream reader or retain a goroutine until the request lifecycle expires.
// Prelude and heartbeat bytes intentionally remain in the replay body rather than
// being written here: a direct write would commit the downstream response and close
// the retry window, while also bypassing protocol conversion and duplicating replay.
func retryableStreamProbe(resp *http.Response) (int, string, bool) {
	result := retryableStreamProbeResult(resp)
	return result.statusCode, result.errorMessage, result.failed
}

func retryableStreamProbeResult(resp *http.Response) logicalFailureProbeResult {
	if resp == nil || resp.Body == nil || !isEventStreamResponse(resp) || hasUnsupportedResponseContentEncoding(resp) {
		return logicalFailureProbeResult{}
	}
	reader := bufio.NewReaderSize(resp.Body, 4*1024)
	prefix := make([]byte, 0, 4*1024)
	var capture sseLogicalFailureCapture
	var probeErr error
	buf := make([]byte, 4*1024)
	for len(prefix) < maxRetryableStreamProbeBytes {
		remaining := maxRetryableStreamProbeBytes - len(prefix)
		n, err := reader.Read(buf[:min(len(buf), remaining)])
		if n > 0 {
			prefix = append(prefix, buf[:n]...)
			_, _ = capture.Write(buf[:n])
		}
		if err != nil {
			capture.Finish()
			if errors.Is(err, io.EOF) && !capture.meaningfulSeen && !capture.terminalSeen {
				// An SSE response that ends before content or a terminal event is an
				// upstream failure for every supported protocol, independent of HTTP status.
				capture.firstSemanticFailed = true
				capture.recordFailure("upstream_eof", "upstream_eof")
			}
			if !errors.Is(err, io.EOF) {
				probeErr = err
			}
		}
		if capture.meaningfulSeen || capture.terminalSeen {
			// Prelude events are buffered without committing the response. Any content,
			// unknown event, or terminal event closes the retry window before forwarding;
			// the selected handler then forwards/translates the replay exactly once.
			break
		}
		if err != nil {
			break
		}
	}
	// Replay is intentional: the selected response handler must independently parse
	// the complete stream for usage, logging, and terminal-state accounting.
	installResponseBodyReplay(resp, reader, prefix, probeErr)
	return logicalFailureProbeResult{
		statusCode:   capture.statusCode,
		errorCode:    capture.errorCode,
		errorMessage: strings.TrimSpace(capture.errorMessage),
		failed:       capture.firstSemanticFailed,
	}
}

// retryableResponseProbe inspects application-level failures before any bytes
// reach the downstream. Non-stream Responses payloads use a bounded prefix and
// keep the buffered reader as the response body so the selected handler replays
// every byte without a second upstream read or an unbounded allocation.
func retryableResponseProbe(c *gin.Context, resp *http.Response, retryAvailable bool) (int, string, bool) {
	result := retryableResponseProbeResult(c, resp, retryAvailable)
	return result.statusCode, result.errorMessage, result.failed
}

func retryableResponseProbeResult(c *gin.Context, resp *http.Response, retryAvailable bool) logicalFailureProbeResult {
	if isEventStreamResponse(resp) {
		// AI review suggested skipping this probe when retries are exhausted. Keep
		// it so a leading logical SSE failure can still set the final HTTP status
		// before downstream headers are committed; integration tests require it.
		return retryableStreamProbeResult(resp)
	}
	if c == nil || c.Request == nil || resp == nil || resp.Body == nil ||
		!retryAvailable ||
		resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices ||
		!isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		return logicalFailureProbeResult{}
	}

	if hasUnsupportedResponseContentEncoding(resp) {
		// Requests parsed by the proxy omit Accept-Encoding, so net/http normally
		// exposes decoded bytes. Preserve an unsolicited encoding rather than
		// risking classification of compressed data or changing the response.
		return logicalFailureProbeResult{}
	}

	reader := bufio.NewReaderSize(resp.Body, 4*1024)
	prefix := make([]byte, 0, 4*1024)
	buf := make([]byte, 4*1024)
	var probeErr error
	for len(prefix) < maxResponseCaptureBytes {
		remaining := maxResponseCaptureBytes - len(prefix)
		n, err := reader.Read(buf[:min(len(buf), remaining)])
		if n > 0 {
			prefix = append(prefix, buf[:n]...)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				probeErr = err
			}
			break
		}
		status := gjson.GetBytes(prefix, "status")
		if status.Exists() && (!strings.EqualFold(strings.TrimSpace(status.String()), "failed") ||
			gjson.GetBytes(prefix, "error.code").Exists() ||
			gjson.GetBytes(prefix, "error.message").Exists()) {
			break
		}
	}
	installResponseBodyReplay(resp, reader, prefix, probeErr)
	statusCode, errorCode, errorMessage, failed := parseResponsesLogicalFailure(prefix, len(prefix) >= maxResponseCaptureBytes)
	return logicalFailureProbeResult{
		statusCode:   statusCode,
		errorCode:    errorCode,
		errorMessage: errorMessage,
		failed:       failed,
	}
}

func isRetryableSSEPrelude(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.queued", "response.in_progress", "message_start", "ping":
		// These events carry only request/message metadata or a heartbeat. Buffering
		// them keeps retries safe because no assistant content reached downstream.
		return true
	default:
		return false
	}
}

func isRetryableSSEDataPrelude(data []byte, eventType string) bool {
	if isRetryableSSEPrelude(eventType) {
		return true
	}
	// OpenAI Chat's first chunk can contain only the assistant role and omit
	// `type`. It is metadata rather than generated content, so a following
	// leading error remains safe to retry. Content/tool deltas close the window.
	if strings.TrimSpace(eventType) != "" {
		return false
	}
	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() || len(choices.Array()) == 0 {
		return false
	}
	delta := choices.Get("0.delta")
	if !delta.IsObject() || strings.TrimSpace(delta.Get("role").String()) == "" {
		return false
	}
	for _, field := range []string{"content", "tool_calls", "function_call", "refusal"} {
		value := delta.Get(field)
		if value.Exists() && !strings.EqualFold(strings.TrimSpace(value.Raw), "null") && value.String() != "" {
			return false
		}
	}
	return true
}

func (w *tailUsageCapture) Write(p []byte) (int, error) {
	if w.limit <= 0 || len(p) == 0 {
		return len(p), nil
	}
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return len(p), nil
	}
	if overflow := len(w.buf) + len(p) - w.limit; overflow > 0 {
		if overflow >= len(w.buf) {
			w.buf = w.buf[:0]
		} else {
			copy(w.buf, w.buf[overflow:])
			w.buf = w.buf[:len(w.buf)-overflow]
		}
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *headResponseCapture) Write(p []byte) (int, error) {
	if w.limit <= 0 || len(p) == 0 {
		return len(p), nil
	}
	remaining := w.limit - len(w.buf)
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		w.truncated = true
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (p *sseLogicalFailureCapture) Write(chunk []byte) (int, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
	if p.disabled {
		return len(chunk), nil
	}
	p.pending = append(p.pending, chunk...)
	for {
		idx := bytes.IndexByte(p.pending, '\n')
		if idx < 0 {
			if len(p.pending) > maxCodexStreamLineBytes {
				p.parseOversizedLinePrefix(p.pending)
				p.unverified = true
				p.disabled = true
				p.pending = p.pending[:0]
			}
			return len(chunk), nil
		}
		line := p.pending[:idx]
		p.pending = p.pending[idx+1:]
		p.parseLine(line)
	}
}

func streamJSONPayload(line []byte) []byte {
	line = bytes.TrimSpace(line)
	switch {
	case bytes.HasPrefix(line, []byte("data:")):
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		return data
	case len(line) > 0 && line[0] == '{':
		// Some gateways emit a JSON error line after SSE comments without a data prefix.
		return line
	default:
		return nil
	}
}

func isSSETerminalSentinel(line []byte) bool {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(line[len("data:"):]), []byte("[DONE]"))
}

func isTerminalResponseEventType(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return true
	default:
		return false
	}
}

// upstreamEOFFailureFields fills the sentinel code/message used when the
// upstream stream ends without a meaningful or terminal event.
func upstreamEOFFailureFields(errorCode, errorMessage string) (string, string) {
	if errorCode == "" {
		errorCode = "upstream_eof"
	}
	if errorMessage == "" {
		errorMessage = "upstream_eof"
	}
	return errorCode, errorMessage
}

func (p *sseLogicalFailureCapture) parseOversizedLinePrefix(line []byte) {
	if isSSETerminalSentinel(line) {
		p.terminalSeen = true
		return
	}
	data := streamJSONPayload(line)
	if len(data) == 0 {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	p.classifyFailure(data, eventType)
}

func (p *sseLogicalFailureCapture) classifyFailure(data []byte, eventType string) {
	isFirstSemantic := !p.meaningfulSeen
	if !isRetryableSSEDataPrelude(data, eventType) {
		p.meaningfulSeen = true
	}
	responseStatus := strings.TrimSpace(gjson.GetBytes(data, "response.status").String())
	if isTerminalResponseEventType(eventType) {
		p.terminalSeen = true
	}
	isUpstreamEOF := isResponseIncompleteUpstreamEOF(data, eventType)
	nestedError := gjson.GetBytes(data, "error")
	if !nestedError.IsObject() && eventType != "error" && eventType != "response.failed" && !strings.EqualFold(responseStatus, "failed") && !isUpstreamEOF {
		return
	}
	if isFirstSemantic {
		p.firstSemanticFailed = true
	}
	errorCode, errorMessage := responseFailureFields(data, eventType)
	if isUpstreamEOF {
		errorCode, errorMessage = upstreamEOFFailureFields(errorCode, errorMessage)
	}
	p.recordFailure(errorCode, errorMessage)
	p.terminalSeen = true
}

func (p *sseLogicalFailureCapture) Finish() {
	if len(p.pending) > 0 {
		p.parseLine(p.pending)
		p.pending = nil
	}
}

func (p *sseLogicalFailureCapture) apply(c *gin.Context) {
	p.Finish()
	if c != nil && c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		if p.unverified {
			c.Set(ctxKeyResponsesStatusUnverified, true)
		}
		if !p.terminalSeen {
			markResponseProcessingFailed(c)
		}
	}
	if p.statusCode > 0 {
		setLogicalFailureContext(c, p.statusCode, p.errorCode, p.errorMessage)
	}
}

func (p *sseLogicalFailureCapture) parseLine(line []byte) {
	if isSSETerminalSentinel(line) {
		p.terminalSeen = true
		return
	}
	data := streamJSONPayload(line)
	if len(data) == 0 {
		return
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := utils.UnmarshalJSONUseNumber(data, &payload); err != nil {
		// pending already reassembles transport reads up to a newline. Do not guess
		// across invalid newline-delimited JSON records: combining separate SSE data
		// can manufacture a false error after semantic output and cause a duplicate retry.
		p.meaningfulSeen = true
		p.unverified = true
		return
	}
	p.classifyFailure(data, payload.Type)
}

func responseFailureFields(data []byte, eventType string) (string, string) {
	errorCode := responseFailureField(data, "error.code")
	if errorCode == "" {
		errorCode = responseFailureField(data, "error.type")
	}
	if errorCode == "" {
		errorCode = responseFailureField(data, "error.status")
	}
	if errorCode == "" {
		errorCode = responseFailureField(data, "response.error.code")
	}
	if errorCode == "" && eventType == "error" {
		errorCode = responseFailureField(data, "code")
	}
	errorMessage := responseFailureField(data, "error.message")
	if errorMessage == "" {
		errorMessage = responseFailureField(data, "response.error.message")
	}
	if errorMessage == "" && eventType == "error" {
		errorMessage = responseFailureField(data, "message")
	}
	return errorCode, errorMessage
}

func responseFailureField(data []byte, path string) string {
	value := gjson.GetBytes(data, path)
	if value.Type == gjson.Number {
		// Preserve the numeric token exactly so high-precision and exponent-form
		// provider codes are classified without float rounding or representation drift.
		return strings.TrimSpace(value.Raw)
	}
	return strings.TrimSpace(value.String())
}

func isResponseIncompleteUpstreamEOF(data []byte, eventType string) bool {
	return eventType == "response.incomplete" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(data, "response.incomplete_details.reason").String()), "upstream_eof")
}

func (p *sseLogicalFailureCapture) recordFailure(errorCode, errorMessage string) {
	p.statusCode = logicalFailureStatusCode(errorCode, errorMessage)
	p.errorCode = errorCode
	if errorMessage != "" {
		p.errorMessage = errorMessage
	}
}

func logicalFailureStatusCode(errorCode, errorMessage string) int {
	lowerCode := strings.ToLower(strings.TrimSpace(errorCode))
	lowerMessage := strings.ToLower(errorMessage)
	switch lowerCode {
	case "429", "rate_limit_exceeded", "rate_limit_error", "resource_exhausted":
		return http.StatusTooManyRequests
	case "invalid_request_error":
		return http.StatusBadRequest
	case "model_not_found":
		return http.StatusNotFound
	case "529", "overloaded_error":
		// Anthropic documents overloaded_error as HTTP 529, including SSE errors
		// that arrive after the initial successful HTTP response.
		return 529
	case "504", "timeout_error":
		return http.StatusGatewayTimeout
	}
	if statusCode := equivalentNumericLogicalFailureStatusCode(lowerCode); statusCode != 0 {
		return statusCode
	}
	if strings.Contains(lowerMessage, "concurrency limit exceeded") || strings.Contains(lowerMessage, "rate limit") {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func isPermanentLogicalFailure(errorCode, errorMessage string) bool {
	lowerCode := strings.ToLower(strings.TrimSpace(errorCode))
	if lowerCode == "invalid_request_error" && isReasoningContentPassbackFailure(errorMessage) {
		return false
	}
	switch lowerCode {
	case "invalid_request_error", "model_not_found":
		return true
	default:
		return false
	}
}

// isReasoningContentPassbackFailure reports whether the error is a DeepSeek
// thinking-mode reasoning_content passback rejection. The upstream rejects
// the request before any output, so failover (retry) on a different key or
// upstream may succeed — this is recoverable, not permanent. Wording varies
// between deployments, so the match requires the three stable keywords
// reasoning_content, thinking and back rather than one fixed phrase.
func isReasoningContentPassbackFailure(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "reasoning_content") &&
		strings.Contains(lower, "thinking") &&
		strings.Contains(lower, "back")
}

func equivalentNumericLogicalFailureStatusCode(errorCode string) int {
	// JSON numbers may use decimal or exponent notation. Compare exactly so a
	// nearby high-precision value is never rounded into a retryable status code.
	const (
		maxNumericStatusCodeBytes    = 64
		maxNumericStatusCodeExponent = 64
	)
	if len(errorCode) > maxNumericStatusCodeBytes || !json.Valid([]byte(errorCode)) {
		return 0
	}
	if exponentIndex := strings.IndexAny(errorCode, "eE"); exponentIndex >= 0 {
		exponent, err := strconv.ParseInt(errorCode[exponentIndex+1:], 10, 16)
		if err != nil || exponent < -maxNumericStatusCodeExponent || exponent > maxNumericStatusCodeExponent {
			return 0
		}
	}
	numericCode, ok := new(big.Rat).SetString(errorCode)
	if !ok || !numericCode.IsInt() || !numericCode.Num().IsInt64() {
		return 0
	}
	switch numericCode.Num().Int64() {
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case 529:
		return 529
	case http.StatusGatewayTimeout:
		return http.StatusGatewayTimeout
	default:
		return 0
	}
}

func setLogicalFailureContext(c *gin.Context, statusCode int, errorCode, errorMessage string) {
	if c == nil || statusCode <= 0 {
		return
	}
	c.Set(ctxKeyUpstreamLogicalStatusCode, statusCode)
	if strings.TrimSpace(errorMessage) != "" {
		c.Set(ctxKeyUpstreamLogicalErrorMessage, strings.TrimSpace(utils.SanitizeErrorBody(errorMessage)))
	}
	if _, exists := c.Get("response_body"); !exists && (strings.TrimSpace(errorCode) != "" || strings.TrimSpace(errorMessage) != "") {
		summary := strings.TrimSpace(utils.SanitizeErrorBody(errorMessage))
		if strings.TrimSpace(errorCode) != "" {
			summary = `{"error":{"code":"` + strings.TrimSpace(errorCode) + `","message":"` + strings.ReplaceAll(summary, `"`, `'`) + `"}}`
		}
		c.Set("response_body", utils.TruncateString(summary, maxResponseCaptureBytes))
	}
	if statusCode == http.StatusTooManyRequests {
		if currentPressure, exists := c.Get(ctxKeyRateLimitPressure); !exists {
			c.Set(ctxKeyRateLimitPressure, int64(3))
		} else if pressure, ok := currentPressure.(int64); ok && pressure < 3 {
			c.Set(ctxKeyRateLimitPressure, int64(3))
		}
	}
}

func setLogicalFailureProbeContext(c *gin.Context, probe logicalFailureProbeResult) {
	errorCode := probe.errorCode
	if errorCode == "" {
		errorCode = "upstream_response_error"
	}
	setLogicalFailureContext(c, probe.statusCode, errorCode, probe.errorMessage)
}

func markResponseProcessingFailed(c *gin.Context) {
	if c != nil {
		c.Set(ctxKeyResponseProcessingFailed, true)
	}
}

// shouldCaptureResponse checks if response body capturing is enabled for the request
func shouldCaptureResponse(c *gin.Context) bool {
	if groupVal, exists := c.Get("group"); exists {
		if group, ok := groupVal.(*models.Group); ok {
			return group.EffectiveConfig.EnableRequestBodyLogging
		}
	}
	return false
}

func sanitizeAndTruncateStringForLog(value string, limit int) string {
	if value == "" || limit <= 0 {
		return ""
	}
	return utils.TruncateString(utils.SanitizeErrorBody(value), limit)
}

func sanitizeAndTruncateBytesForLog(value []byte, limit int) string {
	if len(value) == 0 || limit <= 0 {
		return ""
	}
	return sanitizeAndTruncateStringForLog(string(value), limit)
}

func decodeResponseBodyForLog(resp *http.Response, body []byte) ([]byte, bool) {
	if resp == nil || len(body) == 0 {
		return body, true
	}
	contentEncoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	if contentEncoding == "" {
		return body, true
	}
	decoded, err := utils.DecompressResponseWithLimit(contentEncoding, body, maxResponseCaptureBytes)
	if err != nil {
		logrus.WithError(err).Warn("Failed to decode response body for logging")
		return []byte("[compressed response omitted: decompressed body exceeds log capture limit]"), false
	}
	if bytes.Equal(decoded, body) {
		return []byte("[compressed response omitted: unsupported or invalid content encoding]"), false
	}
	return decoded, true
}

func isSupportedResponseContentEncoding(contentEncoding string) bool {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "identity", "gzip", "deflate", "br", "zstd":
		return true
	default:
		return false
	}
}

func captureDecodedResponseChunk(responseCapture *bytes.Buffer, chunk []byte) {
	if responseCapture == nil || responseCapture.Len() >= maxResponseCaptureBytes {
		return
	}
	toWrite := chunk
	if responseCapture.Len()+len(toWrite) > maxResponseCaptureBytes {
		toWrite = toWrite[:maxResponseCaptureBytes-responseCapture.Len()]
	}
	responseCapture.Write(toWrite)
}

func copyRemainingStreamToClient(c *gin.Context, r io.Reader, flusher http.Flusher) error {
	_, err := io.Copy(streamFlushWriter{writer: c.Writer, flusher: flusher, onFirstByte: func() { markFirstByte(c) }}, r)
	if err != nil {
		logUpstreamError("copying remaining compressed stream to client", err)
	}
	return err
}

func setTokenUsageOrEstimateFromCompressedReader(c *gin.Context, contentEncoding string, encodedBody io.ReadCloser, allowEstimate bool) error {
	decodedReader, err := utils.NewDecompressReader(contentEncoding, encodedBody)
	if err != nil {
		return err
	}
	defer decodedReader.Close()

	usageCapture := &tailUsageCapture{
		limit: maxUsageTailCaptureBytes,
	}
	estimateCapture := &estimatedTokenCapture{}
	copyWriter := io.MultiWriter(usageCapture, estimateCapture)
	var statusCapture *headResponseCapture
	if c != nil && c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		statusCapture = &headResponseCapture{limit: maxResponseCaptureBytes}
		copyWriter = io.MultiWriter(usageCapture, statusCapture, estimateCapture)
	}
	if _, err := io.Copy(copyWriter, decodedReader); err != nil {
		return err
	}
	if statusCapture != nil {
		setResponsesLogicalFailureFromCapturedBody(c, statusCapture.buf, statusCapture.truncated)
	}
	if len(usageCapture.buf) > 0 && setTokenUsageFromBody(c, usageCapture.buf) {
		return nil
	}
	if allowEstimate {
		setEstimatedOutputTokens(c, estimateCapture.Tokens())
	}
	return nil
}

func setResponsesLogicalFailureFromBody(c *gin.Context, body []byte) {
	setResponsesLogicalFailureFromCapturedBody(c, body, false)
}

func setResponsesLogicalFailureFromCapturedBody(c *gin.Context, body []byte, truncated bool) {
	if c == nil || c.Request == nil || len(body) == 0 || !isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		return
	}
	statusCode, errorCode, errorMessage, failed := parseResponsesLogicalFailure(body, truncated)
	if failed {
		setLogicalFailureContext(c, statusCode, errorCode, errorMessage)
		return
	}
	if truncated && !gjson.GetBytes(body, "status").Exists() {
		c.Set(ctxKeyResponsesStatusUnverified, true)
	}
}

func parseResponsesLogicalFailure(body []byte, truncated bool) (int, string, string, bool) {
	if len(body) == 0 {
		return 0, "", "", false
	}
	statusResult := gjson.GetBytes(body, "status")
	if truncated && !statusResult.Exists() {
		return 0, "", "", false
	}
	if !strings.EqualFold(strings.TrimSpace(statusResult.String()), "failed") {
		return 0, "", "", false
	}
	errorCode := strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
	errorMessage := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	return logicalFailureStatusCode(errorCode, errorMessage), errorCode, errorMessage, true
}

func setResponsesLogicalFailure(c *gin.Context, status, errorCode, errorMessage string) {
	if !strings.EqualFold(strings.TrimSpace(status), "failed") {
		return
	}
	setLogicalFailureContext(c, logicalFailureStatusCode(errorCode, errorMessage), errorCode, errorMessage)
}

func (ps *ProxyServer) handleStreamingResponse(c *gin.Context, resp *http.Response) {
	resp.Body = firstByteReadCloser{ReadCloser: resp.Body, onFirstByte: func() { markFirstByte(c) }}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logrus.Error("Streaming unsupported by the writer, falling back to normal response")
		ps.handleNormalResponse(c, resp)
		return
	}

	// Check if response body capturing is enabled
	shouldCapture := shouldCaptureResponse(c)

	var responseCapture *bytes.Buffer
	if shouldCapture {
		responseCapture = bytes.NewBuffer(make([]byte, 0, 4096))
	}
	contentEncoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	encodedResponse := contentEncoding != ""
	decodedEncodedResponse := encodedResponse && isSupportedResponseContentEncoding(contentEncoding)
	isResponsesStream := c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path)
	if encodedResponse && !decodedEncodedResponse && isResponsesStream {
		c.Set(ctxKeyResponsesStatusUnverified, true)
	}
	var encodedCapture bytes.Buffer
	var usageParser tokenusage.SSEParser
	var estimateCapture estimatedTokenCapture
	var failureCapture sseLogicalFailureCapture

	buf := make([]byte, 4*1024)
	if decodedEncodedResponse {
		var downstreamWriteErr error
		teeReader := io.TeeReader(resp.Body, streamFlushWriter{
			writer:      c.Writer,
			flusher:     flusher,
			writeErr:    &downstreamWriteErr,
			onFirstByte: func() { markFirstByte(c) },
		})
		decodedReader, err := utils.NewDecompressReader(contentEncoding, io.NopCloser(teeReader))
		if err != nil {
			if downstreamWriteErr != nil {
				logUpstreamError("writing stream to client", downstreamWriteErr)
				markResponseProcessingFailed(c)
				return
			}
			logUpstreamError("creating compressed stream decoder", err)
			markResponseProcessingFailed(c)
			_ = copyRemainingStreamToClient(c, resp.Body, flusher)
			return
		}
		defer decodedReader.Close()

		for {
			n, err := decodedReader.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				usageParser.Write(chunk)
				estimateCapture.Write(chunk)
				failureCapture.Write(chunk)
				captureDecodedResponseChunk(responseCapture, chunk)
			}
			if downstreamWriteErr != nil {
				logUpstreamError("writing stream to client", downstreamWriteErr)
				markResponseProcessingFailed(c)
				return
			}
			if isResponsesStream && failureCapture.terminalSeen {
				if !strings.EqualFold(contentEncoding, "identity") {
					// The decoder may expose the semantic terminal before consuming the encoded frame trailer.
					// Drain only the remaining raw bytes; TeeReader already forwarded anything buffered by the decoder.
					if err := copyRemainingStreamToClient(c, resp.Body, flusher); err != nil {
						markResponseProcessingFailed(c)
					}
				}
				break
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				logUpstreamError("decoding compressed stream", err)
				markResponseProcessingFailed(c)
				_ = copyRemainingStreamToClient(c, resp.Body, flusher)
				return
			}
		}
	} else {
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if encodedResponse {
					if encodedCapture.Len() < maxResponseCaptureBytes {
						toWrite := chunk
						if encodedCapture.Len()+len(toWrite) > maxResponseCaptureBytes {
							toWrite = toWrite[:maxResponseCaptureBytes-encodedCapture.Len()]
						}
						encodedCapture.Write(toWrite)
					}
				} else {
					usageParser.Write(chunk)
					estimateCapture.Write(chunk)
					failureCapture.Write(chunk)
				}
				markFirstByte(c)
				if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
					logUpstreamError("writing stream to client", writeErr)
					markResponseProcessingFailed(c)
					return
				}

				// Capture response data if enabled (up to max capture limit)
				if !encodedResponse {
					captureDecodedResponseChunk(responseCapture, chunk)
				}

				flusher.Flush()
				if isResponsesStream && failureCapture.terminalSeen {
					break
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				logUpstreamError("reading from upstream", err)
				markResponseProcessingFailed(c)
				return
			}
		}
	}

	if decodedEncodedResponse {
		if responseCapture != nil && responseCapture.Len() > 0 {
			c.Set("response_body", sanitizeAndTruncateStringForLog(responseCapture.String(), maxResponseCaptureBytes))
		}
	} else if encodedResponse {
		decoded, ok := decodeResponseBodyForLog(resp, encodedCapture.Bytes())
		if len(decoded) > 0 {
			if responseCapture != nil {
				c.Set("response_body", sanitizeAndTruncateBytesForLog(decoded, maxResponseCaptureBytes))
			}
			if ok {
				usageParser.Write(decoded)
				estimateCapture.Write(decoded)
				failureCapture.Write(decoded)
			}
		}
	} else if responseCapture != nil && responseCapture.Len() > 0 {
		c.Set("response_body", sanitizeAndTruncateStringForLog(responseCapture.String(), maxResponseCaptureBytes))
	}
	failureCapture.apply(c)
	if usage, ok := usageParser.Finish(); ok {
		setTokenUsage(c, usage)
	} else if resp.StatusCode < http.StatusBadRequest && failureCapture.statusCode == 0 {
		setEstimatedOutputTokens(c, estimateCapture.Tokens())
	}
}

func (ps *ProxyServer) handleNormalResponse(c *gin.Context, resp *http.Response) {
	resp.Body = firstByteReadCloser{ReadCloser: resp.Body, onFirstByte: func() { markFirstByte(c) }}
	// Check if response body capturing is enabled
	shouldCapture := shouldCaptureResponse(c)
	contentEncoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))

	if shouldCapture {
		// Read response body and capture it
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logUpstreamError("reading response body", err)
			markResponseProcessingFailed(c)
			return
		}

		logBody, logBodyDecoded := decodeResponseBodyForLog(resp, body)
		c.Set("response_body", sanitizeAndTruncateBytesForLog(logBody, maxResponseCaptureBytes))
		if logBodyDecoded {
			setResponsesLogicalFailureFromBody(c, logBody)
			setTokenUsageOrEstimateFromFullBodyIf(c, logBody, resp.StatusCode < http.StatusBadRequest)
		} else if isSupportedResponseContentEncoding(contentEncoding) {
			err := setTokenUsageOrEstimateFromCompressedReader(c, contentEncoding, io.NopCloser(bytes.NewReader(body)), resp.StatusCode < http.StatusBadRequest)
			if err != nil {
				logUpstreamError("decoding compressed response body for token accounting", err)
				markResponseProcessingFailed(c)
			}
		} else if c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
			c.Set(ctxKeyResponsesStatusUnverified, true)
		}

		// Write to client
		// Only mark first byte when the body is non-empty: an empty upstream body
		// has no first byte; keep the nullable log column NULL so the UI shows '-' as before
		if len(body) > 0 {
			markFirstByte(c)
		}
		if _, err := c.Writer.Write(body); err != nil {
			logUpstreamError("writing response body", err)
		}
	} else if contentEncoding != "" {
		if isSupportedResponseContentEncoding(contentEncoding) {
			teeReader := io.TeeReader(resp.Body, firstByteWriter{writer: c.Writer, onFirstByte: func() { markFirstByte(c) }})
			err := setTokenUsageOrEstimateFromCompressedReader(c, contentEncoding, io.NopCloser(teeReader), resp.StatusCode < http.StatusBadRequest)
			if err != nil {
				logUpstreamError("decoding compressed response body for token accounting", err)
				markResponseProcessingFailed(c)
				if _, copyErr := io.Copy(firstByteWriter{writer: c.Writer, onFirstByte: func() { markFirstByte(c) }}, resp.Body); copyErr != nil {
					logUpstreamError("copying remaining compressed response body", copyErr)
				}
			}
		} else {
			if c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
				c.Set(ctxKeyResponsesStatusUnverified, true)
			}
			if _, err := io.Copy(firstByteWriter{writer: c.Writer, onFirstByte: func() { markFirstByte(c) }}, resp.Body); err != nil {
				logUpstreamError("copying compressed response body", err)
			}
		}
	} else {
		usageCapture := &tailUsageCapture{
			limit: maxUsageTailCaptureBytes,
		}
		estimateCapture := &estimatedTokenCapture{}
		copyWriter := io.MultiWriter(firstByteWriter{writer: c.Writer, onFirstByte: func() { markFirstByte(c) }}, usageCapture, estimateCapture)
		var statusCapture *headResponseCapture
		if c.Request != nil && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
			statusCapture = &headResponseCapture{limit: maxResponseCaptureBytes}
			copyWriter = io.MultiWriter(firstByteWriter{writer: c.Writer, onFirstByte: func() { markFirstByte(c) }}, usageCapture, statusCapture, estimateCapture)
		}
		if _, err := io.Copy(copyWriter, resp.Body); err != nil {
			logUpstreamError("copying response body", err)
			markResponseProcessingFailed(c)
			return
		}
		if statusCapture != nil {
			setResponsesLogicalFailureFromCapturedBody(c, statusCapture.buf, statusCapture.truncated)
		}
		if (len(usageCapture.buf) == 0 || !setTokenUsageFromBody(c, usageCapture.buf)) && resp.StatusCode < http.StatusBadRequest {
			setEstimatedOutputTokens(c, estimateCapture.Tokens())
		}
	}
}

// handleCodexForcedStreamResponse handles OpenAI Responses streaming response and converts to non-streaming format.
// This is used when client requests non-streaming but the upstream requires streaming internally.
// Per CLIProxyAPI implementation: collect stream events until response.completed, then return non-streaming response.
func (ps *ProxyServer) handleCodexForcedStreamResponse(c *gin.Context, resp *http.Response) {
	logrus.WithFields(logrus.Fields{
		"content_type":     resp.Header.Get("Content-Type"),
		"content_encoding": resp.Header.Get("Content-Encoding"),
		"status_code":      resp.StatusCode,
	}).Debug("Codex forced stream: collecting stream response for non-stream client")

	// Collect stream events and build a Responses API response.
	codexResp, err := collectCodexStreamToResponse(resp)
	if err != nil {
		logrus.WithError(err).Error("Codex forced stream: failed to collect stream response")
		markResponseProcessingFailed(c)
		// Do not expose internal error details to client for security
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Failed to collect stream response",
				"type":    "server_error",
			},
		})
		return
	}
	if !codexResp.terminalEventSeen {
		markResponseProcessingFailed(c)
	}

	// Check for Codex error in response
	if codexResp.Error != nil {
		statusCode := resp.StatusCode
		if strings.EqualFold(codexResp.Status, "failed") {
			statusCode = logicalFailureStatusCode(codexResp.Error.Code, codexResp.Error.Message)
		} else if statusCode < http.StatusBadRequest {
			statusCode = http.StatusBadGateway
		}
		setLogicalFailureContext(c, statusCode, codexResp.Error.Code, codexResp.Error.Message)
		logrus.WithFields(logrus.Fields{
			"error_type":    codexResp.Error.Type,
			"error_message": utils.TruncateString(utils.SanitizeErrorBody(codexResp.Error.Message), 200),
		}).Warn("Codex forced stream: upstream returned error")
		c.JSON(statusCode, codexResp)
		return
	}

	logrus.WithFields(logrus.Fields{
		"response_id": codexResp.ID,
		"model":       codexResp.Model,
		"status":      codexResp.Status,
		"output_len":  len(codexResp.Output),
	}).Debug("Codex forced stream: converted stream to non-stream response")

	// Marshal and return response
	responseBody, err := json.Marshal(codexResp)
	if err != nil {
		logrus.WithError(err).Error("Codex forced stream: failed to marshal response")
		markResponseProcessingFailed(c)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to marshal response",
				"type":    "server_error",
			},
		})
		return
	}
	logicalStatusCode, _, hasLogicalFailure := logicalStatusFromContext(c)
	shouldEstimate := resp.StatusCode < http.StatusBadRequest && (!hasLogicalFailure || logicalStatusCode < http.StatusBadRequest)
	setTokenUsageOrEstimateFromFullBodyIf(c, responseBody, shouldEstimate)
	if codexResp.terminalEventSeen && isFunctionCallEnabled(c) && functionCallTriggerSignal(c) != "" {
		fcResp := &http.Response{
			StatusCode: resp.StatusCode,
			Body:       io.NopCloser(bytes.NewReader(responseBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}
		// Forced OpenAI Responses streams are collected into a normal Responses
		// payload first; reuse the same XML-to-function_call converter here.
		ps.handleFunctionCallResponsesNormalResponse(c, fcResp)
		return
	}

	// Store response for logging if enabled
	if shouldCaptureResponse(c) {
		c.Set("response_body", sanitizeAndTruncateBytesForLog(responseBody, maxResponseCaptureBytes))
	}

	// c.Data already sets Content-Type, no need for redundant c.Header call
	c.Data(resp.StatusCode, "application/json", responseBody)
}

// codexStreamResponse represents a Codex streaming response structure for collection.
type codexStreamResponse struct {
	ID                string                  `json:"id"`
	Object            string                  `json:"object"`
	CreatedAt         int64                   `json:"created_at,omitempty"`
	Status            string                  `json:"status"`
	Model             string                  `json:"model"`
	Output            []codexStreamOutputItem `json:"output"`
	Usage             *codexStreamUsage       `json:"usage,omitempty"`
	Error             *codexStreamError       `json:"error,omitempty"`
	terminalEventSeen bool                    `json:"-"`
}

type codexStreamOutputItem struct {
	Type             string                    `json:"type"`
	ID               string                    `json:"id,omitempty"`
	Status           string                    `json:"status,omitempty"`
	Role             string                    `json:"role,omitempty"`
	Content          []codexStreamContentBlock `json:"content,omitempty"`
	CallID           string                    `json:"call_id,omitempty"`
	Name             string                    `json:"name,omitempty"`
	Arguments        string                    `json:"arguments,omitempty"`
	EncryptedContent string                    `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage           `json:"summary,omitempty"`
}

type codexStreamContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexStreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type codexStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// codexStreamEvent represents a single event in Codex streaming response.
type codexStreamEvent struct {
	Type      string               `json:"type"`
	Response  *codexStreamResponse `json:"response,omitempty"`
	ItemID    string               `json:"item_id,omitempty"`
	OutputIdx int                  `json:"output_index,omitempty"`
	Delta     string               `json:"delta,omitempty"`
	Item      *codexStreamItem     `json:"item,omitempty"`
}

type codexStreamItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	Status           string          `json:"status,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
}

// collectCodexStreamToResponse reads streaming response and builds a complete CodexResponse.
// Waits for response.completed event to get the final response state.
// Note: Caller is responsible for closing resp.Body (typically via defer in server.go).
// Note: Usage field is populated from response.completed event; fallback path has no usage data.
func collectCodexStreamToResponse(resp *http.Response) (*codexStreamResponse, error) {
	if resp == nil || resp.Body == nil {
		return nil, io.ErrUnexpectedEOF
	}

	bodyReader := resp.Body
	if contentEncoding := resp.Header.Get("Content-Encoding"); contentEncoding != "" {
		decompressed, err := utils.NewDecompressReader(contentEncoding, resp.Body)
		if err != nil {
			return nil, err
		}
		bodyReader = decompressed
		defer func() {
			if closer, ok := bodyReader.(io.Closer); ok && closer != resp.Body {
				closer.Close()
			}
		}()
	}

	scanner := bufio.NewScanner(io.LimitReader(bodyReader, maxCodexStreamCollectBytes+1))
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexStreamLineBytes)

	var finalResp *codexStreamResponse
	var currentEventType string
	var parseErrorCount int // Track JSON parse errors for debugging
	var collectedBytes int64

	// Collectors for building response from stream events
	var outputItems []codexStreamOutputItem
	var currentTextContent strings.Builder
	var currentToolArgs strings.Builder // Use strings.Builder for efficient concatenation in loop
	var currentToolID, currentToolName string
	var model string
	var responseID string

readLoop:
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		collectedBytes += int64(len(lineBytes)) + 1 // Include the consumed newline for the total stream cap.
		if collectedBytes > maxCodexStreamCollectBytes {
			return nil, errors.New(errCodexStreamCollectorLimit)
		}

		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}

		// Parse SSE format
		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break readLoop
			}

			var event codexStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				parseErrorCount++
				logrus.WithError(err).Debug("Codex forced stream: failed to parse stream event")
				continue
			}

			if currentEventType != "" && event.Type == "" {
				event.Type = currentEventType
			}
			currentEventType = ""

			// Process events to build response
			switch event.Type {
			case "response.created":
				if event.Response != nil {
					model = event.Response.Model
					responseID = event.Response.ID
				}

			case "response.output_item.added":
				if event.Item != nil && event.Item.Type == "function_call" {
					currentToolID = event.Item.CallID
					currentToolName = event.Item.Name
					currentToolArgs.Reset()
				}

			case "response.output_text.delta":
				if event.Delta != "" {
					currentTextContent.WriteString(event.Delta)
				}

			case "response.function_call_arguments.delta":
				if event.Delta != "" {
					currentToolArgs.WriteString(event.Delta)
				}

			case "response.output_item.done":
				if event.Item != nil {
					switch event.Item.Type {
					case "message":
						// Message complete - add text content if any
						if currentTextContent.Len() > 0 {
							outputItems = append(outputItems, codexStreamOutputItem{
								Type:   "message",
								Role:   "assistant",
								Status: "completed",
								Content: []codexStreamContentBlock{
									{Type: "output_text", Text: currentTextContent.String()},
								},
							})
							currentTextContent.Reset()
						}
					case "function_call":
						// Function call complete
						toolID := event.Item.CallID
						if toolID == "" {
							toolID = currentToolID
						}
						toolName := event.Item.Name
						if toolName == "" {
							toolName = currentToolName
						}
						args := event.Item.Arguments
						if args == "" {
							args = currentToolArgs.String()
						}
						outputItems = append(outputItems, codexStreamOutputItem{
							Type:      "function_call",
							CallID:    toolID,
							Name:      toolName,
							Arguments: args,
						})
						currentToolID = ""
						currentToolName = ""
						currentToolArgs.Reset()
					case "reasoning":
						outputItems = append(outputItems, codexStreamOutputItem{
							Type:             "reasoning",
							ID:               event.Item.ID,
							Status:           event.Item.Status,
							EncryptedContent: event.Item.EncryptedContent,
							Summary:          event.Item.Summary,
						})
					}
				}

			case "response.completed", "response.done":
				// Final response - use the complete response if available (includes Usage)
				if event.Response != nil {
					finalResp = event.Response
					finalResp.terminalEventSeen = true
				}
			case "response.failed":
				if event.Response != nil {
					finalResp = event.Response
					finalResp.terminalEventSeen = true
				}
				break readLoop
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Log warning if multiple parse errors occurred (may indicate upstream issues)
	if parseErrorCount > 0 {
		logrus.WithField("error_count", parseErrorCount).Warn("Codex forced stream: multiple JSON parse errors during stream collection")
	}

	// If we didn't get a complete response from response.completed, build one from collected data
	if finalResp == nil {
		logrus.Warn("Codex forced stream: stream ended without response.completed event, building response from collected data")

		// Add any remaining text content
		if currentTextContent.Len() > 0 {
			outputItems = append(outputItems, codexStreamOutputItem{
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []codexStreamContentBlock{
					{Type: "output_text", Text: currentTextContent.String()},
				},
			})
		}

		// Log warning if partial function call data exists but not included
		// Note: We intentionally do NOT include incomplete function calls as they may cause
		// client-side parsing errors. The client should handle missing tool calls gracefully.
		if currentToolID != "" || currentToolName != "" || currentToolArgs.Len() > 0 {
			logrus.WithFields(logrus.Fields{
				"tool_id":   currentToolID,
				"tool_name": currentToolName,
				"args_len":  currentToolArgs.Len(),
			}).Warn("Codex forced stream: partial function call data lost due to stream interruption")
		}

		finalResp = &codexStreamResponse{
			ID:     responseID,
			Object: "response",
			Status: "completed",
			Model:  model,
			Output: outputItems,
			// Note: Usage is nil in fallback path as it's only available from response.completed event
		}
	}

	if finalResp != nil && strings.EqualFold(finalResp.Status, "failed") && finalResp.Error == nil {
		finalResp.Error = &codexStreamError{
			Type:    "server_error",
			Message: "upstream stream failed",
		}
	}

	return finalResp, nil
}
