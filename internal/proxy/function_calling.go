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

// functionCall represents a parsed tool call from the XML block.
type functionCall struct {
    Name string
    Args map[string]any
}

// applyFunctionCallingRequestRewrite rewrites an OpenAI chat completions request body
// to enable middleware-based function calling. It injects a system prompt describing
// available tools and removes native tools/tool_choice fields so the upstream model
// only sees the prompt-based contract.
//
// Returns:
//   - rewritten body bytes (or the original body if no rewrite is needed)
//   - trigger signal string used to mark the function-calls XML section
//   - error when parsing fails (in which case the caller should fall back to the
//     original body)
func (ps *ProxyServer) applyFunctionCallingRequestRewrite(
    group *models.Group,
    bodyBytes []byte,
) ([]byte, string, error) {
    if len(bodyBytes) == 0 {
        return bodyBytes, "", nil
    }

    var req map[string]any
    if err := json.Unmarshal(bodyBytes, &req); err != nil {
        logrus.WithError(err).WithField("group", group.Name).
            Warn("Failed to unmarshal request body for function-calling rewrite")
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
    prompt := "You are a function-calling coordinator. After answering the user in natural language, " +
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
            Warn("Failed to marshal request after function-calling rewrite")
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
    logrus.WithFields(logFields).Debug("Function calling request rewrite: before/after body (truncated)")

    return rewritten, triggerSignal, nil
}

// handleFunctionCallingNormalResponse handles non-streaming chat completion responses
// when function calling middleware is enabled for the request. It parses the assistant
// message content for XML-based function calls and converts them into OpenAI-compatible
// tool_calls in the response payload.
func (ps *ProxyServer) handleFunctionCallingNormalResponse(c *gin.Context, resp *http.Response) {
    shouldCapture := shouldCaptureResponse(c)

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        logUpstreamError("reading response body", err)
        return
    }

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
        // No trigger signal means this request was not rewritten for function calling.
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

    // Debug: basic info when normal function-calling handler is active.
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            logrus.WithFields(logrus.Fields{
                "group":          g.Name,
                "trigger_signal": triggerSignal,
                "choices_len":    len(choices),
            }).Debug("Function calling normal response: handler activated")
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
            "trigger_signal": triggerSignal,
            "tool_call_count": len(toolCalls),
        }).Debug("Function calling normal response: parsed tool calls for choice")

        msg["tool_calls"] = toolCalls
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
        logrus.WithError(err).Warn("Failed to marshal modified function-calling response, falling back to original body")
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
    logrus.WithFields(fcFields).Debug("Function calling normal response: body before/after (truncated)")

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
func (ps *ProxyServer) handleFunctionCallingStreamingResponse(c *gin.Context, resp *http.Response) {
    // Set standard SSE headers
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")
    c.Header("X-Accel-Buffering", "no")

    flusher, ok := c.Writer.(http.Flusher)
    if !ok {
        logrus.Error("Streaming unsupported by the writer, falling back to normal response")
        ps.handleFunctionCallingNormalResponse(c, resp)
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
                if err == io.EOF && line == "" {
                    // Upstream closed without explicit [DONE], flush last event if any.
                    if len(prevEventLines) > 0 {
                        _ = writeEvent(prevEventLines)
                    }
                    return
                }
                if err != io.EOF {
                    logUpstreamError("reading from upstream", err)
                }
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

        // Extract delta.content from this chunk to build the full assistant text.
        if dataStr != "" {
            var evt map[string]any
            if err := json.Unmarshal([]byte(dataStr), &evt); err == nil {
                if choicesVal, ok := evt["choices"]; ok {
                    if choices, ok := choicesVal.([]any); ok && len(choices) > 0 {
                        if ch, ok := choices[0].(map[string]any); ok {
                            if deltaVal, ok := ch["delta"].(map[string]any); ok {
                                if text, ok := deltaVal["content"].(string); ok && text != "" {
                                    contentBuf.WriteString(text)
                                }
                            }
                        }
                    }
                }
            }
        }

        // Shift previous event: now that we have a new event, the previous one is
        // guaranteed not to be the last, so we can safely forward it.
        if len(prevEventLines) > 0 {
            if err := writeEvent(prevEventLines); err != nil {
                logUpstreamError("writing stream to client", err)
                return
            }
        }
        prevEventLines = append([]string(nil), rawLines...)
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

    // Build tool_calls payload from parsed calls.
    toolCalls := make([]map[string]any, 0, len(parsedCalls))
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
            "id":   "call_" + utils.GenerateRandomSuffix(),
            "type": "function",
            "function": map[string]any{
                "name":      call.Name,
                "arguments": string(argsJSON),
            },
        })
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
        logUpstreamError("marshalling modified streaming function-calling event", err)
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
        "last_event_preview":   truncateString(prevEventData, 512),
        "modified_preview":     previewForLog(out, 512),
    }
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            streamFields["group"] = g.Name
        }
    }
    logrus.WithFields(streamFields).Debug("Function calling streaming response: last event before/after (truncated)")

    // Emit the modified last event followed by [DONE].
    if _, err := c.Writer.Write([]byte("data: " + string(out) + "\n\n")); err != nil {
        logUpstreamError("writing modified streaming event", err)
        return
    }
    flusher.Flush()
    _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
    flusher.Flush()
}

