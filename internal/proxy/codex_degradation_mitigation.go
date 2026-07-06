package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	codexDegradationMitigationConfigKey = "codex_degradation_mitigation_enabled"
	ctxKeyCodexDegradationMitigation    = "codex_degradation_mitigation_enabled"
	codexMitigationDefaultStep          = int64(518)
	codexMitigationMaxContinuations     = 8
	codexMitigationDefaultMarker        = "We need continue thinking. Do not summarize; continue from the previous reasoning state."
	codexMitigationMaxLineBytes         = 1 * 1024 * 1024
	codexMitigationMaxRoundBytes        = 32 * 1024 * 1024
	codexMitigationMaxReplayBytes       = 8 * 1024 * 1024
	codexMitigationMaxBufferedBytes     = 8 * 1024 * 1024
)

var errCodexMitigationLimit = errors.New("codex degradation mitigation exceeded stream collection limit")

type codexDegradationMitigationRoundTrip func([]byte) (*http.Response, error)

type codexMitigationBufferedItem struct {
	upstreamOutputIndex string
	events              []map[string]any
	item                map[string]any
}

type codexMitigationRoundResult struct {
	bufferedItems   []codexMitigationBufferedItem
	roundReasoning  []map[string]any
	terminal        map[string]any
	streamErr       error
	downstreamErr   error
	reasoningTokens *int64
	hasEncrypted    bool
	truncated       bool
	bufferedBytes   int
	replayBytes     int
}

type codexMitigationUsageAccumulator struct {
	sawUsage       bool
	firstInput     int64
	firstCached    int64
	proxyInput     int64
	proxyOutput    int64
	proxyTotal     int64
	proxyCached    int64
	totalReasoning int64
	finalOutput    int64
	finalReasoning int64
}

func codexDegradationMitigationEnabled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(ctxKeyCodexDegradationMitigation)
	if !exists {
		return false
	}
	enabled, _ := value.(bool)
	return enabled
}

func codexDegradationMitigationConfigEnabled(group, originalGroup *models.Group) bool {
	if group != nil && group.ChannelType == "openai-response" && getGroupConfigBool(group, codexDegradationMitigationConfigKey) {
		return true
	}
	return originalGroup != nil &&
		originalGroup != group &&
		originalGroup.ChannelType == "openai-response" &&
		getGroupConfigBool(originalGroup, codexDegradationMitigationConfigKey)
}

func codexDegradationMitigationShouldEnable(c *gin.Context, group, originalGroup *models.Group, bodyBytes []byte, isStream bool) bool {
	if c == nil || c.Request == nil || group == nil ||
		group.ChannelType != "openai-response" ||
		!isStream ||
		!isOpenAIResponsesEndpoint(c.Request.URL.Path) ||
		isCCEnabled(c) ||
		isCodexEnabled(c) ||
		isFunctionCallEnabled(c) ||
		!codexDegradationMitigationConfigEnabled(group, originalGroup) {
		return false
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return false
	}
	if stream, ok := body["stream"].(bool); !ok || !stream {
		return false
	}
	if reasoning, ok := body["reasoning"].(bool); ok && !reasoning {
		return false
	}
	return true
}

func prepareCodexDegradationMitigationInitialPayload(bodyBytes []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return bodyBytes, err
	}
	mergeResponsesEncryptedInclude(body)
	return json.Marshal(body)
}

func buildCodexDegradationMitigationContinuationPayload(baseBody []byte, replayTail []map[string]any, marker string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(baseBody, &body); err != nil {
		return nil, err
	}

	input := codexMitigationInputAsSlice(body["input"])
	for _, item := range replayTail {
		input = append(input, item)
	}
	input = append(input, codexMitigationCommentaryMarker(marker))

	body["stream"] = true
	body["input"] = input
	mergeResponsesEncryptedInclude(body)
	delete(body, "previous_response_id")

	return json.Marshal(body)
}

func mergeResponsesEncryptedInclude(body map[string]any) {
	include := make([]any, 0, 2)
	if existing, ok := body["include"].([]any); ok {
		for _, item := range existing {
			if !codexMitigationSliceContains(include, item) {
				include = append(include, item)
			}
		}
	}
	if !codexMitigationSliceContainsString(include, responsesEncryptedReasoning) {
		include = append(include, responsesEncryptedReasoning)
	}
	body["include"] = include
}

func codexMitigationInputAsSlice(value any) []any {
	switch v := value.(type) {
	case []any:
		out := make([]any, 0, len(v))
		out = append(out, v...)
		return out
	case nil:
		return []any{}
	default:
		return []any{v}
	}
}

