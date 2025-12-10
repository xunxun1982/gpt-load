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
	// XML block patterns - use (?s) only when content may span multiple lines
	// Performance: Precompiled at init time, reused for all requests
	reFunctionCallsBlock = regexp.MustCompile(`(?s)<function_calls>(.*?)</function_calls>`)
	reFunctionCallBlock  = regexp.MustCompile(`(?s)<function_call>(.*?)</function_call>`)
	reToolTag            = regexp.MustCompile(`(?s)<(?:tool|tool_name|invocationName)>(.*?)</(?:tool|tool_name|invocationName)>`)
	reNameTag            = regexp.MustCompile(`<name>([^<]*)</name>`) // Optimized: single line, use [^<]* instead of .*?
	reInvocationTag      = regexp.MustCompile(`(?s)<(?:invocation|invoke)(?:\s+name="([^"]+)")?[^>]*>(.*?)</(?:invocation|invoke)>`)
	reArgsBlock          = regexp.MustCompile(`(?s)<args>(.*?)</args>`)
	reParamsBlock        = regexp.MustCompile(`(?s)<parameters>(.*?)</parameters>`)
	reMcpParam           = regexp.MustCompile(`(?s)<(?:parameter|param)\s+name="([^"]+)"[^>]*>(.*?)</(?:parameter|param)>`)
	reGenericParam       = regexp.MustCompile(`(?s)<([^\s>/]+)(?:\s+[^>]*)?>(.*?)</([^\s>/]+)>`) // Note: Go RE2 doesn't support backreferences
	reToolCallBlock      = regexp.MustCompile(`(?s)<tool_call\s+name="([^"]+)"[^>]*>(.*?)</tool_call>`)
	reInvokeFlat         = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"[^>]*>(.*?)</invoke>`)
	// Trigger signal pattern - no (?s) needed, single line match
	// Optimized: Use character class [a-zA-Z0-9] instead of \w for explicit ASCII matching
	reTriggerSignal = regexp.MustCompile(`<Function_[a-zA-Z0-9]+_Start/>|<<CALL_[a-zA-Z0-9]{4,16}>>`)

	// Pattern to match malformed parameter/invocation tags WITH closing tags
	// Examples:
	//   "<><parameter name=\"test\">value</parameter>"
	//   "<><parameter name=\"a\">1</parameter><parameter name=\"b\">2</parameter>"
	// Strategy: Match from <> prefix through the first closing tag, plus any
	// immediately chained <parameter>/<param> tags on the same line. This handles
	// real-world production log-style chains where a malformed <> prefix is followed by multiple
	// parameters that should all be removed as one malformed fragment.
	// RE2 compatible: Uses simple groups and repetition, no backreferences.
	// Performance: O(n) over the line via character classes.
	reMalformedParamTagClosed = regexp.MustCompile(`<>[\s.]*<(?:parameter|param|invoke)(?:name)?[^>]*>[^<]*?</(?:parameter|param|invoke)>(?:<(?:parameter|param)\s+name="[^"]+"[^>]*>[^<]*?</(?:parameter|param)>)*`)

	// Pattern to match malformed invokename with JSON array value (no closing tag)
	// Examples: "<><invokename=\"TodoWrite\">[{\"id\":\"1\"...}]"
	// This handles cases where the model outputs malformed JSON directly after invokename
	// Strategy: Match <><invokename="...">[ or { followed by rest of line
	// Performance: O(n) character class negation
	reMalformedInvokeJSON = regexp.MustCompile(`<>[\s.]*<invokename="[^"]*"[^>]*>[\s]*[\[{][^\r\n]*`)

	// Pattern to match malformed parameter/invocation tags WITHOUT closing tags
	// Examples: "<><parametername=...", "<><parameter name=...>value_on_same_line"
	// Strategy: Match the tag and rest of current line (only when no closing tag present)
	// NOTE: Multi-line content is handled by iterative application
	// RE2 compatible: No lookahead, simple line-end match
	// Performance: O(n) greedy match to line end
	// NOTE: Changed from [^\s]* to [^\r\n]* to match entire parameter value including spaces
	reMalformedParamTag = regexp.MustCompile(`<>[\s.]*<(?:parameter|param|invoke)(?:\s+name)?[^>]*>[^\r\n]*`)

	// Pattern to match malformed invokename/parametername tags without proper spacing (NO <> prefix)
	// Examples: "<invokename=...", "<parametername=..."
	// Strategy: Match the tag and rest of current line (entire line content after tag)
	// Pattern explanation:
	//   - [ \t]* : leading spaces/tabs (optional, can be in middle of line)
	//   - <(?:invoke|parameter)name : malformed tag (no space before 'name')
	//   - [^>]*> : attributes until closing >
	//   - [^\r\n]* : rest of line (including spaces and all content)
	// Performance: O(n) character class negation
	// NOTE: This matches the entire content after the malformed tag to end of line
	reMalformedMergedTag = regexp.MustCompile(`[ \t]*<(?:invoke|parameter)name[^>]*>[^\r\n]*`)

	// Pattern to match CC preamble text that describes tool usage plans
	// Examples: "### 调研结果分析", "**实施方案构思**", "实施步骤", "技术方案"
	// These are meta-commentary about what the AI is going to do, not actual content
	// Strategy: Match lines that start with markdown headers or bold text containing plan keywords
	// Performance: O(n) character class matching
	// ENHANCED: Added more patterns based on real production logs, including English patterns
	reCCPlanHeader = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]*|[*_]{2})?(?:调研结果|实施方案|实施步骤|技术方案|任务清单|选择建议|轻量级|框架对比|实施计划|Implementation\s*Plan|Task\s*List|Thought\s*in\s*Chinese|技术选型|代码亮点|任务完成情况|文件位置|运行方式)[^*\r\n]*(?:[*_]{2})?[ \t]*$`)

	// Pattern to match CC citation markers like [citation:1], [citation:5]
	// These are internal references that should not be shown to users
	reCCCitation = regexp.MustCompile(`\[citation:\d+\]`)

	// Pattern to match standalone <> followed by malformed XML content with JSON value
	// Examples: "<><parametername=\"todos\">[{...}]", "<><invokename=\"TodoWrite\">[\":分析..."
	// Strategy: Match <> followed by malformed tags and JSON content (starting with [ or { or ") to end of line
	// ENHANCED: Also matches malformed JSON starting with " (string) or : (field separator)
	// Performance: O(n) character class negation
	reMalformedEmptyTagPrefixJSON = regexp.MustCompile(`[ \t]*<>[\s.]*(?:<[a-zA-Z]+name[^>]*>)+[\s]*[\[{":\d][^\r\n]*`)

	// Pattern to match standalone <> followed by malformed XML content with chained tags
	// Examples: "<><invokename=\"tool\">value<parametername=\"param\">value2"
	// Strategy: Match <> followed by tag, then any content containing another <tagname, then rest of line
	// This handles chained malformed tags where multiple tags appear on the same line
	// NOTE: Changed from [^\s]* to [^\r\n]* to match entire parameter value including spaces
	// Performance: O(n) character class negation
	reMalformedEmptyTagPrefixChained = regexp.MustCompile(`[ \t]*<>[\s.]*<[a-zA-Z]+name[^>]*>[^<]*<[a-zA-Z]+name[^>]*>[^\r\n]*`)

	// Pattern to match standalone <> followed by malformed XML content with non-JSON value
	// Examples: "<><invokename=\"Glob\"><parametername=\"pattern\">*", "<><parametername=\"query\">exa工具 PythonGUI2025"
	// Strategy: Match <> followed by malformed tags and rest of line (parameter values may contain spaces)
	// NOTE: Changed from [^\s]* to [^\r\n]* to match entire parameter value including spaces
	// Performance: O(n) character class negation
	reMalformedEmptyTagPrefix = regexp.MustCompile(`[ \t]*<>[\s.]*(?:<[a-zA-Z]+name[^>]*>)+[^\r\n]*`)

	// Pattern to match <> directly followed by JSON array/object (NO XML tags)
	// Examples: "<>[{\"id\":\"1\",\"content\":\"...\"}]", "<>{\"key\":\"value\"}"
	// Strategy: Match <> + rest of current line (multi-line content handled by iteration)
	// Pattern explanation:
	//   - [ \t]* : leading spaces/tabs
	//   - <> : the malformed prefix
	//   - [ \t.]* : optional spaces/tabs/dots
	//   - [^\r\n]+ : content until end of line (must have content)
	// Performance: O(n) greedy matching with character class
	// NOTE: For multi-line JSON, multiple applications will clean subsequent lines
	reBareJSONAfterEmpty = regexp.MustCompile(`[ \t]*<>[ \t.]*[^\r\n]+`)

	// Pattern to match standalone <> on a line (with only trailing whitespace)
	// Examples: "  <>", "<> ", "● <>"
	// This cleans up empty malformed tags that don't have JSON on the same line
	// Pattern explanation:
	//   - ^[ \t]* : start of line with optional leading spaces/tabs
	//   - (?:●|•|‣)? : optional bullet point (preserve the line structure)
	//   - [ \t]* : spaces/tabs after bullet
	//   - <> : the malformed empty tag
	//   - [ \t]* : trailing spaces/tabs
	//   - $ : end of line
	// Result: Removes the <> but preserves bullets (they'll be cleaned by trailing space logic)
	reStandaloneEmpty = regexp.MustCompile(`(?m)^([ \t]*(?:●|•|‣)?[ \t]*)<>[ \t]*$`)

	// Pattern to match orphaned JSON arrays/objects/fragments on indented lines (after <> was removed)
	// Examples: "  [{\"id\":1,\"content\":\"...\"}]", "    {\"key\":\"value\"}"
	//           "  \"status\":\"pending\"}, {\"id\":2, \"content\":\"..." (JSON fragments)
	//           "  和简短示例\",\"status\":\"pending\"..." (mid-string JSON fragments)
	// This handles cross-line cases where <> is on one line and JSON on subsequent lines
	// Pattern explanation:
	//   - ^[ \t]+ : start of line with required leading spaces/tabs (indented lines)
	//   - (?:... : one of several JSON-like patterns:
	//     - ["\[\{] : starts with quote/bracket/brace
	//     - "[^"]*",|"[^"]*": : JSON field patterns like "field": or "value",
	//     - [}\]],? : closes with brace/bracket with optional comma
	//     - [^"\s]+",|[^"\s]+": : word ending then quote-comma or quote-colon (stringend)
	//   - .* : rest of line
	// CRITICAL: Distinguishes JSON from normal text by requiring JSON-specific patterns
	// Preserves normal indented text (like "  World".) that has quotes but isn't JSON
	reOrphanedJSON = regexp.MustCompile(`(?m)^[ \t]+(?:["[\{]|"[^"]*"[,:]|[}\]],?|[^"\s]+",|[^"\s]+":).*$`)

	// Pattern to parse malformed invokename tags for tool call extraction
	// Examples: "<><invokename=\"TodoWrite\"><parametername=\"todos\">[...]"
	// Captures: group 1 = tool name, group 2 = remaining content with parameters
	// Optimized: Uses [^"]+ instead of [^"]* for name to ensure non-empty match
	reMalformedInvoke = regexp.MustCompile(`(?:<>[\s.]*)?<invokename="([^"]+)"[^>]*>(.*)`)

	// Pattern to parse malformed parametername tags
	// Examples: "<parametername=\"todos\">[{\"id\":\"1\"}]"
	// Also handles cases without closing tag: "<parametername=\"todos\">[...]"
	// Captures: group 1 = parameter name, group 2 = value (including JSON arrays, file paths)
	// RE2 compatible: Allows closing tags in value, stops at bullets, newlines or opening tags
	// Optimized: More specific character classes for better performance
	// NOTE: This pattern matches until bullet, newline, or opening tag, allowing for values without closing tags
	// FIXED: Use [^\r\n]* to match entire line content including nested JSON brackets
	reMalformedParam = regexp.MustCompile(`<parametername="([^"]+)"[^>]*>([^\r\n]*)`)

	// Pattern to match incomplete/unclosed invoke or parameter tags at end of content
	// Examples: "<invoke name=\"Read\">F:/path/file.py", "<parameter name=\"todos\">[{...}]"
	// These occur when models output partial XML without closing tags
	// NOTE: This pattern is applied only when no closing tag exists (checked in removeFunctionCallsBlocks)
	// Matches content until newline or end of string
	reUnclosedInvokeParam = regexp.MustCompile(`<(?:invoke|parameter)\s+name="[^"]*">[^\n]*(?:\n|$)`)

	// ANTML (Anthropic Markup Language) patterns used by Claude Code and Kiro
	// Format: <function_calls><invoke name="..."><parameter name="...">...
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
	// Optimized: More specific character classes for better performance
	reUnclosedTag = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)>(.+)`)
	// NOTE: reHybridJsonXml pattern requires backreference (\1) which Go RE2 doesn't support.
	// The hybrid JSON-XML fallback uses a simpler pattern and verifies tag matching in code.
	// Optimized: Use atomic groups where possible
	reHybridJsonXml = regexp.MustCompile(`(?s)\{"([a-zA-Z0-9_]+)":"(.*?)</([a-zA-Z0-9_]+)>`)

	// Loose invocation pattern for fuzzy matching when standard parsing fails.
	// Matches <invocation>...<name>tool</name>...<parameters>...</parameters>...</invocation>
	// or even just <invocation>...<name>tool</name>... without proper closing.
	// Optimized: Use non-capturing groups and specific character classes
	reLooseInvocation = regexp.MustCompile(`(?s)<invocation[^>]*>\s*<name>([^<]+)</name>\s*(?:<parameters>([\s\S]*?)</parameters>)?`)

	// Pattern to match Claude Code preamble/explanatory text before function calls
	// CRITICAL: Only removes meta-commentary about retrying/fixing, NOT normal work descriptions
	// Examples of what to remove:
	//   - "根据用户的要求，我需要..." - repeating user request
	//   - "我需要修正TodoWrite的参数格式" - explaining error correction
	//   - "让我重新创建任务清单" - retry announcement
	// Examples of what to KEEP:
	//   - "我来帮你创建一个GUI程序" - normal work description
	//   - "我需要先建立一个计划" - work step description
	// Pattern matches ONLY the bare meta phrase "根据用户的要求" without bullets.
	// This keeps almost all natural language (including English "I need to fix" / "Let me retry")
	// and bullet lines as normal content, and only strips the most boilerplate Chinese prefix.
	// NOTE: These patterns are used for validation in tests only, not for actual removal.
	// The patterns are intentionally set to never match to preserve all natural language text.
	reCCPreambleIndicator = regexp.MustCompile(`^\x00NEVER_MATCH\x00$`)
	// Tool description: still only triggers when explicit tool names like TodoWrite / tool / function appear.
	// NOTE: Intentionally set to never match to preserve all natural language text.
	reCCToolDescIndicator = regexp.MustCompile(`^\x00NEVER_MATCH\x00$`)
	// Pattern to detect JSON structure indicators from Claude Code tool outputs
	// Matches common field names used in TodoWrite, task lists, and other tool outputs
	// ENHANCED: Added more field names to catch leaked JSON from various tools
	// Derived from real production log analysis: file_path, pattern, command, relative_path, recursive, query, tokensNum
	reCCJSONStructIndicator = regexp.MustCompile(`(?:["'](?:id|content|status|state|priority|activeForm|todos|Form|task|description|pending|completed|in_progress|file_path|pattern|command|relative_path|recursive|query|tokensNum|name|type|value)["']\s*:|\{\s*["'](?:id|content|status|task|name))`)

	xmlEscaper = strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)

	// Precompiled pattern for compressing consecutive blank lines.
	reConsecutiveNewlines = regexp.MustCompile(`\n\n+`)

	// Precompiled patterns for repairMalformedJSON function.
	// These are compiled once at init to avoid repeated compilation in hot path.
	// Performance: Precompilation reduces regex overhead by ~10x per call.
	reJSONMissingComma   = regexp.MustCompile(`\}[ \t\n]*\{`)
	reJSONExtraQuote     = regexp.MustCompile(`\\",`)
	reJSONTrailingComma  = regexp.MustCompile(`,\s*[\]\}]`)
	reJSONMalformedTodo  = regexp.MustCompile(`\{"id":\s*"(\d+)",\s*:\s*`)
	reJSONMissingQuotes  = regexp.MustCompile(`:\s*([a-zA-Z][a-zA-Z0-9_]*)([,}\]])`)
	// Pattern to fix malformed field patterns from real-world production log
	// Matches: {"id": "1",": " (id field followed by malformed field separator)
	// This handles cases where the model outputs: {"id": "1",": "content value"
	// and converts it to: {"id": "1", "content": "content value"
	reJSONMalformedField = regexp.MustCompile(`"id":\s*"(\d+)",": "`)
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

	// Extract messages array. Skip rewrite if messages is missing or malformed,
	// as this indicates a non-chat request that shouldn't be rewritten.
	msgsVal, hasMessages := req["messages"]
	if !hasMessages {
		return bodyBytes, "", nil
	}
	messages, ok := msgsVal.([]any)
	if !ok {
		// Unexpected messages structure - skip rewrite to avoid breaking request
		return bodyBytes, "", nil
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
	triggerSignal := utils.GenerateTriggerSignal()

	toolDefs := collectFunctionToolDefs(toolsSlice)
	if len(toolDefs) == 0 {
		return bodyBytes, "", nil
	}

	toolsXml := buildToolsXml(toolDefs)
	toolSummaries := buildToolSummaries(toolDefs)

	// Compose final prompt content injected as a new system message.
	// Only a strict <invoke>/<parameter name="..."> format is allowed to reduce
	// malformed XML outputs in CC + force_function_call mode.
	prompt := fmt.Sprintf(
		"You coordinate tool calls.\n"+
			"- If no tool is needed, answer normally in the user's language.\n"+
			"- If you need to read, write, search, run, inspect, or execute, you MUST call tools instead of only describing actions.\n"+
			"- Call only ONE tool at a time and wait for the result before calling another.\n"+
			"- Always place the trigger signal on its own line, immediately followed by XML.\n\n"+
			"Trigger signal (STRICT):\n%s\n\n"+
			"Tool call XML format (STRICT, only this format is allowed):\n"+
			"<invoke name=\"tool_name\">\n"+
			"<parameter name=\"param1\">value1</parameter>\n"+
			"</invoke>\n\n"+
			"XML rules:\n"+
			"- Use <invoke> and <parameter name=\"...\"> tags only.\n"+
			"- Do NOT use tags like <function_calls>, <invocation>, <invokename>, <parametername>.\n"+
			"- Do NOT prefix XML with \"<>\" or any other markers.\n"+
			"- Encode arrays and objects as JSON inside <parameter> values.\n"+
			"- Do NOT add any extra text before or after the XML block.\n\n"+
			"Available tools (structured):\n%s\n\n"+
			"Quick reference:\n%s\n",
		triggerSignal,
		toolsXml,
		strings.Join(toolSummaries, "\n\n"),
	)

	// Add stronger continuation reminder for multi-turn conversations.
	// This helps reasoning models (like deepseek-reasoner) that may plan in
	// reasoning_content but fail to output actual XML in content.
	if hasToolHistory {
		continuation := "\n\nCRITICAL CONTINUATION: Previous tool results shown above. "
		if hasToolErrors {
			continuation += "Some failed - fix and retry. "
		}
		continuation += "You MUST output " + triggerSignal + " followed by <invoke> or <function_calls> XML NOW. " +
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
				"group":               safeGroupName(group),
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

	// Debug: log minimal info about request rewrite.
	// Reduced logging to avoid excessive output in production
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logFields := logrus.Fields{
			"group":               safeGroupName(group),
			"trigger_signal":      triggerSignal,
			"tools_count":         len(toolsSlice),
			"original_body_bytes": len(bodyBytes),
			"rewritten_bytes":     len(rewritten),
		}
		logrus.WithFields(logFields).Debug("Function call request rewrite: body modified")
	}

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
			// If we see a <function_calls> block but could not parse any valid calls,
			// treat it as invalid tool XML and strip it from the visible content. This
			// prevents downstream clients (including Claude Code) from seeing malformed
			// <function_calls> markers without corresponding structured tool_calls.
			if strings.Contains(parseInput, "<function_calls>") {
				cleaned := removeFunctionCallsBlocks(contentStr)
				if cleaned != contentStr {
					msg["content"] = cleaned
					chMap["message"] = msg
					choices[i] = chMap
					modified = true // Mark response as modified to write cleaned content back to client
					logrus.WithFields(logrus.Fields{
						"trigger_signal":  triggerSignal,
						"content_preview": utils.TruncateString(parseInput, 200),
					}).Debug("Function call normal response: removed invalid <function_calls> block with no parsed tool calls")
				}
			}

			// Log when we detect execution intent phrases but no function_calls XML at all.
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

	// Debug: log minimal info for non-streaming response when modification succeeded.
	// Reduced logging to avoid excessive output in production
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		fcFields := logrus.Fields{
			"trigger_signal":      triggerSignal,
			"original_body_bytes": len(body),
			"modified_bytes":      len(out),
		}
		if gv, ok := c.Get("group"); ok {
			if g, ok := gv.(*models.Group); ok {
				fcFields["group"] = safeGroupName(g)
			}
		}
		logrus.WithFields(fcFields).Debug("Function call normal response: body modified")
	}

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

									// CRITICAL: Detect malformed XML tags that leak to CC clients.
									// These are output by models when forced function calling is enabled.
									// Examples: "<>", "<><invokename=", "<parametername="
									// Detection: Check for specific malformed patterns that indicate tool call fragments.
									hasMalformedXml := strings.Contains(text, "<>") ||
										strings.Contains(text, "<invokename") ||
										strings.Contains(text, "<parametername")

									// If malformed XML is detected, use full removeFunctionCallsBlocks cleanup.
									// This ensures malformed fragments are never sent to the client.
									if hasMalformedXml {
										// Apply full cleanup to remove malformed tags and their content
										cleaned := removeFunctionCallsBlocks(text)
										if cleaned != text {
											logrus.WithFields(logrus.Fields{
												"original_length": len(text),
												"cleaned_length":  len(cleaned),
												"original_preview": utils.TruncateString(text, 100),
												"cleaned_preview":  utils.TruncateString(cleaned, 100),
											}).Debug("Function call streaming: cleaned malformed XML fragments from content")
										}
										text = cleaned
										// Mark as inside function calls to suppress subsequent related content
										insideFunctionCalls = true
									}

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
				"trigger_signal":    triggerSignal,
				"content_preview":   utils.TruncateString(contentStr, 200),
				"reasoning_preview": utils.TruncateString(reasoningStr, 200),
				"content_intent":    hasContentIntent,
				"reasoning_intent":  hasReasoningIntent,
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

	// Debug: log minimal info for streaming last event when modification succeeded.
	// Reduced logging to avoid excessive output in production
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		streamFields := logrus.Fields{
			"trigger_signal":    triggerSignal,
			"parsed_call_count": len(parsedCalls),
			"original_bytes":    len(prevEventData),
			"modified_bytes":    len(out),
		}
		if gv, ok := c.Get("group"); ok {
			if g, ok := gv.(*models.Group); ok {
				streamFields["group"] = safeGroupName(g)
			}
		}
		logrus.WithFields(streamFields).Debug("Function call streaming response: last event modified")
	}

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



// removeThinkBlocks removes all <think>...</think> and <thinking>...</thinking>
// blocks from input text, but FIRST extracts any <function_calls> blocks inside
// them. This is critical for reasoning models (DeepSeek-R1, etc.) that may
// embed tool calls within their thinking process.
// Returns the cleaned text with function_calls preserved at the end.
func removeThinkBlocks(text string) string {
	var extractedCalls strings.Builder

	// Process both <think> and <thinking> tags
	for _, tag := range []string{"<think>", "<thinking>"} {
		closeTag := strings.Replace(tag, "<", "</", 1)
		for {
			start := strings.Index(text, tag)
			if start == -1 {
				break
			}
			end := strings.Index(text[start:], closeTag)
			if end == -1 {
				break
			}
			// Extract the content inside the think block
			thinkContent := text[start+len(tag) : start+end]

			// Look for function_calls blocks inside thinking content
			fcStart := strings.Index(thinkContent, "<function_calls>")
			if fcStart >= 0 {
				fcEnd := strings.LastIndex(thinkContent, "</function_calls>")
				if fcEnd > fcStart {
					// Extract the entire function_calls block
					fcBlock := thinkContent[fcStart : fcEnd+len("</function_calls>")]
					extractedCalls.WriteString("\n")
					extractedCalls.WriteString(fcBlock)
				}
			}

			// Remove the think block (including its content)
			end += start + len(closeTag)
			text = text[:start] + text[end:]
		}
	}

	// Append extracted function_calls to the end of text
	if extractedCalls.Len() > 0 {
		text = strings.TrimSpace(text) + extractedCalls.String()
	}
	return text
}

// removeFunctionCallsBlocks removes all function call XML blocks and trigger signals
// from the given text. This ensures end users only see natural language text, while
// tool_calls are delivered through the structured API response.
// Both streaming and non-streaming responses should use this for content cleanup.
//
// Cleaned formats include:
//   - <function_calls>...</function_calls>
//   - <function_call>...</function_call>
//   - <invoke name="...">...</invoke>
//   - <invocation>...</invocation>
//   - <tool_call name="...">...
//   - Trigger signals (e.g. <Function_xxxx_Start/>, <<CALL_xxxx>>)
//   - Malformed parameter tags (e.g. <><parametername=...)
//   - Malformed JSON arrays and objects from TodoWrite calls
//
// Performance: Uses strings.Contains for fast pre-checks before regex operations.
// Benchmark shows ~8x speedup for strings without XML markers.

// removeUnclosedTagLines removes lines containing unclosed <invoke> or <parameter> tags.
// A tag is considered unclosed if the line contains an opening tag but no corresponding
// closing tag on the same line. This preserves valid closed tags while removing partial ones.
func removeUnclosedTagLines(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))

	for _, line := range lines {
		// Check if line has unclosed invoke tag
		hasInvokeOpen := strings.Contains(line, "<invoke ") || strings.Contains(line, "<invoke>")
		hasInvokeClose := strings.Contains(line, "</invoke>")
		// Check if line has unclosed parameter tag
		hasParamOpen := strings.Contains(line, "<parameter ")
		hasParamClose := strings.Contains(line, "</parameter>")

		// If line has opening tag without closing tag, remove the tag portion
		if (hasInvokeOpen && !hasInvokeClose) || (hasParamOpen && !hasParamClose) {
			// Remove the unclosed tag and everything after it on this line
			cleaned := reUnclosedInvokeParam.ReplaceAllString(line, "")
			cleaned = strings.TrimSpace(cleaned)
			if cleaned != "" {
				result = append(result, cleaned)
			}
		} else {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

func removeFunctionCallsBlocks(text string) string {
	// Fast path: if no XML-like content and no orphaned JSON fragments, return early.
	// strings.Contains is ~8x faster than regex for simple checks; reOrphanedJSON
	// is only used here to catch bare JSON lines that may appear in separate
	// chunks after "<>" prefix was removed earlier in the stream.
	hasOrphanJson := reOrphanedJSON.MatchString(text)
	if !strings.Contains(text, "<") && !hasOrphanJson {
		// Even without XML tags, check for Claude Code preambles and remove them
		text = removeClaudeCodePreamble(text)
		return strings.TrimSpace(text)
	}

	// Check for function call markers using fast string operations
	hasFunctionCalls := strings.Contains(text, "<function_calls>")
	hasFunctionCall := strings.Contains(text, "<function_call>")
	hasInvoke := strings.Contains(text, "<invoke")
	hasInvocation := strings.Contains(text, "<invocation")
	hasToolCall := strings.Contains(text, "<tool_call")
	hasTrigger := strings.Contains(text, "<Function_") || strings.Contains(text, "<<CALL_")
	// hasMalformed also covers orphaned JSON fragments that may appear on
	// separate lines/chunks after the leading "<>" or malformed tags were
	// removed earlier in the stream. This is critical for CC streaming where
	// models often output "<>\n  [{...}]" and the JSON lines are delivered in
	// separate chunks without any remaining "<>" prefix.
	hasMalformed := strings.Contains(text, "<>") || strings.Contains(text, "<invokename") || strings.Contains(text, "<parametername") || hasOrphanJson

	// Apply regex only when markers are detected
	if hasFunctionCalls {
		text = reFunctionCallsBlock.ReplaceAllString(text, "")
	}
	if hasFunctionCall {
		text = reFunctionCallBlock.ReplaceAllString(text, "")
	}
	if hasInvoke {
		text = reInvokeFlat.ReplaceAllString(text, "")
	}
	if hasInvocation {
		text = reInvocationTag.ReplaceAllString(text, "")
	}
	if hasToolCall {
		text = reToolCallBlock.ReplaceAllString(text, "")
	}
	if hasTrigger {
		text = reTriggerSignal.ReplaceAllString(text, "")
	}
	if hasMalformed {
		// Remove malformed patterns in order of specificity
		// CRITICAL: Remove JSON-containing tags first (match to end of line),
		// then chained tags, then non-JSON tags (preserve trailing text).
		// Loop until no more malformed tags found (handles multiple tags on same line)
		// Limit iterations to prevent infinite loops on pathological input
		const maxIterations = 10
		for i := 0; i < maxIterations; i++ {
			before := text
			text = reMalformedInvokeJSON.ReplaceAllString(text, "")             // First remove <><invokename=...>[JSON] (match to end of line)
			text = reMalformedEmptyTagPrefixJSON.ReplaceAllString(text, "")     // Then remove <><tagname=...>[JSON] (match to end of line)
			text = reMalformedEmptyTagPrefixChained.ReplaceAllString(text, "")  // Then remove <><tagname=...>val<tagname=...> (chained tags)
			text = reMalformedEmptyTagPrefix.ReplaceAllString(text, "")         // Then remove <><tagname=...>value (preserves trailing text)
			text = reMalformedParamTagClosed.ReplaceAllString(text, "")         // Then remove <><parameter ...>value</parameter> (with closing tag)
			text = reMalformedParamTag.ReplaceAllString(text, "")               // Then remove <><parameter ...> (without closing tag)
			text = reMalformedMergedTag.ReplaceAllString(text, "")              // Then remove <parametername=...> (no <> prefix)
			text = reBareJSONAfterEmpty.ReplaceAllString(text, "")              // Finally remove <>[...] and <>{...} on same line
			// If no change after replacement, all malformed tags are removed
			if text == before {
				break
			}
		}
		// After removing malformed tags, clean up standalone <> and orphaned JSON.
		// This handles cross-line cases like "● <>\n  [JSON]" as well as leftover
		// JSON-only fragments that were split across streaming chunks.
		text = reStandaloneEmpty.ReplaceAllString(text, "$1") // Keep bullets, remove line-only <>
		text = reOrphanedJSON.ReplaceAllString(text, "")      // Remove orphaned JSON on indented lines
		// Finally, remove any remaining inline "<>" markers that did not belong to
		// a well-formed malformed tag pattern (e.g., "...<>"). In practice, "<>"
		// is only used by models as a broken prefix, so stripping it is safe.
		text = strings.ReplaceAll(text, "<>", "")

		// Additional cleanup for malformed JSON arrays and objects from TodoWrite
		// Remove lines that look like malformed JSON arrays/objects
		lines := strings.Split(text, "\n")
		cleanedLines := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			// Skip lines that are obviously malformed JSON arrays/objects
			if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, `"Form":`) {
				continue
			}
			if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"Form":`) {
				continue
			}
			if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, `"Form"`) {
				continue
			}
			if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"Form"`) {
				continue
			}
			// Skip lines with malformed todo items
			if strings.Contains(trimmed, `"id": "1",:`) ||
			   strings.Contains(trimmed, `"Form":`) ||
			   strings.Contains(trimmed, `"status":"}"`) {
				continue
			}
			// Skip lines that look like JSON structure indicators (arrays/objects with specific fields)
			// This catches TodoWrite leaked JSON that doesn't have "Form" but has structure
			if (strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) &&
			   reCCJSONStructIndicator.MatchString(trimmed) {
				continue
			}
			// Skip lines that contain malformed JSON fragments from real-world production log patterns
			// Pattern: "1",": "content" (malformed JSON with extra quotes)
			if strings.Contains(trimmed, `",": "`) || strings.Contains(trimmed, `": "pending"},`) {
				continue
			}
			// Skip lines that look like leaked activeForm/status fields
			if strings.Contains(trimmed, `"activeForm":`) || strings.Contains(trimmed, `"activeForm"`) {
				continue
			}
			// Skip lines that start with malformed JSON array elements
			// Pattern: [":text or ["":text (severely malformed JSON)
			if strings.HasPrefix(trimmed, `[":`) || strings.HasPrefix(trimmed, `["":`) {
				continue
			}
			// Skip lines that contain malformed field separators
			// Pattern: "},content": or ",content": or _progress
			if strings.Contains(trimmed, `"},content":`) ||
			   strings.Contains(trimmed, `",content":`) ||
			   strings.Contains(trimmed, `_progress"}`) ||
			   strings.Contains(trimmed, `_progress",`) {
				continue
			}
			// Skip lines that look like malformed JSON with missing field names
			// Pattern: "status":"}  or pending"}
			if strings.Contains(trimmed, `"status":"}`) ||
			   (strings.Contains(trimmed, `pending"}`) && !strings.Contains(trimmed, `"pending"}`)) {
				continue
			}
			// Skip lines that contain malformed state/status fields from real-world production log
			// Pattern: "state":"in_progress" or "state":", or state":
			if strings.Contains(trimmed, `"state":`) || strings.Contains(trimmed, `state":`) {
				continue
			}
			// Skip lines that contain truncated field names from real-world production log
			// Pattern: Form": or ,Form": (truncated activeForm)
			if strings.Contains(trimmed, `Form":`) && !strings.Contains(trimmed, `"Form":`) {
				continue
			}
			// Skip lines that look like malformed JSON with unquoted values
			// Pattern: "content":WebSearch (missing quotes around value)
			if strings.Contains(trimmed, `":`) && !strings.Contains(trimmed, `":"`) &&
			   !strings.Contains(trimmed, `":}`) && !strings.Contains(trimmed, `":]`) {
				// Check if there's a colon followed by non-JSON character
				if idx := strings.Index(trimmed, `":`); idx >= 0 && idx+2 < len(trimmed) {
					nextChar := trimmed[idx+2]
					if nextChar != '"' && nextChar != '[' && nextChar != '{' &&
					   nextChar != ' ' && nextChar != '\t' &&
					   !(nextChar >= '0' && nextChar <= '9') && nextChar != '-' {
						continue
					}
				}
			}
			// Skip lines that contain malformed JSON array with state/content fields
			// Pattern: [{"state":"in_progress", "content":WebSearch...
			if strings.HasPrefix(trimmed, `[{"`) && strings.Contains(trimmed, `"state"`) {
				continue
			}
			// Skip lines that look like leaked JSON from TodoWrite (from real production logs)
			// Pattern: {"id":"1","content":"...", or [{"id":"1",...
			if (strings.HasPrefix(trimmed, `{"id":`) || strings.HasPrefix(trimmed, `[{"id":`)) &&
			   (strings.Contains(trimmed, `"content":`) || strings.Contains(trimmed, `"status":`)) {
				continue
			}
			// Skip lines that contain CC internal markers (from real production logs)
			// Pattern: ImplementationPlan, TaskList, ThoughtinChinese
			if strings.Contains(trimmed, "ImplementationPlan") ||
			   strings.Contains(trimmed, "TaskList") ||
			   strings.Contains(trimmed, "ThoughtinChinese") ||
			   strings.Contains(trimmed, "Implementation Plan") ||
			   strings.Contains(trimmed, "Task List") ||
			   strings.Contains(trimmed, "Thought in Chinese") {
				continue
			}
			// Skip lines that look like CC tool output markers (from real production logs)
			// Pattern: ● Bash(...), ● Search(...), ● Read(...), ● Write(...)
			// But preserve actual tool result descriptions
			if strings.HasPrefix(trimmed, "●") && strings.Contains(trimmed, "(") {
				// Check if it's a tool invocation marker vs result description
				// Tool markers have format: ● ToolName(args) without "⎿" result indicator
				if !strings.Contains(trimmed, "⎿") && !strings.Contains(trimmed, "Done") {
					// Check for common tool names that should be filtered
					toolMarkers := []string{"● Bash(", "● Search(", "● Read(", "● Write(", "● Glob(", "● Task(", "● TodoWrite(", "● Update(", "● Edit("}
					for _, marker := range toolMarkers {
						if strings.HasPrefix(trimmed, marker) {
							continue
						}
					}
				}
			}
			// Skip lines that contain malformed XML tag fragments (from real production logs)
			// Pattern: <><parameter..., <><invoke..., <invokename=..., <parametername=...
			if strings.Contains(trimmed, "<><") || strings.Contains(trimmed, "<invokename") || strings.Contains(trimmed, "<parametername") {
				continue
			}
			cleanedLines = append(cleanedLines, line)
		}
		text = strings.Join(cleanedLines, "\n")
	}
	// Check for unclosed tags only if invoke/parameter patterns exist
	// but NOT if they have proper closing tags
	if hasInvoke || strings.Contains(text, "<parameter") {
		// Only apply unclosed tag removal if there are no closing tags
		// This prevents removing valid closed tags like <parameter name="x">v</parameter>
		hasInvokeClose := strings.Contains(text, "</invoke>")
		hasParamClose := strings.Contains(text, "</parameter>")
		if !hasInvokeClose && !hasParamClose {
			text = reUnclosedInvokeParam.ReplaceAllString(text, "")
		} else {
			// If there are some closing tags, use a more targeted approach:
			// Remove only lines that have opening tags without closing tags on the same line
			text = removeUnclosedTagLines(text)
		}
	}

	// Clean up trailing spaces on each line after XML removal
	// This handles cases like "● <><invokename=...>" -> "● " -> "●"
	text = cleanTrailingSpacesPerLine(text)

	// Remove Claude Code preamble/explanatory text
	// This must be done AFTER XML tag removal to catch explanations that contain XML
	text = removeClaudeCodePreamble(text)

	// Clean up consecutive blank lines left after tag removal
	// This compresses multiple blank lines into single blank lines
	text = cleanConsecutiveBlankLines(text)

	return strings.TrimSpace(text)
}