// previewForLog returns a safe, truncated string representation of a byte slice
// for logging purposes. It avoids logging excessively large bodies while still
// providing useful context.
func previewForLog(b []byte, max int) string {
    if b == nil {
        return ""
    }
    if max <= 0 || len(b) <= max {
        return string(b)
    }
    return string(b[:max])
}

// truncateString truncates a string to at most max characters, used for logging
// previews of JSON strings.
func truncateString(s string, max int) string {
    if max <= 0 || len(s) <= max {
        return s
    }
    return s[:max]
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

// parseFunctionCallsXML parses function calls from the assistant content using a
// trigger signal and a simple XML-like convention:
//
// <function_calls>
//   <function_call>
//     <tool>tool_name</tool>
//     <args>
//       <param1>"value"</param1>
//       <param2>123</param2>
//     </args>
//   </function_call>
// </function_calls>
func parseFunctionCallsXML(text, triggerSignal string) []functionCall {
    if text == "" || triggerSignal == "" || !strings.Contains(text, triggerSignal) {
        return nil
    }

    cleaned := removeThinkBlocks(text)
    idx := strings.LastIndex(cleaned, triggerSignal)
    if idx == -1 {
        return nil
    }

    segment := cleaned[idx:]

    // Extract content inside <function_calls>...</function_calls>
    fcRe := regexp.MustCompile(`(?s)<function_calls>(.*?)</function_calls>`)
    fcMatch := fcRe.FindStringSubmatch(segment)
    if len(fcMatch) < 2 {
        return nil
    }
    callsContent := fcMatch[1]

    // Extract each <function_call>...</function_call> block
    callRe := regexp.MustCompile(`(?s)<function_call>(.*?)</function_call>`)
    callMatches := callRe.FindAllStringSubmatch(callsContent, -1)
    if len(callMatches) == 0 {
        return nil
    }

    toolRe := regexp.MustCompile(`(?s)<tool>(.*?)</tool>`)
    toolNameRe := regexp.MustCompile(`(?s)<tool_name>(.*?)</tool_name>`)
    argsBlockRe := regexp.MustCompile(`(?s)<args>(.*?)</args>`)
    paramsBlockRe := regexp.MustCompile(`(?s)<parameters>(.*?)</parameters>`)
    // Match parameter tags with arbitrary names and nested content. We avoid
    // using backreferences because Go's regexp engine does not support them.
    // Instead, we capture both the opening and closing tag names and compare
    // them in code.
    argRe := regexp.MustCompile(`(?s)<([^\s>/]+)>(.*?)</([^\s>/]+)>`)

    results := make([]functionCall, 0, len(callMatches))

    for _, m := range callMatches {
        block := m[1]
        toolMatch := toolRe.FindStringSubmatch(block)
        if len(toolMatch) < 2 {
            // Fallback: support <tool_name>...</tool_name> shape.
            toolMatch = toolNameRe.FindStringSubmatch(block)
        }
        if len(toolMatch) < 2 {
            continue
        }
        name := strings.TrimSpace(toolMatch[1])
        if name == "" {
            continue
        }

        args := make(map[string]any)
        argsBlockMatch := argsBlockRe.FindStringSubmatch(block)
        if len(argsBlockMatch) < 2 {
            // Fallback: support <parameters>...</parameters> shape.
            argsBlockMatch = paramsBlockRe.FindStringSubmatch(block)
        }
        if len(argsBlockMatch) >= 2 {
            argsContent := argsBlockMatch[1]
            argMatches := argRe.FindAllStringSubmatch(argsContent, -1)
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

                // Try to interpret the value as JSON first, then fall back to raw string.
                var anyVal any
                if err := json.Unmarshal([]byte(valStr), &anyVal); err != nil {
                    anyVal = valStr
                }
                args[key] = anyVal
            }
        }

        results = append(results, functionCall{Name: name, Args: args})
    }

    if len(results) == 0 {
        return nil
    }
    return results
}