func codexMitigationCommentaryMarker(marker string) map[string]any {
	if strings.TrimSpace(marker) == "" {
		marker = codexMitigationDefaultMarker
	}
	return map[string]any{
		"type":  "message",
		"role":  "assistant",
		"phase": "commentary",
		"content": []any{
			map[string]any{
				"type": "output_text",
				"text": marker,
			},
		},
	}
}

func codexMitigationSliceContains(items []any, value any) bool {
	for _, item := range items {
		if reflect.DeepEqual(item, value) {
			return true
		}
	}
	return false
}

func codexMitigationSliceContainsString(items []any, value string) bool {
	for _, item := range items {
		if text, ok := item.(string); ok && text == value {
			return true
		}
	}
	return false
}

func codexMitigationIsTruncationPattern(reasoningTokens *int64, step int64) bool {
	if reasoningTokens == nil {
		return false
	}
	if step < 3 {
		step = 3
	}
	tokens := *reasoningTokens
	return tokens >= step-2 && (tokens+2)%step == 0
}

func codexMitigationTierN(reasoningTokens *int64, step int64) any {
	if !codexMitigationIsTruncationPattern(reasoningTokens, step) {
		return nil
	}
	if step < 3 {
		step = 3
	}
	return (*reasoningTokens + 2) / step
}

func (ps *ProxyServer) handleCodexDegradationMitigationStreamingResponse(
	c *gin.Context,
	firstResp *http.Response,
	baseBody []byte,
	group *models.Group,
	originalGroup *models.Group,
	roundTrip codexDegradationMitigationRoundTrip,
) {
	if firstResp == nil || firstResp.Body == nil || !codexMitigationIsSSE(firstResp) {
		ps.handleStreamingResponse(c, firstResp)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	clearUpstreamEncodingHeaders(c)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logrus.Error("Codex degradation mitigation: streaming unsupported, falling back")
		ps.handleStreamingResponse(c, firstResp)
		return
	}

	state := &codexMitigationState{
		c:             c,
		flusher:       flusher,
		baseBody:      baseBody,
		marker:        codexMitigationDefaultMarker,
		roundTrip:     roundTrip,
		usage:         &codexMitigationUsageAccumulator{},
		group:         group,
		originalGroup: originalGroup,
	}
	state.run(firstResp)
}

type codexMitigationState struct {
	c                     *gin.Context
	flusher               http.Flusher
	baseBody              []byte
	marker                string
	roundTrip             codexDegradationMitigationRoundTrip
	group                 *models.Group
	originalGroup         *models.Group
	usage                 *codexMitigationUsageAccumulator
	roundNo               int
	continuations         int
	sequence              int64
	downstreamOutputIndex int
	baseResponse          map[string]any
	finalOutput           []any
	replayTail            []map[string]any
	rounds                []any
	writeErr              error
}

func (s *codexMitigationState) run(firstResp *http.Response) {
	current := firstResp
	for {
		s.roundNo++
		result := s.processRound(current)
		if current != nil && current != firstResp && current.Body != nil {
			_ = current.Body.Close()
		}

		if result.downstreamErr != nil {
			logrus.WithError(sanitizeInternalError(result.downstreamErr)).Debug("Codex degradation mitigation: downstream write failed")
			return
		}
		if result.streamErr != nil {
			logrus.WithError(result.streamErr).Warn("Codex degradation mitigation: stream round failed")
			s.writeSyntheticIncomplete("upstream_error")
			return
		}

		s.usage.addRound(codexMitigationUsageFromTerminal(result.terminal))
		result.truncated = codexMitigationIsTruncationPattern(result.reasoningTokens, codexMitigationDefaultStep)
		s.rounds = append(s.rounds, map[string]any{
			"round":                 s.roundNo,
			"reasoning_tokens":      codexMitigationNullableInt(result.reasoningTokens),
			"n":                     codexMitigationTierN(result.reasoningTokens, codexMitigationDefaultStep),
			"truncated":             result.truncated,
			"has_encrypted_content": result.hasEncrypted,
			"continued":             result.terminal != nil && result.truncated && result.hasEncrypted && s.continuations < codexMitigationMaxContinuations,
		})

		if result.terminal != nil && result.truncated && result.hasEncrypted && s.continuations < codexMitigationMaxContinuations {
			nextResp, ok := s.continueRound(result.roundReasoning, result.replayBytes)
			if !ok {
				return
			}
			current = nextResp
			continue
		}

		if result.terminal == nil {
			s.writeSyntheticIncomplete("upstream_eof")
			return
		}
		if result.truncated && !result.hasEncrypted {
			s.flushFinalRound(result, "no_encrypted_content")
			return
		}
		if result.truncated && s.continuations >= codexMitigationMaxContinuations {
			s.flushFinalRound(result, "max_continue")
			return
		}
		s.flushFinalRound(result, "")
		return
	}
}