// cleanTrailingSpacesPerLine removes trailing spaces from each line.
// This is used after XML block removal to clean up residual spaces.
// Performance: Uses strings.Builder for efficient string concatenation.
func cleanTrailingSpacesPerLine(text string) string {
	if !strings.Contains(text, " \n") && !strings.HasSuffix(text, " ") {
		return text
	}

	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.Grow(len(text))

	for i, line := range lines {
		sb.WriteString(strings.TrimRight(line, " \t"))
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// removeClaudeCodePreamble removes explanatory text (preambles) that Claude Code
// outputs before function calls. This removes:
// - Pure JSON structures (leaked JSON from tool parameters)
// - Plan headers and meta-commentary (调研结果, 实施方案, etc.)
// - Citation markers [citation:N]
// - CC internal markers (ImplementationPlan, TaskList, etc.)
// - Malformed tool invocation markers
//
// Natural language descriptions of actual work are preserved.
//
// Performance: Line-by-line filtering with early exit for lines without indicators.
func removeClaudeCodePreamble(text string) string {
	// First pass: remove citation markers
	if strings.Contains(text, "[citation:") {
		text = reCCCitation.ReplaceAllString(text, "")
	}

	// Second pass: remove plan headers (Chinese and English)
	if strings.Contains(text, "调研") || strings.Contains(text, "实施") ||
		strings.Contains(text, "方案") || strings.Contains(text, "任务清单") ||
		strings.Contains(text, "Implementation") || strings.Contains(text, "Task") {
		text = reCCPlanHeader.ReplaceAllString(text, "")
	}

	lines := strings.Split(text, "\n")
	cleanedLines := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Empty lines are kept (will be compressed later)
		if trimmed == "" {
			cleanedLines = append(cleanedLines, line)
			continue
		}

		// Keep bullet points without content (●, •, ‣)
		if trimmed == "●" || trimmed == "•" || trimmed == "‣" {
			cleanedLines = append(cleanedLines, line)
			continue
		}

		// Skip CC internal markers (from real production logs)
		if strings.Contains(trimmed, "ImplementationPlan") ||
		   strings.Contains(trimmed, "TaskList") ||
		   strings.Contains(trimmed, "ThoughtinChinese") ||
		   strings.Contains(trimmed, "Implementation Plan") ||
		   strings.Contains(trimmed, "Task List") ||
		   strings.Contains(trimmed, "Thought in Chinese") {
			continue
		}

		// Skip lines that look like CC code block markers (from real production logs)
		// Pattern: ```python, ```bash, etc. followed by code
		// But preserve actual code blocks that are part of the response
		if strings.HasPrefix(trimmed, "```") && len(trimmed) > 3 {
			// Check if this is a language marker (```python, ```bash, etc.)
			lang := strings.TrimPrefix(trimmed, "```")
			if lang == "python" || lang == "bash" || lang == "json" || lang == "go" ||
			   lang == "javascript" || lang == "typescript" || lang == "html" || lang == "css" {
				// This is a code block start - keep it
				cleanedLines = append(cleanedLines, line)
				continue
			}
		}

		// Only remove pure JSON structures (short lines starting with { or [)
		// Keep everything else, including all natural language text
		isPureJSON := (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) &&
			len(trimmed) < 500 &&
			reCCJSONStructIndicator.MatchString(trimmed)

		if isPureJSON {
			continue
		}

		// Skip lines that contain malformed XML fragments
		if strings.Contains(trimmed, "<><") || strings.Contains(trimmed, "<invokename") || strings.Contains(trimmed, "<parametername") {
			continue
		}

		// Skip lines that look like leaked JSON field values
		// Pattern: "activeForm": "...", "status": "pending", etc.
		if strings.HasPrefix(trimmed, `"activeForm":`) || strings.HasPrefix(trimmed, `"status":`) ||
		   strings.HasPrefix(trimmed, `"Form":`) || strings.HasPrefix(trimmed, `"id":`) {
			continue
		}

		cleanedLines = append(cleanedLines, line)
	}

	return strings.Join(cleanedLines, "\n")
}

// cleanConsecutiveBlankLines compresses consecutive blank lines into single blank lines.
// This is used after XML tag removal to clean up extra blank lines left behind.
// Performance: Uses regexp to replace multiple consecutive newlines with single newline.
func cleanConsecutiveBlankLines(text string) string {
	// Fast path: if no consecutive blank lines, return early
	if !strings.Contains(text, "\n\n") {
		return text
	}
	return reConsecutiveNewlines.ReplaceAllString(text, "\n")
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

type functionToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

func collectFunctionToolDefs(toolsSlice []any) []functionToolDefinition {
	defs := make([]functionToolDefinition, 0, len(toolsSlice))
	for _, t := range toolsSlice {
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

		defs = append(defs, functionToolDefinition{
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}
	return defs
}

func buildToolSummaries(defs []functionToolDefinition) []string {
	if len(defs) == 0 {
		return nil
	}

	summaries := make([]string, 0, len(defs))
	for i, def := range defs {
		paramSummary := "None"
		if def.Parameters != nil {
			if props, ok := def.Parameters["properties"].(map[string]any); ok && len(props) > 0 {
				propNames := make([]string, 0, len(props))
				for pName := range props {
					propNames = append(propNames, pName)
				}
				sort.Strings(propNames)

				pairs := make([]string, 0, len(props))
				for _, pName := range propNames {
					pType := "any"
					if infoMap, ok := props[pName].(map[string]any); ok {
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

		var block strings.Builder
		fmt.Fprintf(&block, "%d. <tool name=\"%s\">\n", i+1, def.Name)
		block.WriteString("   Description:\n")
		if def.Description != "" {
			block.WriteString("```\n")
			block.WriteString(def.Description)
			block.WriteString("\n```\n")
		} else {
			block.WriteString("None\n")
		}
		block.WriteString("   Parameters summary: ")
		block.WriteString(paramSummary)
		summaries = append(summaries, block.String())
	}

	return summaries
}

func buildToolsXml(defs []functionToolDefinition) string {
	var sb strings.Builder
	sb.WriteString("<function_list>\n")
	for i, def := range defs {
		fmt.Fprintf(&sb, "  <tool id=\"%d\">\n", i+1)
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", escapeXml(def.Name)))
		sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", escapeXml(def.Description)))

		if def.Parameters != nil {
			params, ok := def.Parameters["properties"].(map[string]any)
			if ok && len(params) > 0 {
				required := getRequiredParams(def.Parameters)
				sb.WriteString("    <parameters>\n")

				paramNames := make([]string, 0, len(params))
				for name := range params {
					paramNames = append(paramNames, name)
				}
				sort.Strings(paramNames)

				for _, name := range paramNames {
					infoMap, _ := params[name].(map[string]any)
					sb.WriteString(fmt.Sprintf("      <param name=\"%s\">\n", escapeXml(name)))
					typeVal := "any"
					if tVal, ok := infoMap["type"].(string); ok && tVal != "" {
						typeVal = tVal
					}
					sb.WriteString(fmt.Sprintf("        <type>%s</type>\n", escapeXml(typeVal)))
					sb.WriteString(fmt.Sprintf("        <required>%v</required>\n", contains(required, name)))
					if desc, ok := infoMap["description"].(string); ok && desc != "" {
						sb.WriteString(fmt.Sprintf("        <description>%s</description>\n", escapeXml(desc)))
					}
					sb.WriteString("      </param>\n")
				}

				sb.WriteString("    </parameters>\n")
			}
		}

		sb.WriteString("  </tool>\n")
	}
	sb.WriteString("</function_list>")
	return sb.String()
}

func getRequiredParams(params map[string]any) []string {
	if params == nil {
		return nil
	}
	if reqSlice, ok := params["required"].([]any); ok {
		result := make([]string, 0, len(reqSlice))
		for _, item := range reqSlice {
			if s, ok := item.(string); ok && s != "" {
				result = append(result, s)
			}
		}
		return result
	}
	if reqStrings, ok := params["required"].([]string); ok {
		return reqStrings
	}
	return nil
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func escapeXml(s string) string {
	if s == "" {
		return ""
	}
	return xmlEscaper.Replace(s)
}

// parseFunctionCallsXML parses function calls from the assistant content using a
// trigger signal and a simple XML-like convention:
//
// <function_calls>
//
//	<function_call>
//	  <tool>tool_name</tool>
//	  <args>
//	    <param1>"value"</param1>
//	    <param2>123</param2>
//	  </args>
//	</function_call>
//
// </function_calls>
func parseFunctionCallsXML(text, triggerSignal string) []functionCall {
	if text == "" {
		return nil
	}

	cleaned := removeThinkBlocks(text)

	// Fast path: check if any XML-like content exists
	if !strings.Contains(cleaned, "<") {
		return nil
	}

	// Prefer to anchor parsing near the trigger signal when the model
	// correctly follows the prompt convention. Fall back to the first
	// <function_calls> block if no trigger is found.
	// NOTE: Use Index (first) instead of LastIndex because AI sometimes outputs
	// multiple function_calls blocks, and the first one is typically correct
	// while subsequent ones may have corrupted parameters.
	start := 0
	hasTrigger := triggerSignal != "" && strings.Contains(cleaned, triggerSignal)
	hasFunctionCalls := strings.Contains(cleaned, "<function_calls>")

	if hasTrigger {
		start = strings.Index(cleaned, triggerSignal)
	} else if hasFunctionCalls {
		start = strings.Index(cleaned, "<function_calls>")
	}

	segment := cleaned[start:]
	// Remove any orphaned trigger signals from the segment only if they exist
	if hasTrigger || strings.Contains(segment, "<Function_") || strings.Contains(segment, "<<CALL_") {
		segment = reTriggerSignal.ReplaceAllString(segment, "")
	}

	// Prefer the flat <invoke name="..."> format when present to reduce parsing overhead.
	if flatMatches := reInvokeFlat.FindAllStringSubmatch(segment, -1); len(flatMatches) > 0 {
		flatCalls := parseFlatInvokes(flatMatches)
		if len(flatCalls) > 0 {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithField("parsed_count", len(flatCalls)).Debug("Function call parsing: parsed flat invoke format")
			}
			return flatCalls
		}
	}

	// Try parsing malformed <invokename="..."> format (no space between tag and attribute)
	// This handles cases where models output <><invokename="TodoWrite"><parametername="todos">[...]
	if malformedCalls := parseMalformedInvokes(segment); len(malformedCalls) > 0 {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithField("parsed_count", len(malformedCalls)).Debug("Function call parsing: parsed malformed invokename format")
		}
		return malformedCalls
	}

	// Handle nested or double <function_calls> blocks.
	// Reasoning models sometimes start with one format (e.g., <invocation><name>...)
	// then mid-stream switch to another (nested <function_calls><function_call><tool>...).
	// We find and use the INNERMOST complete <function_calls>...</function_calls> block.
	for {
		innerIdx := strings.Index(segment, "<function_calls>")
		if innerIdx == -1 {
			break
		}
		afterFirst := segment[innerIdx+len("<function_calls>"):]
		nextOpen := strings.Index(afterFirst, "<function_calls>")
		if nextOpen != -1 && strings.Contains(afterFirst[nextOpen:], "</function_calls>") {
			// Found a nested complete block, use it instead
			segment = afterFirst[nextOpen:]
		} else {
			break
		}
	}

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

func parseFlatInvokes(matches [][]string) []functionCall {
	if len(matches) == 0 {
		return nil
	}
	results := make([]functionCall, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		args := extractParameters(m[2], reMcpParam, reGenericParam)
		results = append(results, functionCall{Name: name, Args: args})
	}
	return results
}

// parseMalformedInvokes parses malformed <invokename="..."> format tool calls.
// This handles cases where models output <><invokename="TodoWrite"><parametername="todos">[...]
// instead of the correct <invoke name="TodoWrite"><parameter name="todos">[...]</parameter></invoke>
// Performance: Uses strings.Contains for fast pre-check before regex matching.
func parseMalformedInvokes(text string) []functionCall {
	// Fast path: check if malformed invokename pattern exists
	if !strings.Contains(text, "<invokename") {
		return nil
	}

	matches := reMalformedInvoke.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	results := make([]functionCall, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}

		// Parse malformed parameter tags from the remaining content
		args := extractMalformedParameters(m[2])
		results = append(results, functionCall{Name: name, Args: args})
	}
	return results
}

// extractMalformedParameters parses malformed <parametername="..."> format parameters.
// This handles cases where models output <parametername="todos">[...] instead of
// <parameter name="todos">[...]</parameter>
// Also handles cases without closing tags and partially malformed JSON.
// Performance: Uses strings.Contains for fast pre-check before regex matching.
// Robustness: Attempts to fix common JSON formatting issues before parsing.
func extractMalformedParameters(content string) map[string]any {
	args := make(map[string]any)
	if content == "" {
		return args
	}

	// Fast path: check if malformed parametername pattern exists
	if !strings.Contains(content, "<parametername") {
		// Try JSON parsing for the entire content as fallback
		trimmed := strings.TrimSpace(content)
		if trimmed != "" && (strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) {
			if jsonVal, ok := tryParseJSON(trimmed); ok {
				args["value"] = jsonVal
			}
		}
		return args
	}

	// Try to parse malformed parameter tags
	paramMatches := reMalformedParam.FindAllStringSubmatch(content, -1)
	for _, pm := range paramMatches {
		if len(pm) < 3 {
			continue
		}
		paramName := strings.TrimSpace(pm[1])
		paramValue := strings.TrimSpace(pm[2])
		if paramName == "" {
			continue
		}

		// Try to parse the value as JSON (for arrays/objects)
		// ENHANCED: Handle cases where JSON may not be properly terminated
		if len(paramValue) > 1 {
			firstChar := paramValue[0]
			// Check if it starts with JSON array/object
			if firstChar == '[' || firstChar == '{' {
				// Try to find balanced JSON by counting brackets
				jsonStr := extractBalancedJSON(paramValue)
				if jsonStr != "" {
					if jsonVal, ok := tryParseJSON(jsonStr); ok {
						args[paramName] = jsonVal
						continue
					}
				}
				// Fallback: try parsing the entire value
				if jsonVal, ok := tryParseJSON(paramValue); ok {
					args[paramName] = jsonVal
					continue
				}
			}
		}

		// Store as string if not valid JSON
		args[paramName] = paramValue
	}

	// If no parameters found via regex, try to extract from the content directly
	// This handles cases where the value is not properly enclosed
	if len(args) == 0 {
		trimmed := strings.TrimSpace(content)
		if trimmed != "" {
			// Try JSON parsing for the entire content
			if (strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) {
				if jsonVal, ok := tryParseJSON(trimmed); ok {
					args["value"] = jsonVal
				}
			}
		}
	}

	return args
}

// extractBalancedJSON extracts a balanced JSON array or object from the beginning of a string.
// It counts brackets/braces to find the end of the JSON structure.
// Returns the extracted JSON string, or empty string if not found.
// Performance: O(n) single pass through the string.
func extractBalancedJSON(s string) string {
	if len(s) == 0 {
		return ""
	}

	firstChar := s[0]
	var openChar, closeChar byte
	switch firstChar {
	case '[':
		openChar, closeChar = '[', ']'
	case '{':
		openChar, closeChar = '{', '}'
	default:
		return ""
	}

	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case openChar:
			depth++
		case closeChar:
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}

	// If we reach here, brackets are unbalanced - return the whole string
	// and let the JSON parser try to handle it
	return s
}

// tryParseJSON attempts to parse a string as JSON, with fallback for malformed JSON.
// It tries to fix common JSON formatting issues before parsing.
// Returns the parsed value and true if successful, or nil and false if parsing fails.
func tryParseJSON(s string) (any, bool) {
	var jsonVal any
	if err := json.Unmarshal([]byte(s), &jsonVal); err == nil {
		return jsonVal, true
	}

	// If direct parsing fails, attempt to repair common malformed JSON patterns
	// that appear in Claude Code output (e.g., from real-world production log)
	repaired := repairMalformedJSON(s)
	if err := json.Unmarshal([]byte(repaired), &jsonVal); err == nil {
		return jsonVal, true
	}

	// If still fails, return false
	return nil, false
}

// repairMalformedJSON attempts to fix common JSON malformations from Claude Code output.
// Known issues derived from real production log analysis:
// 1. Incorrect field names: "Form": instead of "activeForm":
// 2. Missing commas between array elements or object items
// 3. Extra quotes inside strings: \",Form\": (extra quotes before field name)
// 4. Malformed numeric keys in arrays: 3,\"\": (should be {"id":3,...})
// 5. Unbalanced braces/brackets
// 6. Malformed todo items with missing values: {"id": "1",: (missing value after colon)
// 7. Malformed field patterns: "id": "1",": " (extra quotes in field names)
// 8. Truncated field names: Form": instead of "activeForm":
// 9. Missing quotes around values: "content":WebSearch instead of "content":"WebSearch"
// 10. Malformed state/status values: _progress instead of in_progress
// 11. Severely malformed arrays: [":text instead of [{"content":"text"
// 7. Missing quotes around field values
// 8. Malformed field patterns from real-world production log: "1",": " (extra quotes in field names)
// 9. Severely malformed JSON like [":text", "activeForm": "text","status":"}
// 10. Missing quotes around field values: "content":WebSearch -> "content":"WebSearch"
// 11. Truncated field names: Form": -> "activeForm":
// Returns a potentially repaired JSON string (best effort).
// Performance: Uses precompiled regex patterns where possible.
func repairMalformedJSON(s string) string {
	// Work on a copy
	result := s

	// Fix 0: Handle severely malformed JSON arrays starting with ":
	// Pattern: [":text" -> [{"content":"text"
	// This handles cases like [":分析Python GUI库选择并制定实施方案", "activeForm":...
	if strings.HasPrefix(result, `[":`) || strings.HasPrefix(result, `["":`) {
		// This JSON is too malformed to repair - return empty array
		return "[]"
	}

	// Fix 0a: Handle missing quotes around field values (from real-world production log)
	// Pattern: "content":WebSearch -> "content":"WebSearch"
	// This handles cases where the model outputs unquoted string values
	result = fixUnquotedFieldValues(result)

	// Fix 0b: Handle malformed field separators like ","status":"}
	// Pattern: ","status":"} -> ,"status":"pending"}
	result = strings.ReplaceAll(result, `","status":"}`, `,"status":"pending"}`)
	result = strings.ReplaceAll(result, `"status":"}`, `"status":"pending"}`)

	// Fix 0c: Handle malformed content fields like "},content":
	// Pattern: "},content": -> },"content":
	result = strings.ReplaceAll(result, `"},content":`, `},"content":`)
	result = strings.ReplaceAll(result, `",content":`, `,"content":`)

	// Fix 0d: Handle _progress pattern (malformed status value)
	// Pattern: "_progress" -> "in_progress"
	result = strings.ReplaceAll(result, `"_progress"`, `"in_progress"`)
	result = strings.ReplaceAll(result, `_progress"}`, `in_progress"}`)
	result = strings.ReplaceAll(result, `_progress",`, `in_progress",`)

	// Fix 0e: Handle truncated field names (from real-world production log)
	// Pattern: Form": -> "activeForm":
	// Pattern: state":", -> "state":"
	result = fixTruncatedFieldNames(result)

	// Fix 1: Replace "Form": with "activeForm": (common mistake in TodoWrite)
	result = strings.ReplaceAll(result, `"Form":`, `"activeForm":`)

	// Fix 2: Add missing commas between objects in arrays
	// Pattern: }[ \t\n]*{ (missing comma between objects)
	result = reJSONMissingComma.ReplaceAllString(result, `},{`)

	// Fix 3: Remove extra quotes before field names (e.g., \",Form\": -> ,"Form":
	// Common pattern: \"",Form\": (extra quote after comma)
	// Replace \"", with ," (when followed by letter)
	result = reJSONExtraQuote.ReplaceAllString(result, `,"`)

	// Fix 4: Fix "status":"}" (string value that's just a closing brace)
	// Replace "status":"}" with "status":"pending"
	result = strings.ReplaceAll(result, `"status":"}"`, `"status":"pending"`)

	// Fix 5: Remove trailing commas before } or ]
	result = reJSONTrailingComma.ReplaceAllStringFunc(result, func(match string) string {
		// Keep only the closing bracket/brace
		return string(match[len(match)-1])
	})

	// Fix 6: Fix malformed todo items with missing values
	// Pattern: {"id": "1",: -> {"id": "1", "task": "pending",
	result = reJSONMalformedTodo.ReplaceAllString(result, `{"id": "$1", "task": "pending", `)

	// Fix 7: Fix missing quotes around string values that aren't already quoted
	// Pattern: : [a-zA-Z][^",}\]\[]*: (word followed by colon, not already quoted)
	// This is a simplified heuristic - only fix common cases
	result = reJSONMissingQuotes.ReplaceAllString(result, `: "$1"$2`)

	// Fix 8: Fix malformed field patterns from real-world production log
	// Pattern: "id": "1",": " -> "id": "1", "content": "
	// This handles cases where the model outputs malformed JSON like:
	// [{"id": "1",": "研究PythonGUI框架最佳实践", "activeForm":...}]
	result = reJSONMalformedField.ReplaceAllString(result, `"id": "$1", "content": "`)

	// Fix 9: Handle malformed Form field (should be activeForm)
	// Pattern: "Form": -> "activeForm":
	result = strings.ReplaceAll(result, `"Form":`, `"activeForm":`)
	result = strings.ReplaceAll(result, `Form":`, `activeForm":`)
	result = strings.ReplaceAll(result, `Form"`, `activeForm"`)

	// Fix 10: Handle pending" pattern (malformed status value)
	// Pattern: pending" -> "pending"
	if strings.Contains(result, `pending"`) && !strings.Contains(result, `"pending"`) {
		result = strings.ReplaceAll(result, `pending"`, `"pending"`)
	}

	// Fix 11: Handle malformed array elements starting with :
	// Pattern: [":text -> [{"content":"text
	// This is a severe malformation that cannot be reliably fixed
	if strings.Contains(result, `[":`) {
		// Try to extract valid JSON objects from the malformed array
		result = extractValidJSONFromMalformed(result)
	}

	// Fix 9: Balance braces/brackets - count and add missing ones at the end
	openBraces := strings.Count(result, "{")
	closeBraces := strings.Count(result, "}")
	openBrackets := strings.Count(result, "[")
	closeBrackets := strings.Count(result, "]")

	if openBraces > closeBraces {
		result += strings.Repeat("}", openBraces-closeBraces)
	}
	if openBrackets > closeBrackets {
		result += strings.Repeat("]", openBrackets-closeBrackets)
	}

	return result
}

// extractValidJSONFromMalformed attempts to extract valid JSON objects from severely malformed JSON.
// This handles cases where the model outputs completely broken JSON like:
// [":分析Python GUI库选择并制定实施方案", "activeForm": "分析..."
// Returns a valid JSON array string or empty array if extraction fails.
func extractValidJSONFromMalformed(s string) string {
	// If the JSON is too malformed, return empty array
	if strings.HasPrefix(s, `[":`) || strings.HasPrefix(s, `["":`) {
		return "[]"
	}

	// Try to find valid JSON objects within the malformed string
	// Look for patterns like {"id": or {"content":
	var validObjects []string
	depth := 0
	start := -1
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				obj := s[start : i+1]
				// Validate the extracted object
				var test map[string]any
				if json.Unmarshal([]byte(obj), &test) == nil {
					validObjects = append(validObjects, obj)
				}
				start = -1
			}
		}
	}

	if len(validObjects) > 0 {
		return "[" + strings.Join(validObjects, ",") + "]"
	}

	return "[]"
}

// fixUnquotedFieldValues fixes missing quotes around field values in JSON.
// Pattern: "content":WebSearch -> "content":"WebSearch"
// This handles cases where the model outputs unquoted string values.
// Performance: O(n) single pass through the string.
func fixUnquotedFieldValues(s string) string {
	// Fast path: if no colon followed by letter, no fix needed
	if !strings.Contains(s, `:`) {
		return s
	}

	var result strings.Builder
	result.Grow(len(s) + 100) // Pre-allocate with some extra space for quotes

	i := 0
	for i < len(s) {
		// Look for pattern: ": followed by unquoted value
		if i+2 < len(s) && s[i] == '"' && s[i+1] == ':' {
			result.WriteByte(s[i]) // Write the closing quote
			result.WriteByte(s[i+1]) // Write the colon
			i += 2

			// Skip whitespace after colon
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				result.WriteByte(s[i])
				i++
			}

			// Check if next char is NOT a quote, bracket, brace, digit, or special value
			if i < len(s) {
				c := s[i]
				// If it's already quoted, a number, or JSON structure, skip
				if c == '"' || c == '[' || c == '{' || c == 't' || c == 'f' || c == 'n' ||
					(c >= '0' && c <= '9') || c == '-' {
					continue
				}

				// Check for true/false/null
				if i+4 <= len(s) && (s[i:i+4] == "true" || s[i:i+4] == "null") {
					continue
				}
				if i+5 <= len(s) && s[i:i+5] == "false" {
					continue
				}

				// Found unquoted string value - add opening quote
				result.WriteByte('"')

				// Read until comma, closing bracket/brace, or end of line
				valueStart := i
				for i < len(s) {
					c := s[i]
					if c == ',' || c == '}' || c == ']' || c == '\n' || c == '\r' {
						break
					}
					i++
				}

				// Write the value and closing quote
				value := strings.TrimSpace(s[valueStart:i])
				result.WriteString(value)
				result.WriteByte('"')
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}

	return result.String()
}

// fixTruncatedFieldNames fixes truncated field names in JSON.
// Pattern: Form": -> "activeForm":
// Pattern: state":", -> "state":"
// This handles cases where the model outputs truncated field names.
// IMPORTANT: Only fixes patterns that are clearly truncated (missing opening quote).
// Performance: O(n) with string replacements.
func fixTruncatedFieldNames(s string) string {
	result := s

	// Fix truncated activeForm field
	// Pattern: ,Form": -> ,"activeForm":
	// Pattern: {Form": -> {"activeForm":
	// IMPORTANT: Only match when there's no opening quote before Form
	// Check for patterns like ,Form": but NOT ,"Form":
	if strings.Contains(result, `Form":`) && !strings.Contains(result, `"Form":`) {
		result = strings.ReplaceAll(result, `,Form":`, `,"activeForm":`)
		result = strings.ReplaceAll(result, `{Form":`, `{"activeForm":`)
		result = strings.ReplaceAll(result, ` Form":`, ` "activeForm":`)
	}

	// Fix truncated state field (should be status or state)
	// Pattern: ,state": -> ,"state":
	// IMPORTANT: Only match when there's no opening quote before state
	if strings.Contains(result, `state":`) && !strings.Contains(result, `"state":`) {
		result = strings.ReplaceAll(result, `,state":`, `,"state":`)
		result = strings.ReplaceAll(result, `{state":`, `{"state":`)
	}

	// Fix truncated content field
	// Pattern: ,content": -> ,"content":
	if strings.Contains(result, `content":`) && !strings.Contains(result, `"content":`) {
		result = strings.ReplaceAll(result, `,content":`, `,"content":`)
		result = strings.ReplaceAll(result, `{content":`, `{"content":`)
	}

	// Fix truncated status field
	// Pattern: ,status": -> ,"status":
	if strings.Contains(result, `status":`) && !strings.Contains(result, `"status":`) {
		result = strings.ReplaceAll(result, `,status":`, `,"status":`)
		result = strings.ReplaceAll(result, `{status":`, `{"status":`)
	}

	// Fix truncated id field
	// Pattern: ,id": -> ,"id":
	if strings.Contains(result, `id":`) && !strings.Contains(result, `"id":`) {
		result = strings.ReplaceAll(result, `,id":`, `,"id":`)
		result = strings.ReplaceAll(result, `{id":`, `{"id":`)
	}

	// Fix truncated priority field
	// Pattern: ,priority": -> ,"priority":
	if strings.Contains(result, `priority":`) && !strings.Contains(result, `"priority":`) {
		result = strings.ReplaceAll(result, `,priority":`, `,"priority":`)
		result = strings.ReplaceAll(result, `{priority":`, `{"priority":`)
	}

	return result
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
