package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// maxContentBufferBytes limits how much assistant content we buffer when
// reconstructing the XML block for function call. This avoids unbounded
// memory growth for very long streaming responses.
const maxContentBufferBytes = 256 * 1024

// functionCall represents a parsed tool call from the XML block.
type functionCall struct {
	Name string
	Args map[string]any
}

var (
	reFunctionCallsBlock = regexp.MustCompile(`(?s)<function_calls>(.*?)</function_calls>`)
	reFunctionCallBlock  = regexp.MustCompile(`(?s)<function_call>(.*?)</function_call>`)
	reToolTag            = regexp.MustCompile(`(?s)<(?:tool|tool_name|invocationName)>(.*?)</(?:tool|tool_name|invocationName)>`)
	reInvocationTag      = regexp.MustCompile(`(?s)<(?:invocation|invoke)\s+name="([^"]+)"[^>]*>(.*?)</(?:invocation|invoke)>`)
	reArgsBlock          = regexp.MustCompile(`(?s)<args>(.*?)</args>`)
	reParamsBlock        = regexp.MustCompile(`(?s)<parameters>(.*?)</parameters>`)
	reMcpParam           = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"[^>]*>(.*?)</parameter>`)
	reGenericParam       = regexp.MustCompile(`(?s)<([^\s>/]+)>(.*?)</([^\s>/]+)>`)
	reToolCallBlock      = regexp.MustCompile(`(?s)<tool_call\s+name="([^"]+)"[^>]*>(.*?)</tool_call>`)
	reTriggerSignal      = regexp.MustCompile(`<Function_[a-zA-Z0-9]+_Start/>`)
)

// applyFunctionCallRequestRewrite rewrites an OpenAI chat completions request body
// to enable middleware-based function call. It injects a system prompt describing
// available tools and removes native tools/tool_choice fields so the upstream model
// only sees the prompt-based contract.
//
// Returns:
//   - rewritten body bytes (or the original body if no rewrite is needed)
//   - trigger signal string used to mark the function-calls XML section
//   - error when parsing fails (in which case the caller should fall back to the
//     original body)
func (ps *ProxyServer) applyFunctionCallRequestRewrite(
    group *models.Group,
    bodyBytes []byte,
) ([]byte, string, error) {
    if len(bodyBytes) == 0 {
        return bodyBytes, "", nil
    }

    var req map[string]any
    if err := json.Unmarshal(bodyBytes, &req); err != nil {
        logrus.WithError(err).WithField("group", group.Name).
            Warn("Failed to unmarshal request body for function call rewrite")
        return bodyBytes, "", err
    }

    toolsVal, ok := req["tools"]
    if !ok {
        // No tools configured – nothing to do.
        return bodyBytes, "", nil
    }

    toolsSlice, ok := toolsVal.([]any)
    if !ok || len(toolsSlice) == 0 {
        return bodyBytes, "", nil
    }

    // Extract messages array if present.
    msgsVal, hasMessages := req["messages"]
    var messages []any
    if hasMessages {
        if arr, ok := msgsVal.([]any); ok {
            messages = arr
        }
    }

    // Generate a lightweight trigger signal for this request.
    triggerSignal := "<Function_" + utils.GenerateRandomSuffix() + "_Start/>"

    // Build tools description block.
    var toolBlocks []string
    for i, t := range toolsSlice {
        toolMap, ok := t.(map[string]any)
        if !ok {
            continue
        }
        funcVal, ok := toolMap["function"]
        if !ok {
            continue
        }
        fn, ok := funcVal.(map[string]any)
        if !ok {
            continue
        }

        name, _ := fn["name"].(string)
        if name == "" {
            continue
        }
        desc, _ := fn["description"].(string)
        params, _ := fn["parameters"].(map[string]any)

        // Build a simple parameters summary from JSON schema properties.
        paramSummary := "None"
        if params != nil {
            if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
                pairs := make([]string, 0, len(props))
                for pName, pInfo := range props {
                    pType := "any"
                    if infoMap, ok := pInfo.(map[string]any); ok {
                        if tVal, ok := infoMap["type"].(string); ok && tVal != "" {
                            pType = tVal
                        }
                    }
                    pairs = append(pairs, pName+" ("+pType+")")
                }
                if len(pairs) > 0 {
                    paramSummary = strings.Join(pairs, ", ")
                }
            }
        }

        block := ""
        block += fmt.Sprintf("%d. <tool name=\"%s\">\n", i+1, name)
        block += "   Description:\n"
        if desc != "" {
            block += "```\n" + desc + "\n```\n"
        } else {
            block += "None\n"
        }
        block += "   Parameters summary: " + paramSummary
        toolBlocks = append(toolBlocks, block)
    }

    if len(toolBlocks) == 0 {
        return bodyBytes, "", nil
    }

    // Compose final prompt content injected as a new system message.
    prompt := "You are a function call coordinator. After answering the user in natural language, " +
        "you MUST output function calls in an XML block after the trigger signal.\n" +
        "Trigger signal: " + triggerSignal + "\n\n" +
        "Tools:\n" + strings.Join(toolBlocks, "\n\n") + "\n\n" +
        "When you need to call tools, append a block like:\n" +
        "<function_calls>... XML ...</function_calls> after the trigger signal."

    newMessages := make([]any, 0, len(messages)+1)
    newMessages = append(newMessages, map[string]any{
        "role":    "system",
        "content": prompt,
    })
    if len(messages) > 0 {
        newMessages = append(newMessages, messages...)
    }
    req["messages"] = newMessages

    // Remove native tools-related fields to avoid confusing upstream implementations.
    delete(req, "tools")
    delete(req, "tool_choice")

    rewritten, err := json.Marshal(req)
    if err != nil {
        logrus.WithError(err).WithField("group", group.Name).
            Warn("Failed to marshal request after function call rewrite")
        return bodyBytes, "", err
    }

    // Debug: log a safe preview of request body before and after rewrite.
    logFields := logrus.Fields{
        "group":              group.Name,
        "trigger_signal":     triggerSignal,
        "tools_count":        len(toolsSlice),
        "original_body_bytes": len(bodyBytes),
        "rewritten_bytes":    len(rewritten),
        "original_preview":    previewForLog(bodyBytes, 512),
        "rewritten_preview":   previewForLog(rewritten, 512),
    }
    logrus.WithFields(logFields).Debug("Function call request rewrite: before/after body (truncated)")

    return rewritten, triggerSignal, nil
}