func (s *codexMitigationState) continueRound(roundReasoning []map[string]any, replayBytes int) (*http.Response, bool) {
	if s.roundTrip == nil {
		s.writeSyntheticIncomplete("upstream_error")
		return nil, false
	}
	if s.writeErr != nil || s.c.Request.Context().Err() != nil {
		return nil, false
	}

	existingReplayBytes := codexMitigationJSONSize(s.replayTail)
	if existingReplayBytes+replayBytes > codexMitigationMaxReplayBytes {
		s.writeSyntheticIncomplete("collector_limit")
		return nil, false
	}

	s.continuations++
	s.replayTail = append(s.replayTail, roundReasoning...)
	nextBody, err := buildCodexDegradationMitigationContinuationPayload(s.baseBody, s.replayTail, s.marker)
	if err != nil {
		logrus.WithError(err).Warn("Codex degradation mitigation: failed to build continuation payload")
		s.writeSyntheticIncomplete("payload_error")
		return nil, false
	}
	s.replayTail = append(s.replayTail, codexMitigationCommentaryMarker(s.marker))

	nextResp, err := s.roundTrip(nextBody)
	if err != nil {
		logrus.WithError(sanitizeInternalError(err)).Warn("Codex degradation mitigation: continuation request failed")
		s.writeSyntheticIncomplete("upstream_error")
		return nil, false
	}
	if nextResp == nil || nextResp.Body == nil {
		s.writeSyntheticIncomplete("upstream_error")
		return nil, false
	}
	if nextResp.StatusCode < http.StatusOK || nextResp.StatusCode >= http.StatusMultipleChoices || !codexMitigationIsSSE(nextResp) {
		if nextResp.Body != nil {
			_ = nextResp.Body.Close()
		}
		s.writeSyntheticIncomplete("upstream_status")
		return nil, false
	}
	return nextResp, true
}

func (s *codexMitigationState) processRound(resp *http.Response) codexMitigationRoundResult {
	var result codexMitigationRoundResult
	if resp == nil || resp.Body == nil {
		result.streamErr = io.ErrUnexpectedEOF
		return result
	}

	reader, closer, err := codexMitigationResponseReader(resp)
	if err != nil {
		result.streamErr = err
		return result
	}
	if closer != nil {
		defer closer.Close()
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), codexMitigationMaxLineBytes)

	var block []string
	var currentBytes int
	itemKind := map[string]string{}
	itemIndex := map[string]int{}
	totalRoundBytes := 0

	processFrame := func(frame map[string]any) {
		if frame == nil {
			return
		}
		s.processEvent(frame, &result, itemKind, itemIndex)
		if s.writeErr != nil {
			result.downstreamErr = s.writeErr
		}
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		totalRoundBytes += len(line) + 1
		if totalRoundBytes > codexMitigationMaxRoundBytes {
			result.streamErr = errCodexMitigationLimit
			return result
		}
		if line == "" {
			processFrame(codexMitigationParseSSEBlock(block))
			if result.downstreamErr != nil {
				return result
			}
			if result.streamErr != nil {
				return result
			}
			block = nil
			currentBytes = 0
			continue
		}
		currentBytes += len(line) + 1
		if currentBytes > codexMitigationMaxLineBytes {
			result.streamErr = errCodexMitigationLimit
			return result
		}
		block = append(block, line)
	}
	if err := scanner.Err(); err != nil {
		result.streamErr = err
		return result
	}
	processFrame(codexMitigationParseSSEBlock(block))
	if result.downstreamErr != nil {
		return result
	}
	if result.streamErr != nil {
		return result
	}
	return result
}

