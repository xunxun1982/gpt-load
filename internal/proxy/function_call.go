package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
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

// safeGroupName returns the group name for logging, with nil-safe access.
// This prevents panic when group is unexpectedly nil (e.g., tests, misconfiguration).
func safeGroupName(group *models.Group) string {
	if group == nil {
		return "<nil>"
	}
	return group.Name
}

var (
	reFunctionCallsBlock = regexp.MustCompile(`(?s)<function_calls>(.*?)</function_calls>`)
	// NOTE: reFunctionCallBlock does not support attributes (e.g. <function_call id="1">)
	// because our prompt doesn't instruct models to emit attributes. Keeping it simple
	// reduces parsing complexity and avoids false matches with unrelated XML.
	reFunctionCallBlock = regexp.MustCompile(`(?s)<function_call>(.*?)</function_call>`)
	reToolTag            = regexp.MustCompile(`(?s)<(?:tool|tool_name|invocationName)>(.*?)</(?:tool|tool_name|invocationName)>`)
	reNameTag            = regexp.MustCompile(`(?s)<name>(.*?)</name>`)
	reInvocationTag      = regexp.MustCompile(`(?s)<(?:invocation|invoke)(?:\s+name="([^"]+)")?[^>]*>(.*?)</(?:invocation|invoke)>`)
	reArgsBlock          = regexp.MustCompile(`(?s)<args>(.*?)</args>`)
	reParamsBlock        = regexp.MustCompile(`(?s)<parameters>(.*?)</parameters>`)
	reMcpParam           = regexp.MustCompile(`(?s)<(?:parameter|param)\s+name="([^"]+)"[^>]*>(.*?)</(?:parameter|param)>`)
	reGenericParam       = regexp.MustCompile(`(?s)<([^\s>/]+)(?:\s+[^>]*)?>(.*?)</([^\s>/]+)>`)
	reToolCallBlock      = regexp.MustCompile(`(?s)<tool_call\s+name="([^"]+)"[^>]*>(.*?)</tool_call>`)
	reTriggerSignal      = regexp.MustCompile(`<Function_[a-zA-Z0-9]+_Start/>`)

	// Model-specific special tokens that may leak into parameter values.
	// DeepSeek uses fullwidth pipes: <｜User｜>, <｜Assistant｜>, <｜System｜>
	// Some models use ASCII pipes: <|user|>, <|assistant|>, <|im_start|>, <|im_end|>
	reModelSpecialToken = regexp.MustCompile(`<[｜|][^｜|>]+[｜|]>`)

	// Patterns to detect "execution intent" phrases where AI describes an action
	// but doesn't actually call the tool. Used for auto-continuation detection.
	// Covers both Chinese and English expressions.
	reExecutionIntent = regexp.MustCompile(`(?i)(现在让我|让我立即|让我再次|我来|我现在|我将|现在执行|现在运行|接下来我(会|将)?(开始)?(运行|执行)|首先进行(联网)?搜索|先进行(联网)?搜索|首先(联网)?搜索|先(联网)?搜索|运行以下命令|运行下面的命令|执行以下命令|执行下面的命令|在(终端|命令行|shell)中运行|now let me|let me now|i will now|i'll now|let me run|let me execute|let me try|let's run|let's execute|let's try|we can run|you can run|run the (following )?command|run this command|execute the (following )?command|execute this command|open a terminal and run|from your terminal, run|running the|executing the)`)

	// Precompiled patterns for extractParameters fallback parsing.
	// These are compiled once at init to avoid repeated compilation in hot path.
	// See: Go regex best practice - compile once, reuse often.
	reUnclosedTag = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)>(.+)`)
	// NOTE: reHybridJsonXml pattern requires backreference (\1) which Go RE2 doesn't support.
	// The hybrid JSON-XML fallback uses a simpler pattern and verifies tag matching in code.
	reHybridJsonXml = regexp.MustCompile(`(?s)\{"([a-zA-Z0-9_]+)":"(.*?)</([a-zA-Z0-9_]+)>`)

	// Loose invocation pattern for fuzzy matching when standard parsing fails.
	// Matches <invocation>...<name>tool</name>...<parameters>...</parameters>...</invocation>
	// or even just <invocation>...<name>tool</name>... without proper closing.
	reLooseInvocation = regexp.MustCompile(`(?s)<invocation[^>]*>\s*<name>([^<]+)</name>\s*(?:<parameters>([\s\S]*?)</parameters>)?`)
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
        logrus.WithError(err).WithField("group", safeGroupName(group)).
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

    // Check if this is a follow-up request with tool results.
    // Multi-turn function calling flow: client sends user message + tools → model
    // returns assistant message with tool_calls → client executes tools and appends
    // role="tool" messages → client sends the full conversation back.
    //
    // IMPORTANT: Unlike previous implementation that skipped rewrite entirely,
    // we now preprocess these messages to convert tool_calls and tool results
    // into AI-understandable text format. This enables the upstream model to
    // understand the conversation context even without native function calling.
    //
    // Reference: snow-cli useConversation.ts and Toolify preprocess_messages()
    hasToolHistory := hasToolResults(messages)
    hasToolErrors := false
    if hasToolHistory {
        // Preprocess messages: convert tool_calls and tool results to text format
        messages, hasToolErrors = preprocessToolMessagesWithErrorDetection(messages)
        logrus.WithFields(logrus.Fields{
            "message_count":   len(messages),
            "has_tool_errors": hasToolErrors,
        }).Debug("Preprocessed messages with tool history for function call continuation")
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
        // Sort parameter names for deterministic output across runs,
        // which aids debugging and log comparison.
        paramSummary := "None"
        if params != nil {
            if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
                // Collect and sort parameter names for deterministic output
                propNames := make([]string, 0, len(props))
                for pName := range props {
                    propNames = append(propNames, pName)
                }
                sort.Strings(propNames)

                pairs := make([]string, 0, len(props))
                for _, pName := range propNames {
                    pInfo := props[pName]
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

        // Use strings.Builder for efficient string concatenation in loop.
        var block strings.Builder
        fmt.Fprintf(&block, "%d. <tool name=\"%s\">\n", i+1, name)
        block.WriteString("   Description:\n")
        if desc != "" {
            block.WriteString("```\n")
            block.WriteString(desc)
            block.WriteString("\n```\n")
        } else {
            block.WriteString("None\n")
        }
        block.WriteString("   Parameters summary: ")
        block.WriteString(paramSummary)
        toolBlocks = append(toolBlocks, block.String())
    }

    if len(toolBlocks) == 0 {
        return bodyBytes, "", nil
    }

    // Compose final prompt content injected as a new system message.
    // Concise prompt based on extended/interleaved thinking research.
    prompt := "You coordinate tool calls.\n" +
        "- If no tool is needed, answer normally in the user's language.\n" +
        "- If you need to read, write, search, run, inspect, or execute, you MUST call tools instead of only describing actions.\n" +
        "- For multi-step tasks, keep calling tools → observe results → call tools again until the task is really finished. On errors, adjust arguments and retry.\n\n" +
        "To call tools:\n" +
        "1) First output this trigger on its own line:\n" +
        triggerSignal + "\n" +
        "2) Immediately after the trigger, output exactly one <function_calls> XML block:\n" +
        "<function_calls>\n" +
        "  <function_call>\n" +
        "    <invocation>\n" +
        "      <name>tool-name</name>\n" +
        "      <parameters><param1>value1</param1></parameters>\n" +
        "    </invocation>\n" +
        "  </function_call>\n" +
        "</function_calls>\n\n" +
        "IMPORTANT: Output the trigger signal immediately followed by XML. Do NOT explain what you will do before the XML.\n\n" +
        "Tools:\n" + strings.Join(toolBlocks, "\n\n") + "\n"

    // Add stronger continuation reminder for multi-turn conversations.
    // This helps reasoning models (like deepseek-reasoner) that may plan in
    // reasoning_content but fail to output actual XML in content.
    if hasToolHistory {
        continuation := "\n\nCRITICAL CONTINUATION: Previous tool results shown above. "
        if hasToolErrors {
            continuation += "Some failed - fix and retry. "
        }
        continuation += "You MUST output " + triggerSignal + " followed by <function_calls> XML NOW. " +
            "Do NOT summarize or describe - just output the XML block."
        prompt += continuation
    }

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

    // Remove max_tokens only when it's too low to prevent truncation of the XML block.
    // The XML format requires more tokens than standard text, and low limits (e.g. 100)
    // will cause the response to be cut off mid-XML, breaking parsing.
    // We preserve caller's budget control when max_tokens >= 500 (sufficient for XML).
    // This threshold is based on typical function call XML overhead (~200-400 tokens).
    const minTokensForXml = 500
    if maxTokens, ok := req["max_tokens"]; ok {
        shouldRemove := false
        switch v := maxTokens.(type) {
        case float64:
            shouldRemove = v < minTokensForXml
        case int:
            shouldRemove = v < minTokensForXml
        case int64:
            shouldRemove = v < minTokensForXml
        }
        if shouldRemove {
            delete(req, "max_tokens")
            logrus.WithFields(logrus.Fields{
                "group":              safeGroupName(group),
                "original_max_tokens": maxTokens,
            }).Debug("Function call rewrite: removed low max_tokens to prevent XML truncation")
        }
    }

    rewritten, err := json.Marshal(req)
    if err != nil {
        logrus.WithError(err).WithField("group", safeGroupName(group)).
            Warn("Failed to marshal request after function call rewrite")
        return bodyBytes, "", err
    }

    // Debug: log a safe preview of request body before and after rewrite.
    // NOTE: These previews may contain user prompts/PII. They are Debug-level only and
    // are essential for troubleshooting function call behavior in development. Production
    // deployments should set log level to Info or higher, or operators can implement
    // log filtering/sanitization as needed for their compliance requirements.
    logFields := logrus.Fields{
        "group":              safeGroupName(group),
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

    // Read full response body. We bound the workload of XML parsing below by
    // limiting the size of the assistant content string passed into the parser,
    // instead of truncating the response returned to the client.
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
                "group":          safeGroupName(g),
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

        // Bound parsing window to avoid feeding arbitrarily large content into
        // the XML parser. We keep only the tail of the content where the
        // <function_calls> block is expected to appear.
        parseInput := contentStr
        if len(parseInput) > maxContentBufferBytes {
            parseInput = parseInput[len(parseInput)-maxContentBufferBytes:]
        }

        calls := parseFunctionCallsXML(parseInput, triggerSignal)

        // Fallback: If no calls found with trigger signal, try parsing without it.
        if len(calls) == 0 && strings.Contains(parseInput, "<function_calls>") {
            calls = parseFunctionCallsXML(parseInput, "")
            if len(calls) > 0 {
                logrus.WithField("parsed_count", len(calls)).
                    Debug("Function call normal response: parsed calls using fallback (no trigger signal)")
            }
        }

        if len(calls) == 0 {
            // Log when we detect execution intent phrases but no function_calls XML.
            if reExecutionIntent.MatchString(parseInput) && !strings.Contains(parseInput, "<function_calls>") {
                fields := logrus.Fields{
                    "trigger_signal":  triggerSignal,
                    "content_preview": utils.TruncateString(parseInput, 200),
                }
                if fr, ok := chMap["finish_reason"].(string); ok {
                    fields["finish_reason"] = fr
                }
                logrus.WithFields(fields).Debug("Function call normal response: detected execution intent without tool call XML")
            }
            continue
        }

        // Build OpenAI-compatible tool_calls array.
        // Use index suffix to guarantee uniqueness within same response (avoid birthday paradox collision).
        toolCalls := make([]map[string]any, 0, len(calls))
        callIndex := 0
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
                "id":   fmt.Sprintf("call_%s_%d", utils.GenerateRandomSuffix(), callIndex),
                "type": "function",
                "function": map[string]any{
                    "name":      call.Name,
                    "arguments": string(argsJSON),
                },
            })
            callIndex++
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
    // SECURITY NOTE: These debug previews may contain sensitive prompts/responses.
    // Only enable debug level logging in trusted environments. Consider PII redaction
    // if used in production with relaxed log levels.
    fcFields := logrus.Fields{
        "trigger_signal":      triggerSignal,
        "original_body_bytes": len(body),
        "modified_bytes":      len(out),
        "original_preview":    previewForLog(body, 512),
        "modified_preview":    previewForLog(out, 512),
    }
    if gv, ok := c.Get("group"); ok {
        if g, ok := gv.(*models.Group); ok {
            fcFields["group"] = safeGroupName(g)
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
    // reasoningBuf accumulates reasoning_content for detecting tool call intent in thinking.
    var reasoningBuf strings.Builder
    // contentBufFullWarned ensures we log the buffer-limit warning at most once.
    contentBufFullWarned := false

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
        eof := false // Track EOF to break outer loop when no more data
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
                    } else {
                        // No more data in this event and EOF from upstream.
                        // Mark eof to break outer loop after processing any pending event.
                        eof = true
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
            if eof {
                // Clean EOF with no pending event: terminate outer loop to prevent
                // infinite loop / goroutine leak when upstream closes cleanly.
                break
            }
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
                                // Accumulate reasoning_content for intent detection.
                                if reasoning, ok := deltaVal["reasoning_content"].(string); ok && reasoning != "" {
                                    if reasoningBuf.Len()+len(reasoning) <= maxContentBufferBytes {
                                        reasoningBuf.WriteString(reasoning)
                                    }
                                }
                                if text, ok := deltaVal["content"].(string); ok && text != "" {
                                    // Accumulate content for final XML parsing.
                                    if contentBuf.Len()+len(text) <= maxContentBufferBytes {
                                        contentBuf.WriteString(text)
                                    } else if !contentBufFullWarned {
                                        // Log once when buffer limit is first reached to aid debugging.
                                        logrus.Warn("Function call streaming: content buffer limit reached, subsequent content will not be parsed for tool calls")
                                        contentBufFullWarned = true
                                    }

                                    // First, always strip trigger signals from content.
                                    // These should never be visible to clients.
                                    // Also, detecting trigger means XML block follows, enter suppression.
                                    if reTriggerSignal.MatchString(text) {
                                        insideFunctionCalls = true
                                    }
                                    text = reTriggerSignal.ReplaceAllString(text, "")

                                    hasOpen := strings.Contains(text, "<function_calls>")
                                    hasClose := strings.Contains(text, "</function_calls>")
                                    // Detect internal XML tags to track XML block boundaries.
                                    // Include both complete tags and partial patterns for robustness.
                                    hasInternalXml := strings.Contains(text, "<function_call") ||
                                        strings.Contains(text, "</function_call") ||
                                        strings.Contains(text, "<invocation") ||
                                        strings.Contains(text, "</invocation") ||
                                        strings.Contains(text, "<parameters") ||
                                        strings.Contains(text, "</parameters") ||
                                        strings.Contains(text, "<name>") ||
                                        strings.Contains(text, "</name>") ||
                                        strings.Contains(text, "<args") ||
                                        strings.Contains(text, "</args") ||
                                        strings.Contains(text, "<tool>") ||
                                        strings.Contains(text, "</tool>") ||
                                        strings.Contains(text, "<tool_call") ||
                                        strings.Contains(text, "</tool_call") ||
                                        strings.Contains(text, "<todo") ||
                                        strings.Contains(text, "</todo") ||
                                        strings.Contains(text, "<command") ||
                                        strings.Contains(text, "</command") ||
                                        strings.Contains(text, "<filePath") ||
                                        strings.Contains(text, "</filePath")

                                    // Detect partial XML: content containing < followed by valid tag start character.
                                    // This catches character-by-character streaming where tags are split.
                                    // To avoid false positives with comparison operators (e.g. "x < 5"),
                                    // we only trigger if < is followed by a letter or / for closing tags.
                                    hasPartialXmlStart := false
                                    if !insideFunctionCalls && !hasOpen && !hasClose && !hasInternalXml {
                                        if ltIdx := strings.Index(text, "<"); ltIdx >= 0 && !strings.Contains(text, ">") {
                                            remaining := text[ltIdx+1:]
                                            if len(remaining) > 0 {
                                                nextChar := remaining[0]
                                                // XML tag names start with letter or underscore; / for closing tags
                                                if (nextChar >= 'a' && nextChar <= 'z') ||
                                                    (nextChar >= 'A' && nextChar <= 'Z') ||
                                                    nextChar == '/' || nextChar == '_' {
                                                    hasPartialXmlStart = true
                                                }
                                            }
                                        }
                                    }

                                    if hasOpen && hasClose {
                                        // Entire block in one chunk: strip only the XML block, keep prefix and suffix.
                                        startIdx := strings.Index(text, "<function_calls>")
                                        if startIdx >= 0 {
                                            endRel := strings.Index(text[startIdx:], "</function_calls>")
                                            if endRel >= 0 {
                                                endIdx := startIdx + endRel + len("</function_calls>")
                                                prefix := text[:startIdx]
                                                suffix := text[endIdx:]
                                                deltaVal["content"] = strings.TrimSpace(prefix + suffix)
                                            } else {
                                                // Closing tag missing in this chunk, keep only prefix
                                                deltaVal["content"] = strings.TrimSpace(text[:startIdx])
                                            }
                                        }
                                    } else if hasOpen {
                                        // Start of XML block: keep text before tag, suppress rest.
                                        insideFunctionCalls = true
                                        if idx := strings.Index(text, "<function_calls>"); idx >= 0 {
                                            deltaVal["content"] = strings.TrimSpace(text[:idx])
                                        }
                                    } else if hasClose {
                                        // End of XML block: drop the XML portion but keep any trailing text.
                                        insideFunctionCalls = false
                                        if endIdx := strings.Index(text, "</function_calls>"); endIdx >= 0 {
                                            deltaVal["content"] = strings.TrimSpace(text[endIdx+len("</function_calls>"):])
                                        } else {
                                            deltaVal["content"] = ""
                                        }
                                    } else if insideFunctionCalls {
                                        // Inside XML block: suppress all content.
                                        deltaVal["content"] = ""
                                    } else if hasInternalXml {
                                        // Detected internal XML without seeing opening tag - likely missed it.
                                        // Mark as inside and suppress this content.
                                        insideFunctionCalls = true
                                        deltaVal["content"] = ""
                                    } else if hasPartialXmlStart {
                                        // Detected partial XML start (< without >) - enter suppression mode.
                                        // This handles character-by-character streaming.
                                        insideFunctionCalls = true
                                        if idx := strings.Index(text, "<"); idx >= 0 {
                                            deltaVal["content"] = strings.TrimSpace(text[:idx])
                                        } else {
                                            deltaVal["content"] = ""
                                        }
                                    } else {
                                        // Normal text (not inside XML block): update with trigger-stripped version
                                        deltaVal["content"] = text
                                    }
                                }
                            }
                        }
                    }
                }
                // Re-serialize with modified content for forwarding. For intermediate
                // events we rebuild only the data: line and intentionally drop any
                // upstream SSE metadata (id:, event:) for simplicity. Most clients
                // using OpenAI-style streaming rely only on data: lines. The final
                // modified event below preserves upstream metadata by reusing
                // prevEventLines and replacing only data: lines.
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

        if eof {
            // Upstream closed after this event; stop reading further.
            break
        }
    }

    // If we have never seen any event, simply send [DONE] and return.
    if !seenAnyEvent {
        _, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
        flusher.Flush()
        return
    }

    // At this point, prevEventLines holds the last event before [DONE]. Attempt to
    // parse function calls from the accumulated content buffer.
    contentStr := contentBuf.String()
    parsedCalls := parseFunctionCallsXML(contentStr, triggerSignal)

    // Fallback: If no calls found with trigger signal, try parsing without it.
    // This handles cases where AI outputs <function_calls> directly without the trigger.
    if len(parsedCalls) == 0 && strings.Contains(contentStr, "<function_calls>") {
        parsedCalls = parseFunctionCallsXML(contentStr, "")
        if len(parsedCalls) > 0 {
            logrus.WithField("parsed_count", len(parsedCalls)).
                Debug("Function call streaming: parsed calls using fallback (no trigger signal)")
        }
    }

    if len(parsedCalls) == 0 || prevEventData == "" {
        // Log if we detected execution intent but no tool calls (helps with debugging)
        reasoningStr := reasoningBuf.String()
        hasContentIntent := reExecutionIntent.MatchString(contentStr) && !strings.Contains(contentStr, "<function_calls>")
        hasReasoningIntent := detectToolIntentInReasoning(reasoningStr)

        if hasContentIntent || hasReasoningIntent {
            logrus.WithFields(logrus.Fields{
                "trigger_signal":     triggerSignal,
                "content_preview":    utils.TruncateString(contentStr, 200),
                "reasoning_preview":  utils.TruncateString(reasoningStr, 200),
                "content_intent":     hasContentIntent,
                "reasoning_intent":   hasReasoningIntent,
            }).Debug("Function call streaming: detected execution intent without tool call XML")
        }

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
    // Use index suffix in ID to guarantee uniqueness within same response (avoid birthday paradox collision).
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
            "id":    fmt.Sprintf("call_%s_%d", utils.GenerateRandomSuffix(), index),
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
    // SECURITY NOTE: These debug previews may contain sensitive prompts/responses.
    // Only enable debug level logging in trusted environments.
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
            streamFields["group"] = safeGroupName(g)
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
// Optimization: slices the byte array before string conversion to avoid
// allocating large string when only a small preview is needed.
func previewForLog(b []byte, max int) string {
    if b == nil {
        return ""
    }
    // Optimization: avoid full string allocation for large slices
    if max > 0 && len(b) > max {
        b = b[:max]
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
// NOTE: This implementation does NOT handle nested <think> blocks correctly.
// For example: <think>outer <think>inner</think> more</think> would leave
// " more</think>" in the text. However:
// 1. Nested think blocks are extremely rare in actual AI model output
// 2. Regex lazy matching (.*?) cannot solve true nesting either - it would
//    match to the first </think>, same as our current approach
// 3. True nested handling requires a counter-based or recursive approach,
//    adding significant complexity for a marginal edge case
// We keep this simple implementation and document the limitation.
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
// and trigger signals (e.g. <Function_xxxx_Start/>) from the given text.
// This ensures end users only see natural language text, while tool_calls are
// delivered through the structured API response. Both streaming and non-streaming
// responses should use this for content cleanup.
func removeFunctionCallsBlocks(text string) string {
    // First remove function_calls XML blocks
    text = reFunctionCallsBlock.ReplaceAllString(text, "")
    // Then remove trigger signals
    text = reTriggerSignal.ReplaceAllString(text, "")
    // Clean up extra whitespace left by removals (multiple newlines -> single)
    text = strings.TrimSpace(text)
    return text
}

// hasToolResults checks if any message in the array has role="tool" or role="function",
// indicating this is a follow-up request after tool execution in a multi-turn
// function calling conversation. When detected, the middleware will preprocess
// these messages to convert tool_calls and tool results into text format that
// the upstream model can understand, then apply the standard function call rewrite
// with additional continuation hints. This enables models without native function
// calling to continue multi-turn tool conversations.
func hasToolResults(messages []any) bool {
    for _, m := range messages {
        msg, ok := m.(map[string]any)
        if !ok {
            continue
        }
        role, ok := msg["role"].(string)
        if !ok {
            continue
        }
        if role == "tool" || role == "function" {
            return true
        }
    }
    return false
}

// preprocessToolMessagesWithErrorDetection converts tool_calls and tool result
// messages into AI-understandable text format for multi-turn function calling.
// Also returns whether any tool execution errors were detected in the results.
// This helps the prompt builder add stronger continuation hints when errors occurred.
//
// Conversion rules (based on Toolify preprocess_messages() and snow-cli):
// 1. Assistant messages with tool_calls -> text description of the calls
// 2. Tool result messages (role="tool") -> formatted user message with results
func preprocessToolMessagesWithErrorDetection(messages []any) ([]any, bool) {
    if len(messages) == 0 {
        return messages, false
    }

    result := make([]any, 0, len(messages))
    hasErrors := false

    for _, m := range messages {
        msg, ok := m.(map[string]any)
        if !ok {
            result = append(result, m)
            continue
        }

        role, _ := msg["role"].(string)

        // Handle assistant messages with tool_calls
        if role == "assistant" {
            if toolCallsVal, hasToolCalls := msg["tool_calls"]; hasToolCalls {
                toolCalls, ok := toolCallsVal.([]any)
                if ok && len(toolCalls) > 0 {
                    // Convert tool_calls to XML text format
                    content, _ := msg["content"].(string)
                    toolCallsText := formatToolCallsAsText(toolCalls)

                    // Create new assistant message with converted content
                    newMsg := make(map[string]any)
                    for k, v := range msg {
                        if k != "tool_calls" {
                            newMsg[k] = v
                        }
                    }
                    if content != "" {
                        newMsg["content"] = content + "\n" + toolCallsText
                    } else {
                        newMsg["content"] = toolCallsText
                    }
                    result = append(result, newMsg)
                    continue
                }
            }
        }

        // Handle tool result messages
        if role == "tool" || role == "function" {
            toolCallID, _ := msg["tool_call_id"].(string)
            content, _ := msg["content"].(string)
            name, _ := msg["name"].(string)

            // Check for error indicators in tool result
            if containsToolError(content) {
                hasErrors = true
            }

            // Convert tool result to user message format
            formattedContent := formatToolResultAsText(toolCallID, name, content)
            newMsg := map[string]any{
                "role":    "user",
                "content": formattedContent,
            }
            result = append(result, newMsg)
            continue
        }

        // Keep other messages unchanged
        result = append(result, msg)
    }

    return result, hasErrors
}

// containsToolError checks if tool result content indicates an error.
// This uses simple heuristics to detect common error patterns.
func containsToolError(content string) bool {
    if content == "" {
        return false
    }
    lower := strings.ToLower(content)
    // Check for common error indicators
    errorIndicators := []string{
        `"iserror":true`,
        `"iserror": true`,
        `"error":`,
        "error executing",
        "failed to",
        "cannot read",
        "cannot find",
        "not found",
        "permission denied",
        "enoent",
        "timeout",
    }
    for _, indicator := range errorIndicators {
        if strings.Contains(lower, indicator) {
            return true
        }
    }
    return false
}

// detectToolIntentInReasoning checks if reasoning_content contains indicators
// that the model intended to call tools but failed to output actual XML.
// This helps diagnose issues with reasoning models that plan in thinking
// but don't execute in content.
func detectToolIntentInReasoning(reasoning string) bool {
    if reasoning == "" {
        return false
    }
    lower := strings.ToLower(reasoning)
    // Check for tool-related keywords in reasoning
    intentIndicators := []string{
        "function_call",
        "tool_call",
        "<function_calls>",
        "invoke",
        "call the tool",
        "use the tool",
        "execute the",
        "run the command",
        "filesystem-read",
        "filesystem-write",
        "todo-create",
        "bash-exec",
        "web-search",
    }
    for _, indicator := range intentIndicators {
        if strings.Contains(lower, indicator) {
            return true
        }
    }
    return false
}

// formatToolCallsAsText converts tool_calls array into XML text format
// that the AI can understand for context preservation.
// NOTE: We intentionally do NOT escape the arguments/content here because:
// 1. This is only used for context in prompts, not for XML parsing
// 2. Escaping would make the content less readable for AI
// 3. Our parsers look for specific patterns, not general XML structure
func formatToolCallsAsText(toolCalls []any) string {
    if len(toolCalls) == 0 {
        return ""
    }

    var sb strings.Builder
    sb.WriteString("<function_calls>\n")

    for _, tc := range toolCalls {
        callMap, ok := tc.(map[string]any)
        if !ok {
            continue
        }

        funcVal, ok := callMap["function"]
        if !ok {
            continue
        }
        funcMap, ok := funcVal.(map[string]any)
        if !ok {
            continue
        }

        name, _ := funcMap["name"].(string)
        arguments, _ := funcMap["arguments"].(string)

        if name == "" {
            continue
        }

        sb.WriteString("<function_call>\n")
        sb.WriteString("<tool>")
        sb.WriteString(name)
        sb.WriteString("</tool>\n")
        sb.WriteString("<args>")
        sb.WriteString(arguments)
        sb.WriteString("</args>\n")
        sb.WriteString("</function_call>\n")
    }

    sb.WriteString("</function_calls>")
    return sb.String()
}

// formatToolResultAsText converts a tool result message into a formatted
// user message that the AI can understand.
func formatToolResultAsText(toolCallID, name, content string) string {
    var sb strings.Builder
    sb.WriteString("Tool execution result:\n")
    if name != "" {
        sb.WriteString("- Tool name: ")
        sb.WriteString(name)
        sb.WriteString("\n")
    }
    if toolCallID != "" {
        sb.WriteString("- Call ID: ")
        sb.WriteString(toolCallID)
        sb.WriteString("\n")
    }
    sb.WriteString("- Execution result:\n<tool_result>\n")
    sb.WriteString(content)
    sb.WriteString("\n</tool_result>")
    return sb.String()
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
	if text == "" {
		return nil
	}

	cleaned := removeThinkBlocks(text)

	// Prefer to anchor parsing near the trigger signal when the model
	// correctly follows the prompt convention. Fall back to the first
	// <function_calls> block if no trigger is found.
	// NOTE: Use Index (first) instead of LastIndex because AI sometimes outputs
	// multiple function_calls blocks, and the first one is typically correct
	// while subsequent ones may have corrupted parameters.
	start := 0
	if triggerSignal != "" {
		if idx := strings.Index(cleaned, triggerSignal); idx != -1 {
			start = idx
		} else if idx := strings.Index(cleaned, "<function_calls>"); idx != -1 {
			start = idx
		}
	} else {
		if idx := strings.Index(cleaned, "<function_calls>"); idx != -1 {
			start = idx
		}
	}
	segment := cleaned[start:]
	// Remove any orphaned trigger signals from the segment.
	segment = reTriggerSignal.ReplaceAllString(segment, "")

	// Handle double closing tags: </function_calls></function_calls>
	// Find the first </function_calls> and truncate there to avoid parsing issues.
	if openIdx := strings.Index(segment, "<function_calls>"); openIdx != -1 {
		afterOpen := segment[openIdx+len("<function_calls>"):]
		if closeIdx := strings.Index(afterOpen, "</function_calls>"); closeIdx != -1 {
			// Truncate at first closing tag to ignore duplicates
			segment = segment[:openIdx+len("<function_calls>")+closeIdx+len("</function_calls>")]
		}
	}

	// Extract content inside <function_calls>...</function_calls> using shared
	// precompiled patterns.
	fcMatch := reFunctionCallsBlock.FindStringSubmatch(segment)
	var callsContent string
	if len(fcMatch) >= 2 {
		callsContent = fcMatch[1]
	} else {
		// Fuzzy fallback: tolerate missing <function_calls> root as long as the
		// content still contains <function_call>, <invocation>, or <tool_call> blocks.
		// This helps when the model omits the wrapper but emits valid inner structures.
		if strings.Contains(segment, "<function_call>") || strings.Contains(segment, "<tool_call") || strings.Contains(segment, "<invocation") {
			callsContent = segment
		} else {
			return nil
		}
	}

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
				argsContent := invMatch[2]

				// If name attribute is missing, try to find <name> tag inside the content.
				// This handles the case where the model follows the prompt example which uses
				// <name> as a child tag instead of an attribute.
				if name == "" {
					nameMatch := reNameTag.FindStringSubmatch(argsContent)
					if len(nameMatch) >= 2 {
						name = strings.TrimSpace(nameMatch[1])
					}
				}

				if name == "" {
					continue
				}

				// If argsContent contains a nested <parameters> block, extract from there.
				// This handles the format: <invocation><name>...</name><parameters>...</parameters></invocation>
				paramsMatch := reParamsBlock.FindStringSubmatch(argsContent)
				var paramSource string
				if len(paramsMatch) >= 2 {
					paramSource = paramsMatch[1]
				} else {
					paramSource = argsContent
				}

				args := extractParameters(paramSource, reMcpParam, reGenericParam)
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

	// Fuzzy fallback: if standard parsing found nothing but content contains <invocation>,
	// try the loose invocation pattern which is more tolerant of malformed XML.
	if len(results) == 0 && strings.Contains(callsContent, "<invocation") {
		looseMatches := reLooseInvocation.FindAllStringSubmatch(callsContent, -1)
		for _, lm := range looseMatches {
			if len(lm) < 2 {
				continue
			}
			name := strings.TrimSpace(lm[1])
			if name == "" {
				continue
			}
			var paramContent string
			if len(lm) >= 3 {
				paramContent = lm[2]
			}
			args := extractParameters(paramContent, reMcpParam, reGenericParam)
			results = append(results, functionCall{Name: name, Args: args})
			logrus.WithFields(logrus.Fields{
				"tool_name":     name,
				"param_content": utils.TruncateString(paramContent, 200),
				"args_count":    len(args),
			}).Debug("Function call parsing: extracted via fuzzy invocation fallback")
		}
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

    // Attempt to parse the entire content as JSON first.
    // This handles cases where <args> contains JSON instead of XML tags.
    // IMPORTANT: Store trimmed result and check length to avoid panic on whitespace-only content.
    // See: Go best practice - always check string length before indexing.
    trimmed := strings.TrimSpace(content)
    if trimmed == "" {
        return args
    }
    // Try JSON parsing for object, array, or primitive values.
    // Objects are returned directly; arrays/primitives are wrapped under "value" key
    // so callers always receive a map structure.
    // DESIGN DECISION: We wrap non-object JSON in {"value": ...} to maintain consistent
    // return type (map[string]any). This is intentional because:
    // 1. Tool arguments are typically JSON objects; arrays/primitives are rare edge cases
    // 2. Downstream code (json.Marshal for arguments field) handles both shapes correctly
    // 3. Maintaining type consistency simplifies caller logic
    firstChar := trimmed[0]
    if firstChar == '{' || firstChar == '[' || firstChar == '"' ||
        (firstChar >= '0' && firstChar <= '9') || firstChar == '-' ||
        trimmed == "true" || trimmed == "false" || trimmed == "null" {
        var jsonVal any
        if err := json.Unmarshal([]byte(trimmed), &jsonVal); err == nil {
            // If it's already a map, return directly
            if mapVal, ok := jsonVal.(map[string]any); ok {
                return mapVal
            }
            // For arrays, primitives (string, number, bool, null), wrap under "value" key
            // so the caller still gets a map structure with the parsed content.
            return map[string]any{"value": jsonVal}
        }
        // If JSON parsing fails (e.g. due to unescaped characters), fall back to regex parsing.
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
    // Reserved tags that should not be treated as parameters.
    reservedTags := map[string]bool{
        "name": true, "parameters": true, "invocation": true,
        "invoke": true, "tool": true, "args": true,
    }
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
        // Skip reserved tags that are part of the XML structure.
        if reservedTags[strings.ToLower(openTag)] {
            continue
        }
        key := openTag
        valStr := strings.TrimSpace(am[2])
        if key == "" {
            continue
        }
        args[key] = parseValueOrString(valStr)
    }

    // Fallback for unclosed tags: <tag>value (without </tag>).
    // This handles malformed AI output like <todos>["a","b","c"]</parameters>
    // Uses precompiled reUnclosedTag for performance (avoid hot path compilation).
    if len(args) == 0 && trimmed != "" {
        if m := reUnclosedTag.FindStringSubmatch(content); len(m) >= 3 {
            tagName := strings.TrimSpace(m[1])
            if !reservedTags[strings.ToLower(tagName)] {
                // Extract content up to the next </ or end of string
                valueContent := m[2]
                if idx := strings.Index(valueContent, "</"); idx != -1 {
                    valueContent = valueContent[:idx]
                }
                args[tagName] = parseValueOrString(strings.TrimSpace(valueContent))
            }
        }
    }

    // Fallback for hybrid JSON-XML hallucination: {"key":"value...</key>
    // This happens when AI starts writing JSON but switches to XML mid-stream.
    // Example: {"content":"...code...</content>
    // Uses precompiled reHybridJsonXml for performance (avoid hot path compilation).
    // Since Go RE2 doesn't support backreferences, we capture both tag names and verify match.
    if len(args) == 0 && strings.Contains(content, `{"`) {
        matches := reHybridJsonXml.FindAllStringSubmatch(content, -1)
        for _, m := range matches {
            if len(m) < 4 {
                continue
            }
            openTag := m[1]
            val := m[2]
            closeTag := m[3]
            // Verify opening and closing tag names match (manual backreference check)
            if openTag != closeTag {
                continue
            }
            // If the value ends with ", strip it (JSON closing quote)
            val = strings.TrimSuffix(val, `"`)
            args[openTag] = parseValueOrString(val)
        }
    }

    return args
}

// parseValueOrString attempts to parse the input string as JSON; if that fails,
// it returns the string as-is. Before returning, it sanitizes the value to remove
// model-specific special tokens that may have leaked into the output.
func parseValueOrString(s string) any {
    // Sanitize special tokens first
    s = sanitizeModelTokens(s)

    var val any
    if err := json.Unmarshal([]byte(s), &val); err != nil {
        return s
    }
    // Recursively sanitize string values within parsed JSON
    return sanitizeJsonValue(val)
}

// sanitizeModelTokens removes model-specific special tokens from a string.
// These tokens (e.g., DeepSeek's <｜User｜>, <｜Assistant｜>) indicate
// token boundary leakage and should never appear in parameter values.
func sanitizeModelTokens(s string) string {
    if s == "" {
        return s
    }
    // Only remove model special tokens, nothing else
    return reModelSpecialToken.ReplaceAllString(s, "")
}

// sanitizeJsonValue recursively sanitizes string values within a parsed JSON structure.
func sanitizeJsonValue(v any) any {
    switch val := v.(type) {
    case string:
        return sanitizeModelTokens(val)
    case map[string]any:
        for k, v := range val {
            val[k] = sanitizeJsonValue(v)
        }
        return val
    case []any:
        for i, v := range val {
            val[i] = sanitizeJsonValue(v)
        }
        return val
    default:
        return v
    }
}