// handleFunctionCallNormalResponse handles non-streaming chat completion responses
// when function call middleware is enabled for the request. It parses the assistant
// message content for XML-based function calls and converts them into OpenAI-compatible
// tool_calls in the response payload.
//
// NOTE: The fallback branches which write the original body back to the client are
// intentionally kept inline instead of being extracted into a helper. This keeps the
// control flow explicit in this hot path and avoids adding another function call
// layer, even though automated reviews may suggest refactoring for deduplication.
func (ps *ProxyServer) handleFunctionCallNormalResponse(c *gin.Context, resp *http.Response) {
    shouldCapture := shouldCaptureResponse(c)

    rawBody, err := io.ReadAll(resp.Body)
    if err != nil {
        logUpstreamError("reading response body", err)
        return
    }
    body := handleGzipCompression(resp, rawBody)

    // Fallback: if we cannot parse JSON, behave like normal response handler.
    var payload map[string]any
    if err := json.Unmarshal(body, &payload); err != nil {
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    // Retrieve trigger signal stored during request rewrite.
    triggerVal, exists := c.Get(ctxKeyTriggerSignal)
    if !exists {
        // No trigger signal means this request was not rewritten for function call.
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    triggerSignal, ok := triggerVal.(string)
    if !ok || triggerSignal == "" {
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    choicesVal, ok := payload["choices"]
    if !ok {
        // No choices field, fallback to original payload.
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    choices, ok := choicesVal.([]any)
    if !ok || len(choices) == 0 {
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    modified := false

    // Debug: basic info when normal function-call handler is active.
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            logrus.WithFields(logrus.Fields{
                "group":          g.Name,
                "trigger_signal": triggerSignal,
                "choices_len":    len(choices),
            }).Debug("Function call normal response: handler activated")
        }
    }

    for i, ch := range choices {
        chMap, ok := ch.(map[string]any)
        if !ok {
            continue
        }

        msgVal, ok := chMap["message"]
        if !ok {
            continue
        }
        msg, ok := msgVal.(map[string]any)
        if !ok {
            continue
        }

        contentVal, ok := msg["content"]
        if !ok {
            continue
        }
        contentStr, ok := contentVal.(string)
        if !ok || contentStr == "" {
            continue
        }

        calls := parseFunctionCallsXML(contentStr, triggerSignal)
        if len(calls) == 0 {
            continue
        }

        // Build OpenAI-compatible tool_calls array.
        toolCalls := make([]map[string]any, 0, len(calls))
        for _, call := range calls {
            if call.Name == "" {
                continue
            }
            argsJSON, err := json.Marshal(call.Args)
            if err != nil {
                logrus.WithError(err).Debug("Failed to marshal function call arguments, skipping this call")
                continue
            }

            toolCalls = append(toolCalls, map[string]any{
                "id":   "call_" + utils.GenerateRandomSuffix(),
                "type": "function",
                "function": map[string]any{
                    "name":      call.Name,
                    "arguments": string(argsJSON),
                },
            })
        }

        if len(toolCalls) == 0 {
            continue
        }

        // Debug per-choice: how many calls were parsed.
        logrus.WithFields(logrus.Fields{
            "trigger_signal":  triggerSignal,
            "tool_call_count": len(toolCalls),
        }).Debug("Function call normal response: parsed tool calls for choice")

        msg["tool_calls"] = toolCalls
        // Remove function_calls XML blocks from visible content so end users
        // only see natural language text, while AI internally processes tool calls.
        msg["content"] = removeFunctionCallsBlocks(contentStr)
        chMap["message"] = msg
        chMap["finish_reason"] = "tool_calls"
        choices[i] = chMap
        modified = true
    }

    if !modified {
        // No valid function calls parsed, fall back to original payload.
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    payload["choices"] = choices

    out, err := json.Marshal(payload)
    if err != nil {
        logrus.WithError(err).Warn("Failed to marshal modified function call response, falling back to original body")
        if shouldCapture {
            if len(body) > maxResponseCaptureBytes {
                c.Set("response_body", string(body[:maxResponseCaptureBytes]))
            } else {
                c.Set("response_body", string(body))
            }
        }
        if _, werr := c.Writer.Write(body); werr != nil {
            logUpstreamError("writing response body", werr)
        }
        return
    }

    // Debug: log before/after preview for non-streaming response when modification succeeded.
    fcFields := logrus.Fields{
        "trigger_signal":      triggerSignal,
        "original_body_bytes": len(body),
        "modified_bytes":      len(out),
        "original_preview":    previewForLog(body, 512),
        "modified_preview":    previewForLog(out, 512),
    }
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            fcFields["group"] = g.Name
        }
    }
    logrus.WithFields(fcFields).Debug("Function call normal response: body before/after (truncated)")

    // Store captured response in context for logging
    if shouldCapture {
        if len(out) > maxResponseCaptureBytes {
            c.Set("response_body", string(out[:maxResponseCaptureBytes]))
        } else {
            c.Set("response_body", string(out))
        }
    }

    if _, werr := c.Writer.Write(out); werr != nil {
        logUpstreamError("writing response body", werr)
    }
}

func (ps *ProxyServer) handleFunctionCallStreamingResponse(c *gin.Context, resp *http.Response) {
    // Set standard SSE headers
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")
    c.Header("X-Accel-Buffering", "no")

    flusher, ok := c.Writer.(http.Flusher)
    if !ok {
        logrus.Error("Streaming unsupported by the writer, falling back to normal response")
        ps.handleFunctionCallNormalResponse(c, resp)
        return
    }

    // Retrieve trigger signal for this request; if missing, fallback to normal streaming.
    triggerVal, exists := c.Get(ctxKeyTriggerSignal)
    triggerSignal, ok := triggerVal.(string)
    if !exists || !ok || triggerSignal == "" {
        ps.handleStreamingResponse(c, resp)
        return
    }

    reader := bufio.NewReader(resp.Body)
    // contentBuf accumulates assistant text content across all chunks.
    var contentBuf strings.Builder

    // prevEvent holds the last non-[DONE] event that we have not yet forwarded.
    var prevEventLines []string
    var prevEventData string
    seenAnyEvent := false
    // Track whether we are inside a <function_calls> block to suppress XML from streaming output.
    insideFunctionCalls := false

    writeEvent := func(lines []string) error {
        if len(lines) == 0 {
            return nil
        }
        for _, line := range lines {
            if _, err := c.Writer.Write([]byte(line)); err != nil {
                return err
            }
        }
        // Each SSE event is terminated by a blank line.
        if _, err := c.Writer.Write([]byte("\n")); err != nil {
            return err
        }
        flusher.Flush()
        return nil
    }

    for {
        // Read a single SSE event (sequence of lines terminated by a blank line).
        var rawLines []string
        var dataBuf strings.Builder
        for {
            line, err := reader.ReadString('\n')
            if err != nil {
                if err == io.EOF {
                    // Treat a non-empty line+EOF as part of the current event, then
                    // process the accumulated event before returning. This avoids
                    // dropping the last partial event when upstream closes without a
                    // trailing newline.
                    trimmed := strings.TrimRight(line, "\r\n")
                    if trimmed != "" {
                        rawLines = append(rawLines, line)
                        if strings.HasPrefix(trimmed, "data:") {
                            dataLine := strings.TrimSpace(trimmed[len("data:"):])
                            if dataLine != "" {
                                if dataBuf.Len() > 0 {
                                    dataBuf.WriteByte('\n')
                                }
                                dataBuf.WriteString(dataLine)
                            }
                        }
                    }
                    break
                }
                // Non-EOF error: abort streaming.
                logUpstreamError("reading from upstream", err)
                return
            }

            trimmed := strings.TrimRight(line, "\r\n")
            if trimmed == "" {
                // End of current event
                break
            }

            rawLines = append(rawLines, line)
            if strings.HasPrefix(trimmed, "data:") {
                dataLine := strings.TrimSpace(trimmed[len("data:"):])
                if dataLine != "" {
                    if dataBuf.Len() > 0 {
                        dataBuf.WriteByte('\n')
                    }
                    dataBuf.WriteString(dataLine)
                }
            }
        }

        if len(rawLines) == 0 && dataBuf.Len() == 0 {
            // Skip spurious empty events
            continue
        }

        dataStr := dataBuf.String()
        if dataStr == "" && len(rawLines) == 0 {
            continue
        }

        // Handle [DONE] sentinel: we do not forward it immediately, so we can emit
        // a final tool_calls event before closing the stream.
        if strings.TrimSpace(dataStr) == "[DONE]" {
            break
        }

        seenAnyEvent = true

        // Parse current chunk, accumulate content, and strip XML blocks in real-time.
        var modifiedLines []string
        if dataStr != "" {
            var evt map[string]any
            if err := json.Unmarshal([]byte(dataStr), &evt); err == nil {
                if choicesVal, ok := evt["choices"]; ok {
                    if choices, ok := choicesVal.([]any); ok && len(choices) > 0 {
                        if ch, ok := choices[0].(map[string]any); ok {
                            if deltaVal, ok := ch["delta"].(map[string]any); ok {
                                if text, ok := deltaVal["content"].(string); ok && text != "" {
                                    // Accumulate content for final XML parsing.
                                    if contentBuf.Len()+len(text) <= maxContentBufferBytes {
                                        contentBuf.WriteString(text)
                                    }

                                    // State machine: suppress XML blocks from visible streaming output.
                                    hasOpen := strings.Contains(text, "<function_calls>")
                                    hasClose := strings.Contains(text, "</function_calls>")

                                    if hasOpen && hasClose {
                                        // Entire block in one chunk: strip everything between tags.
                                        if idx := strings.Index(text, "<function_calls>"); idx >= 0 {
                                            deltaVal["content"] = text[:idx]
                                        }
                                    } else if hasOpen {
                                        // Start of XML block: keep text before tag, suppress rest.
                                        insideFunctionCalls = true
                                        if idx := strings.Index(text, "<function_calls>"); idx >= 0 {
                                            deltaVal["content"] = text[:idx]
                                        }
                                    } else if hasClose {
                                        // End of XML block: suppress this chunk entirely.
                                        insideFunctionCalls = false
                                        deltaVal["content"] = ""
                                    } else if insideFunctionCalls {
                                        // Inside XML block: suppress all content.
                                        deltaVal["content"] = ""
                                    }
                                    // If none of above, text is normal and remains unchanged.
                                }
                            }
                        }
                    }
                }
                // Re-serialize with modified content for forwarding.
                if modifiedData, err := json.Marshal(evt); err == nil {
                    modifiedLines = []string{"data: " + string(modifiedData) + "\n"}
                } else {
                    // Fallback to original on marshal error.
                    modifiedLines = rawLines
                }
            } else {
                // Fallback to original on unmarshal error.
                modifiedLines = rawLines
            }
        } else {
            modifiedLines = rawLines
        }

        // Forward previous event (which has already been processed).
        if len(prevEventLines) > 0 {
            if err := writeEvent(prevEventLines); err != nil {
                logUpstreamError("writing stream to client", err)
                return
            }
        }

        // Save current event (with XML stripped) as the new previous event.
        prevEventLines = append([]string(nil), modifiedLines...)
        prevEventData = dataStr
    }

    // If we have never seen any event, simply send [DONE] and return.
    if !seenAnyEvent {
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // At this point, prevEventLines holds the last event before [DONE]. Attempt to
    // parse function calls from the accumulated content buffer.
    parsedCalls := parseFunctionCallsXML(contentBuf.String(), triggerSignal)
    if len(parsedCalls) == 0 || prevEventData == "" {
        // No function calls detected – forward last event as-is, then [DONE].
        if len(prevEventLines) > 0 {
            if err := writeEvent(prevEventLines); err != nil {
                logUpstreamError("writing stream to client", err)
                return
            }
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // Modify the last event to include tool_calls and finish_reason "tool_calls".
    var lastEvt map[string]any
    if err := json.Unmarshal([]byte(prevEventData), &lastEvt); err != nil {
        logUpstreamError("parsing last streaming event for function calls", err)
        if len(prevEventLines) > 0 {
            _ = writeEvent(prevEventLines)
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    choicesVal, ok := lastEvt["choices"]
    if !ok {
        if len(prevEventLines) > 0 {
            _ = writeEvent(prevEventLines)
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }
    choices, ok := choicesVal.([]any)
    if !ok || len(choices) == 0 {
        if len(prevEventLines) > 0 {
            _ = writeEvent(prevEventLines)
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // Build tool_calls payload from parsed calls. We include an explicit
    // zero-based index field to better align with OpenAI streaming examples
    // and to help strict clients correlate incremental tool call events.
    toolCalls := make([]map[string]any, 0, len(parsedCalls))
    index := 0
    for _, call := range parsedCalls {
        if call.Name == "" {
            continue
        }
        argsJSON, err := json.Marshal(call.Args)
        if err != nil {
            logrus.WithError(err).Debug("Failed to marshal function call arguments in streaming, skipping this call")
            continue
        }
        toolCalls = append(toolCalls, map[string]any{
            "index": index,
            "id":    "call_" + utils.GenerateRandomSuffix(),
            "type":  "function",
            "function": map[string]any{
                "name":      call.Name,
                "arguments": string(argsJSON),
            },
        })
        index++
    }

    if len(toolCalls) == 0 {
        if len(prevEventLines) > 0 {
            _ = writeEvent(prevEventLines)
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // Update the first choice (index 0) with tool_calls delta.
    if ch, ok := choices[0].(map[string]any); ok {
        // Ensure delta map exists.
        var delta map[string]any
        if dv, ok := ch["delta"].(map[string]any); ok {
            delta = dv
        } else {
            delta = make(map[string]any)
        }
        // Remove content field from the last chunk to avoid duplicating XML in plain text.
        delete(delta, "content")
        delta["tool_calls"] = toolCalls
        ch["delta"] = delta
        ch["finish_reason"] = "tool_calls"
        choices[0] = ch
    }
    lastEvt["choices"] = choices

    out, err := json.Marshal(lastEvt)
    if err != nil {
        logUpstreamError("marshalling modified streaming function call event", err)
        if len(prevEventLines) > 0 {
            _ = writeEvent(prevEventLines)
        }
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // Debug: log before/after preview for streaming last event when modification succeeded.
    streamFields := logrus.Fields{
        "trigger_signal":       triggerSignal,
        "parsed_call_count":    len(parsedCalls),
        "last_event_bytes":     len(prevEventData),
        "modified_event_bytes": len(out),
        "last_event_preview":   utils.TruncateString(prevEventData, 512),
        "modified_preview":     previewForLog(out, 512),
    }
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            streamFields["group"] = g.Name
        }
    }
    logrus.WithFields(streamFields).Debug("Function call streaming response: last event before/after (truncated)")

    // Emit the modified last event followed by [DONE]. We preserve any upstream
    // SSE metadata lines (such as id: / event:) by reusing prevEventLines and
    // only replacing data: lines with our modified payload.
    finalLines := make([]string, 0, len(prevEventLines)+1)
    for _, line := range prevEventLines {
        trimmed := strings.TrimRight(line, "\r\n")
        if strings.HasPrefix(trimmed, "data:") {
            // Skip original data: lines; they will be replaced with the modified one
            // below.
            continue
        }
        finalLines = append(finalLines, line)
    }
    finalLines = append(finalLines, "data: "+string(out)+"\n")
    if err := writeEvent(finalLines); err != nil {
        logUpstreamError("writing modified streaming event", err)
        return
    }
    _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
    flusher.Flush()
}

// previewForLog returns a safe, truncated string representation of a byte slice
// for logging purposes. It avoids logging excessively large bodies while still
// providing useful context. The truncation is rune-aware via utils.TruncateString
// to avoid cutting multi-byte UTF-8 characters in the middle.
func previewForLog(b []byte, max int) string {
    if b == nil {
        return ""
    }
    s := string(b)
    if max <= 0 {
        return s
    }
    return utils.TruncateString(s, max)
}

// removeThinkBlocks temporarily removes all <think>...</think> blocks from the
// input text. This is used to avoid interfering with XML parsing while keeping
// the original content intact for the user.
func removeThinkBlocks(text string) string {
    for {
        start := strings.Index(text, "<think>")
        if start == -1 {
            break
        }
        // Simple non-nested removal to keep implementation lightweight.
        end := strings.Index(text[start:], "</think>")
        if end == -1 {
            break
        }
        end += start + len("</think>")
        text = text[:start] + text[end:]
    }
    return text
}

// removeFunctionCallsBlocks removes all <function_calls>...</function_calls> blocks
// from the text, including any trigger signals immediately preceding them. This ensures
// that end users see only natural language output while the system internally extracts
// and processes tool calls.
func removeFunctionCallsBlocks(text string) string {
	if text == "" {
		return text
	}

	// Remove all <function_calls> blocks using a greedy approach. We use (?s) to
	// enable dot-all mode for multi-line matching.
	cleaned := reFunctionCallsBlock.ReplaceAllString(text, "")

	// Also remove any orphaned trigger signals (format: <Function_xxx_Start/>).
	// This regex matches the pattern generated by applyFunctionCallRequestRewrite.
	cleaned = reTriggerSignal.ReplaceAllString(cleaned, "")

    // Trim excessive whitespace that may remain after block removal.
    cleaned = strings.TrimSpace(cleaned)

    return cleaned
}

// ... (rest of the code remains the same)

// parseFunctionCallsXML parses function calls from the assistant content using a
// trigger signal and a simple XML-like convention:
//
// <function_calls>
//   <function_calls>
//     <tool>tool_name</tool>
//     <args>
//       <param1>"value"</param1>
//       <param2>123</param2>
//     </args>
// </function_calls>
func parseFunctionCallsXML(text, triggerSignal string) []functionCall {
	if text == "" {
		return nil
	}

	cleaned := removeThinkBlocks(text)

	// Prefer to anchor parsing near the last trigger signal when the model
	// respects it. However, some upstream models may emit the <function_calls>
	// block without repeating the trigger. In that case, we gracefully fall
	// back to the last <function_calls> occurrence so that function call behavior
	// remains robust.
	start := 0
	if triggerSignal != "" {
		if idx := strings.LastIndex(cleaned, triggerSignal); idx != -1 {
			start = idx
		} else if idx := strings.LastIndex(cleaned, "<function_calls>"); idx != -1 {
			start = idx
		}
	} else {
		if idx := strings.LastIndex(cleaned, "<function_calls>"); idx != -1 {
			start = idx
		}
	}
	segment := cleaned[start:]

	// Extract content inside <function_calls>...</function_calls> using shared
	// precompiled patterns.
	fcMatch := reFunctionCallsBlock.FindStringSubmatch(segment)
	if len(fcMatch) < 2 {
		return nil
	}
	callsContent := fcMatch[1]

	// Extract each <function_call>...</function_call> block. Some MCP-style
	// models instead emit top-level <tool_call name="...">...
	// blocks directly under <function_calls>, so we handle those separately
	// below.
	callMatches := reFunctionCallBlock.FindAllStringSubmatch(callsContent, -1)

	// Pre-allocate with estimated capacity: each function_call block may contain
	// multiple <invoke> tags, so we use a generous estimate to reduce reallocation.
	results := make([]functionCall, 0, len(callMatches)*2)

	// First, handle traditional <function_call> blocks.
	for _, m := range callMatches {
		block := m[1]

		// Check for MCP-style invocation/invoke blocks with name attribute.
		// Multiple invocation blocks within a single function_call are supported,
		// and each becomes a separate tool call.
		invMatches := reInvocationTag.FindAllStringSubmatch(block, -1)
		if len(invMatches) > 0 {
			for _, invMatch := range invMatches {
				if len(invMatch) < 3 {
					continue
				}
				name := strings.TrimSpace(invMatch[1])
				if name == "" {
					continue
				}
				argsContent := invMatch[2]
				args := extractParameters(argsContent, reMcpParam, reGenericParam)
				results = append(results, functionCall{Name: name, Args: args})
			}
			continue
		}

		// Fallback: resolve tool name from traditional <tool> or <tool_name> tags.
		name := ""
		var argsContent string
		toolMatch := reToolTag.FindStringSubmatch(block)
		if len(toolMatch) >= 2 {
			name = strings.TrimSpace(toolMatch[1])
		}
		if name == "" {
			continue
		}

		if argsContent == "" {
			// Fallback to traditional <args> or <parameters> wrappers when no
			// invocation inner block was detected or provided.
			argsBlockMatch := reArgsBlock.FindStringSubmatch(block)
			if len(argsBlockMatch) < 2 {
				// Fallback: support <parameters>...</parameters> shape.
				argsBlockMatch = reParamsBlock.FindStringSubmatch(block)
			}
			if len(argsBlockMatch) >= 2 {
				argsContent = argsBlockMatch[1]
			}
		}

		args := extractParameters(argsContent, reMcpParam, reGenericParam)
		results = append(results, functionCall{Name: name, Args: args})
	}

	// Additionally support top-level MCP-style <tool_call name="..."> blocks
	// directly under <function_calls>. Each  becomes a separate
	// functionCall entry.
	toolCallMatches := reToolCallBlock.FindAllStringSubmatch(callsContent, -1)
	for _, tm := range toolCallMatches {
		if len(tm) < 3 {
			continue
		}
		name := strings.TrimSpace(tm[1])
		if name == "" {
			continue
		}
		argsContent := tm[2]
		args := extractParameters(argsContent, reMcpParam, reGenericParam)
		results = append(results, functionCall{Name: name, Args: args})
	}

	if len(results) == 0 {
		return nil
	}
	return results
}

// extractParameters parses parameter tags from content, supporting both
// MCP-style <parameter name="key" type="...">value</parameter> and
// traditional <key>value</key> formats. Returns a map of parsed arguments.
func extractParameters(content string, mcpParamRe, argRe *regexp.Regexp) map[string]any {
    args := make(map[string]any)
    if content == "" {
        return args
    }

    // First attempt: MCP parameter blocks with name attributes.
    mcpMatches := mcpParamRe.FindAllStringSubmatch(content, -1)
    if len(mcpMatches) > 0 {
        for _, pm := range mcpMatches {
            if len(pm) < 3 {
                continue
            }
            key := strings.TrimSpace(pm[1])
            valStr := strings.TrimSpace(pm[2])
            if key == "" {
                continue
            }
            args[key] = parseValueOrString(valStr)
        }
        return args
    }

    // Fallback: traditional tag-based parameters.
    argMatches := argRe.FindAllStringSubmatch(content, -1)
    for _, am := range argMatches {
        // Expect: 0 = full match, 1 = open tag, 2 = inner text, 3 = close tag.
        if len(am) < 4 {
            continue
        }
        openTag := strings.TrimSpace(am[1])
        closeTag := strings.TrimSpace(am[3])
        if openTag == "" || closeTag == "" || openTag != closeTag {
            continue
        }
        key := openTag
        valStr := strings.TrimSpace(am[2])
        if key == "" {
            continue
        }
        args[key] = parseValueOrString(valStr)
    }
    return args
}

// parseValueOrString attempts to parse the input string as JSON; if that fails,
// it returns the string as-is. This avoids duplicating the same parse-or-fallback
// logic across multiple call sites.
func parseValueOrString(s string) any {
    var val any
    if err := json.Unmarshal([]byte(s), &val); err != nil {
        return s
    }
    return val
}