func (s *codexMitigationState) processEvent(
	event map[string]any,
	result *codexMitigationRoundResult,
	itemKind map[string]string,
	itemIndex map[string]int,
) {
	eventType := codexMitigationEventType(event)
	if eventType == "response.created" || eventType == "response.in_progress" {
		if s.roundNo == 1 {
			if eventType == "response.created" {
				s.baseResponse = codexMitigationMapField(event, "response")
			}
			_ = s.writeEvent(event)
		}
		return
	}
	if codexMitigationTerminalEvent(eventType) {
		result.terminal = event
		result.reasoningTokens = codexMitigationReasoningTokensFromTerminal(event)
		return
	}

	if eventType == "response.output_item.added" {
		upstreamIndex := codexMitigationOutputIndexKey(event)
		if upstreamIndex == "" {
			_ = s.writeEvent(event)
			return
		}
		itemType := codexMitigationOutputItemType(event)
		if itemType == "reasoning" {
			itemKind[upstreamIndex] = "reasoning"
			itemIndex[upstreamIndex] = s.downstreamOutputIndex
			codexMitigationSetOutputIndex(event, s.downstreamOutputIndex)
			s.downstreamOutputIndex++
			_ = s.writeEvent(event)
			return
		}
		// Non-reasoning output is tentative until terminal usage proves the round
		// was not truncated; streaming it early would make truncated rounds
		// impossible to discard before the hidden continuation.
		itemKind[upstreamIndex] = "buffered"
		item := codexMitigationMapField(event, "item")
		result.bufferedItems = append(result.bufferedItems, codexMitigationBufferedItem{
			upstreamOutputIndex: upstreamIndex,
			events:              []map[string]any{event},
			item:                item,
		})
		result.bufferedBytes += codexMitigationJSONSize(event)
		return
	}

	upstreamIndex := codexMitigationOutputIndexKey(event)
	if upstreamIndex == "" {
		_ = s.writeEvent(event)
		return
	}
	switch itemKind[upstreamIndex] {
	case "reasoning":
		if idx, ok := itemIndex[upstreamIndex]; ok {
			codexMitigationSetOutputIndex(event, idx)
		}
		if eventType == "response.output_item.done" {
			if item := codexMitigationMapField(event, "item"); item != nil {
				result.roundReasoning = append(result.roundReasoning, item)
				result.replayBytes += codexMitigationJSONSize(item)
				if result.replayBytes > codexMitigationMaxReplayBytes {
					result.streamErr = errCodexMitigationLimit
					return
				}
				result.hasEncrypted = result.hasEncrypted || codexMitigationHasEncryptedContent(item)
				s.finalOutput = append(s.finalOutput, item)
			}
		}
		_ = s.writeEvent(event)
	case "buffered":
		for i := range result.bufferedItems {
			if result.bufferedItems[i].upstreamOutputIndex == upstreamIndex {
				if eventType == "response.output_item.done" {
					if item := codexMitigationMapField(event, "item"); item != nil {
						result.bufferedItems[i].item = item
					}
				}
				result.bufferedItems[i].events = append(result.bufferedItems[i].events, event)
				result.bufferedBytes += codexMitigationJSONSize(event)
				if result.bufferedBytes > codexMitigationMaxBufferedBytes {
					result.streamErr = errCodexMitigationLimit
				}
				return
			}
		}
	default:
		_ = s.writeEvent(event)
	}
}

func (s *codexMitigationState) flushFinalRound(result codexMitigationRoundResult, stoppedReason string) {
	if s.writeErr != nil {
		return
	}
	for _, buffered := range result.bufferedItems {
		finalItem := buffered.item
		for _, event := range buffered.events {
			codexMitigationSetOutputIndex(event, s.downstreamOutputIndex)
			if codexMitigationEventType(event) == "response.output_item.done" {
				if item := codexMitigationMapField(event, "item"); item != nil {
					finalItem = item
				}
			}
			if err := s.writeEvent(event); err != nil {
				return
			}
		}
		s.downstreamOutputIndex++
		if finalItem != nil {
			s.finalOutput = append(s.finalOutput, finalItem)
		}
	}
	s.writeTerminal(result.terminal, stoppedReason)
}

func (s *codexMitigationState) writeTerminal(terminal map[string]any, stoppedReason string) {
	if s.writeErr != nil {
		return
	}
	terminalType := codexMitigationEventType(terminal)
	if terminalType == "" {
		terminalType = "response.incomplete"
	}
	responseData := codexMitigationMapField(terminal, "response")
	response := codexMitigationCloneMap(s.baseResponse)
	if response == nil {
		response = codexMitigationCloneMap(responseData)
	}
	if response == nil {
		response = map[string]any{}
	}
	if status, ok := responseData["status"]; ok {
		response["status"] = status
	} else if terminalType == "response.incomplete" {
		response["status"] = "incomplete"
	}
	if details, ok := responseData["incomplete_details"]; ok {
		response["incomplete_details"] = details
	}
	response["output"] = s.finalOutput
	s.attachMetadataAndUsage(response, stoppedReason)

	event := map[string]any{
		"type":     terminalType,
		"response": response,
	}
	if err := s.writeEvent(event); err != nil {
		return
	}
	_ = s.writeDone()
}

func (s *codexMitigationState) writeSyntheticIncomplete(reason string) {
	if s.writeErr != nil {
		return
	}
	response := codexMitigationCloneMap(s.baseResponse)
	if response == nil {
		response = map[string]any{}
	}
	response["status"] = "incomplete"
	response["incomplete_details"] = map[string]any{"reason": reason}
	response["output"] = s.finalOutput
	s.attachMetadataAndUsage(response, reason)

	event := map[string]any{
		"type":     "response.incomplete",
		"response": response,
	}
	if err := s.writeEvent(event); err != nil {
		return
	}
	_ = s.writeDone()
}

func (s *codexMitigationState) attachMetadataAndUsage(response map[string]any, stoppedReason string) {
	metadata, _ := response["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["proxy_rounds"] = s.rounds
	metadata["proxy_billed_usage"] = s.usage.proxyBilledUsage()
	if stoppedReason != "" {
		metadata["proxy_stopped_reason"] = stoppedReason
	}
	metadata["codex_degradation_mitigation"] = map[string]any{
		"enabled": true,
		"step":    codexMitigationDefaultStep,
		"formula": "reasoning_tokens >= step - 2 && (reasoning_tokens + 2) % step == 0",
	}
	response["metadata"] = metadata
	if usage := s.usage.publicUsage(); usage != nil {
		response["usage"] = usage
		outputDetails, _ := usage["output_tokens_details"].(map[string]any)
		setTokenUsage(s.c, tokenusage.Usage{
			InputTokens:    codexMitigationInt64FromAny(usage["input_tokens"]),
			OutputTokens:   codexMitigationInt64FromAny(usage["output_tokens"]),
			TotalTokens:    codexMitigationInt64FromAny(usage["total_tokens"]),
			ThinkingTokens: codexMitigationInt64FromAny(outputDetails["reasoning_tokens"]),
		})
	}
}

func (s *codexMitigationState) writeEvent(event map[string]any) error {
	if event == nil {
		return nil
	}
	codexMitigationSetSequence(event, s.sequence)
	s.sequence++
	eventType := codexMitigationEventType(event)
	if eventType == "" {
		eventType = "message"
	}
	data, err := json.Marshal(event)
	if err != nil {
		s.writeErr = err
		return err
	}
	if _, err := fmt.Fprintf(s.c.Writer, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
		s.writeErr = err
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *codexMitigationState) writeDone() error {
	if _, err := io.WriteString(s.c.Writer, "data: [DONE]\n\n"); err != nil {
		s.writeErr = err
		return err
	}
	s.flusher.Flush()
	return nil
}

func codexMitigationParseSSEBlock(lines []string) map[string]any {
	if len(lines) == 0 {
		return nil
	}
	eventType := ""
	dataLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := codexMitigationSSEField(line, "event"); ok {
			eventType = value
			continue
		}
		if value, ok := codexMitigationSSEField(line, "data"); ok {
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return nil
	}
	data := strings.Join(dataLines, "\n")
	if strings.TrimSpace(data) == "[DONE]" {
		return nil
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		logrus.WithError(err).Debug("Codex degradation mitigation: failed to parse SSE event")
		return nil
	}
	if _, ok := event["type"]; !ok && eventType != "" {
		event["type"] = eventType
	}
	return event
}

func codexMitigationSSEField(line, field string) (string, bool) {
	prefix := field + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(line, prefix)
	if strings.HasPrefix(value, " ") {
		value = strings.TrimPrefix(value, " ")
	}
	return value, true
}

func codexMitigationResponseReader(resp *http.Response) (io.Reader, io.Closer, error) {
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		reader, err := utils.NewDecompressReader(encoding, resp.Body)
		if err != nil {
			return nil, nil, err
		}
		if closer, ok := reader.(io.Closer); ok {
			return reader, closer, nil
		}
		return reader, nil, nil
	}
	return resp.Body, nil, nil
}

func codexMitigationIsSSE(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

func codexMitigationEventType(event map[string]any) string {
	if event == nil {
		return ""
	}
	value, _ := event["type"].(string)
	return value
}

func codexMitigationTerminalEvent(eventType string) bool {
	return eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed"
}

func codexMitigationMapField(event map[string]any, key string) map[string]any {
	if event == nil {
		return nil
	}
	value, _ := event[key].(map[string]any)
	return value
}

func codexMitigationOutputItemType(event map[string]any) string {
	item := codexMitigationMapField(event, "item")
	if item == nil {
		return ""
	}
	value, _ := item["type"].(string)
	return value
}

func codexMitigationOutputIndexKey(event map[string]any) string {
	if event == nil {
		return ""
	}
	value, ok := event["output_index"]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func codexMitigationSetOutputIndex(event map[string]any, outputIndex int) {
	if event == nil {
		return
	}
	if _, ok := event["output_index"]; ok {
		event["output_index"] = outputIndex
	}
}

func codexMitigationSetSequence(event map[string]any, sequence int64) {
	if event != nil {
		event["sequence_number"] = sequence
	}
}

func codexMitigationHasEncryptedContent(item map[string]any) bool {
	if item == nil {
		return false
	}
	value, _ := item["encrypted_content"].(string)
	return value != ""
}

func codexMitigationReasoningTokensFromTerminal(terminal map[string]any) *int64 {
	usage := codexMitigationUsageFromTerminal(terminal)
	if usage == nil {
		return nil
	}
	details, _ := usage["output_tokens_details"].(map[string]any)
	if details == nil {
		return nil
	}
	value, ok := details["reasoning_tokens"]
	if !ok {
		return nil
	}
	tokens := codexMitigationInt64FromAny(value)
	return &tokens
}

func codexMitigationUsageFromTerminal(terminal map[string]any) map[string]any {
	response := codexMitigationMapField(terminal, "response")
	if response == nil {
		return nil
	}
	return codexMitigationMapField(response, "usage")
}

func (u *codexMitigationUsageAccumulator) addRound(usage map[string]any) {
	if usage == nil {
		return
	}
	input := codexMitigationInt64FromAny(usage["input_tokens"])
	output := codexMitigationInt64FromAny(usage["output_tokens"])
	total := codexMitigationInt64FromAny(usage["total_tokens"])
	if total <= 0 {
		total = input + output
	}
	cached := int64(0)
	if details, _ := usage["input_tokens_details"].(map[string]any); details != nil {
		cached = codexMitigationInt64FromAny(details["cached_tokens"])
	}
	reasoning := int64(0)
	if details, _ := usage["output_tokens_details"].(map[string]any); details != nil {
		reasoning = codexMitigationInt64FromAny(details["reasoning_tokens"])
	}
	if !u.sawUsage {
		u.firstInput = input
		u.firstCached = cached
	}
	u.sawUsage = true
	u.proxyInput += input
	u.proxyOutput += output
	u.proxyTotal += total
	u.proxyCached += cached
	u.totalReasoning += reasoning
	u.finalOutput = output
	u.finalReasoning = reasoning
}

func (u *codexMitigationUsageAccumulator) publicUsage() map[string]any {
	if u == nil || !u.sawUsage {
		return nil
	}
	finalVisible := u.finalOutput - u.finalReasoning
	if finalVisible < 0 {
		finalVisible = 0
	}
	output := u.totalReasoning + finalVisible
	usage := map[string]any{
		"input_tokens":  u.firstInput,
		"output_tokens": output,
		"total_tokens":  u.firstInput + output,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": u.totalReasoning,
		},
	}
	if u.firstCached > 0 {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": u.firstCached}
	}
	return usage
}

func (u *codexMitigationUsageAccumulator) proxyBilledUsage() map[string]any {
	if u == nil || !u.sawUsage {
		return map[string]any{}
	}
	usage := map[string]any{
		"input_tokens":  u.proxyInput,
		"output_tokens": u.proxyOutput,
		"total_tokens":  u.proxyTotal,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": u.totalReasoning,
		},
	}
	if u.proxyCached > 0 {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": u.proxyCached}
	}
	return usage
}

func codexMitigationInt64FromAny(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		i, _ := v.Int64()
		return i
	default:
		return 0
	}
}

func codexMitigationNullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func codexMitigationCloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func codexMitigationJSONSize(value any) int {
	if value == nil {
		return 0
	}
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(data)
}

func codexMitigationRequestWithBody(ctx context.Context, template *http.Request, body []byte) (*http.Request, error) {
	if template == nil {
		return nil, errors.New("nil request template")
	}
	req, err := http.NewRequestWithContext(ctx, template.Method, template.URL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header = template.Header.Clone()
	req.Header.Del("Content-Length")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return req, nil
}
