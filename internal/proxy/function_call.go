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
	"unicode/utf8"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// maxContentBufferBytes limits how much assistant content we buffer when
// reconstructing the XML block for function call. This avoids unbounded
// memory growth for very long streaming responses.
const maxContentBufferBytes = 256 * 1024

// NOTE: AI Review (2026-01-03) suggested removing unused regex patterns reTruncatedEscapedQuoteJSON*
// and reTruncatedJSONNoEscape*. These patterns were originally designed for truncated JSON cleanup
// but were superseded by the more comprehensive cleanTruncatedToolResultJSON function which uses
// string-based detection instead of regex. The patterns are removed to reduce dead code.

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
	reMalformedParamTagClosed = regexp.MustCompile(`<>[\s.]*<(?:parameter|param|invoke)(?:\s+)?(?:name)?[^>]*>[^<]*?</(?:parameter|param|invoke)>(?:<(?:parameter|param)\s+name="[^"]+"[^>]*>[^<]*?</(?:parameter|param)>)*`)

	// Pattern to match malformed ANTML tags with backslash-b escape sequence
	// Examples: "</antml\b:role>", "<antml\b:role>", "<antml\\b:tools>", "</antml\b:format>"
	// These are internal markers that should never be visible to users
	// The \b is a word boundary in regex but appears literally in some model outputs
	// NOTE: Only matches individual tags, NOT content between tags (to preserve user text)
	// ENHANCED: Also matches ANTML blocks with content between tags (e.g., <antml\b:format>...</antml\b:format>)
	// ENHANCED: Also matches double backslash patterns like </antml\\b:format> (escaped in output)
	// Performance: Simple string matching, O(n)
	reMalformedANTMLBackslash = regexp.MustCompile(`</?antml\\\\?b:[^>]*>`)

	// Pattern to match truncated/incomplete thinking tags that leak to output
	// Examples: "● <thinking", "<thinking", "● <think", "<think", "<thinking\n"
	// These occur when streaming is interrupted or model outputs partial tags
	// Strategy: Match incomplete thinking tags (without closing >) at end of line or followed by newline
	// Performance: O(n) character class matching
	// NOTE: Only matches incomplete tags, NOT complete <thinking>...</thinking> blocks
	// NOTE: Preserves bullet points by only matching the tag portion
	// Pattern: optional whitespace + <think or <thinking + end of line (no closing >) or followed by newline
	// ENHANCED: Also matches <thinking followed by newline (common in streaming mode)
	reTruncatedThinkingTag = regexp.MustCompile(`(?m)[ \t]*<think(?:ing)?(?:[ \t]*$|[ \t]*\n)$`)

	// Pattern to match ANTML format/tools blocks with content (entire block should be removed)
	// Examples: "<antml\b:format>...</antml\b:format>", "<antml\b:tools>...</antml\b:tools>"
	// Also matches thinking-related tags: "<antml\b:thinking_mode>...", "<antml\b:max_thinking_length>..."
	// These blocks contain internal formatting examples that should never be visible to users
	// ENHANCED: Also matches </antml\b:format>, </antml\b:role> closing tags that may leak
	// Performance: Uses (?s) for multiline matching, O(n)
	// NOTE: Unclosed ANTML blocks are handled by reANTMLRoleTag to avoid over-matching
	reANTMLBlockWithContent = regexp.MustCompile(`(?s)<antml\\{0,2}b:(format|tools|role|thinking|thinking_mode|max_thinking_length)[^>]*>.*?</antml\\{0,2}b?(?::(format|tools|role|thinking|thinking_mode|max_thinking_length))?>|<antml\\{0,2}b:(format|tools|role|thinking|thinking_mode|max_thinking_length)[^>]*>.*?</antml>`)

	// Pattern to match malformed ANTML role tags that leak to output
	// Examples: "</antml\b:role>", "<antml\b:role>", "</antml>", "<antml\b:format>"
	// These are internal markers that should never be visible to users
	// Performance: Simple string matching, O(n)
	// ENHANCED: Also matches tags with backslash-b escape like </antml\b:role>
	// ENHANCED: Also matches </antml\b:format>, </function_calls> and similar closing tags
	// ENHANCED: Also matches </antml\\b:format> with double backslash (escaped in output)
	// NOTE: Only matches individual tags, NOT content between tags (to preserve user text)
	// NOTE: </invoke> and </parameter> are handled separately to avoid removing valid XML
	reANTMLRoleTag = regexp.MustCompile(`</?antml[^>]*>|<>[\s.]*\*?</antml[^>]*>|</function_calls>|</antml\\{1,2}b:[^>]*>`)

	// Pattern to match incomplete/truncated ANTML tags (without closing >)
	// Examples: "<antml\", "<antml\b", "<antml\b:", "<antml\b:thinking", "</antml\b:thinking"
	// Also matches: "<antml\ some text" where incomplete tag is followed by space and text
	// These occur when streaming is interrupted or model outputs partial tags
	// Strategy: Match <antml or </antml followed by backslash and optional content until end of line
	// Performance: O(n) character class matching
	// NOTE: Matches incomplete ANTML tags with backslash escape sequence
	reIncompleteANTMLTag = regexp.MustCompile(`</?antml\\[^>]*`)

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

	// Pattern to match malformed parameter tags with JSON-like name attribute
	// Examples: "<><parameter name="todosid":"1","content":"..."
	// This handles cases where model outputs malformed name attribute with JSON content
	// Pattern: <><parameter name="fieldname":"value" followed by rest of line
	// Performance: O(n) character class negation
	reMalformedParamJSONName = regexp.MustCompile(`<>[\s.]*<(?:parameter|param)\s+name="[^"]*":[^\r\n]*`)

	// Pattern to match malformed invokename/parametername tags without proper spacing (NO <> prefix)
	// Examples: "<invokename=...", "<parametername=...", "<invokename=\"\">..."
	// Strategy: Match the tag and rest of current line (entire line content after tag)
	// Pattern explanation:
	//   - [ \t]* : leading spaces/tabs (optional, can be in middle of line)
	//   - <(?:invoke|parameter)name : malformed tag (no space before 'name')
	//   - [^>]*> : attributes until closing >
	//   - [^\r\n]* : rest of line (including spaces and all content)
	// Performance: O(n) character class negation
	// NOTE: This matches the entire content after the malformed tag to end of line
	// ENHANCED: Also matches empty name attribute like <invokename="">
	reMalformedMergedTag = regexp.MustCompile(`[ \t]*<(?:invoke|parameter)name[^>]*>[^\r\n]*`)

	// Pattern to match CC preamble text that describes tool usage plans
	// STRUCTURAL APPROACH: Instead of keyword matching, detect markdown structure patterns
	// Examples: "### ...", "**...**", "1. ...", "- ..."
	// These are meta-commentary headers that should be filtered based on structure, not content
	// Strategy: Match markdown headers (###) or bold markers (**) at line start
	// Performance: O(n) character class matching
	// NOTE: Only filter explicit markdown headers, preserve all natural language text
	reCCPlanHeader = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]+[^\r\n]*|[*_]{2}[^*_\r\n]+[*_]{2}[ \t]*)$$`)

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

	// Pattern to match <> followed by non-XML content like "*" or file paths
	// Examples: "<>*", "<>F:/path/file.py", "<>*</antml..."
	// Strategy: Match <> followed by content that doesn't start with < (not XML)
	// This handles cases where model outputs <> followed by glob patterns or paths
	// Performance: O(n) character class matching
	reBareContentAfterEmpty = regexp.MustCompile(`<>[\s.]*[^<\s][^\r\n]*`)

	// Pattern to match short CJK header followed by <> and JSON (entire segment removed)
	// Examples: "任务清单<>[{...}]", "实施方案<>[{...}]"
	// This handles cases where a short CJK header (2-6 chars) at line start precedes malformed JSON
	// Pattern explanation:
	//   - (?m)^ : start of line (multiline mode)
	//   - [ \t]* : optional leading whitespace
	//   - [\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}]{2,6} : 2-6 CJK characters (short header)
	//   - <> : the malformed prefix
	//   - [\[\{] : JSON array or object start
	//   - [^\r\n]* : rest of line
	// NOTE: Only matches short headers at line start, NOT after bullets or other text
	// This preserves "● 创建任务清单<>[...]" but removes "任务清单<>[...]"
	reShortCJKHeaderWithJSON = regexp.MustCompile(`(?m)^[ \t]*[\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}]{2,6}<>[\[\{][^\r\n]*`)

	// Pattern to match <> followed by JSON array/object (removes only the malformed part)
	// Examples: "<>[{...}]", "<>[content":...]", "<>{...}"
	// This handles cases where <> is followed by JSON or truncated JSON
	// Pattern explanation:
	//   - <> : the malformed prefix
	//   - [\[\{] : JSON array or object start (including truncated like [content":)
	//   - [^\r\n]* : rest of line
	// NOTE: Only removes the <>[...] part, preserves text before <>
	// ENHANCED: Also matches truncated JSON patterns like [content": or [id":
	reTextBeforeMalformedJSON = regexp.MustCompile(`<>[\[\{][^\r\n]*`)

	// Pattern to match <> followed by truncated JSON field (no opening bracket)
	// Examples from production log:
	//   - "<>id":"1","content":"联网搜索..." (field without opening bracket)
	//   - "<>联网搜索Python GUI最佳实践","activeForm":"..." (value without field name)
	//   - "<>content":"task","status":"pending"}" (field in middle of object)
	// This handles cases where model outputs truncated JSON fragments after <>
	// Pattern explanation:
	//   - <> : the malformed prefix
	//   - (?:...) : one of several JSON fragment patterns:
	//     - [a-zA-Z_][a-zA-Z0-9_]*"\s*: : field name followed by quote-colon
	//     - "[^"]*"\s*[,:] : quoted value followed by comma or colon
	//     - [^\s"<>]+"\s*[,:] : unquoted content (CJK/text) followed by quote and separator
	//   - [^\r\n]* : rest of line
	// NOTE: Structural detection - matches JSON field patterns, not keywords
	reTruncatedJSONField = regexp.MustCompile(`<>(?:[a-zA-Z_][a-zA-Z0-9_]*"\s*:|"[^"]*"\s*[,:]|[^\s"<>\[\{][^\r\n]*"\s*[,:])[^\r\n]*`)

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

	// Pattern to parse malformed property tags (new format from production logs)
	// Examples: "<propertyname=\"activeForm\"value=\"正在分析\">", "<property name=\"id\"value=\"2\">"
	// This handles cases where models output property tags instead of parameter tags
	// Captures: group 1 = property name, group 2 = property value
	// Performance: O(n) character class matching
	reMalformedProperty = regexp.MustCompile(`<(?:property\s*name|propertyname)="([^"]+)"\s*value="([^"]*)"[^>]*>`)

	// Pattern to match malformed property tags for removal (entire tag)
	// Examples: "<propertyname=\"activeForm\"value=\"正在分析\">"
	// Strategy: Match the entire property tag for removal from output
	reMalformedPropertyTag = regexp.MustCompile(`<(?:property\s*name|propertyname)="[^"]*"\s*value="[^"]*"[^>]*>`)

	// Pattern to parse invoke with arg_key/arg_value format (thinking model output)
	// Examples: "<invoke name=\"Bash<arg_key>command</arg_key><arg_value>ls</arg_value><arg_key>description</arg_key><arg_value>list files</arg_value>"
	// This handles cases where thinking models output tool calls with <arg_key>/<arg_value> pairs
	// Captures: group 1 = tool name, group 2 = remaining content with arg_key/arg_value pairs (including first <arg_key>)
	// Performance: O(n) character class matching
	reInvokeArgKeyValue = regexp.MustCompile(`<invoke\s+name="([^"<]+)(<arg_key>.*)`)

	// Pattern to extract arg_key/arg_value pairs from invoke content
	// Examples: "<arg_key>command</arg_key><arg_value>ls -la</arg_value>"
	// Captures: group 1 = key name, group 2 = value (may contain special chars)
	// Uses non-greedy match to stop at </arg_value>
	// Performance: O(n) character class matching
	reArgKeyValuePair = regexp.MustCompile(`(?s)<arg_key>([^<]+)</arg_key><arg_value>(.*?)</arg_value>`)

	// Pattern to match incomplete/unclosed invoke or parameter tags at end of content
	// Examples: "<invoke name=\"Read\">F:/path/file.py", "<parameter name=\"todos\">[{...}]"
	// These occur when models output partial XML without closing tags
	// NOTE: This pattern is applied only when no closing tag exists (checked in removeFunctionCallsBlocks)
	// Matches content until newline or end of string
	reUnclosedInvokeParam = regexp.MustCompile(`<(?:invoke|parameter)\s+name="[^"]*">[^\n]*(?:\n|$)`)
	reUnclosedMalformedNameTag = regexp.MustCompile(`(?m)<(?:invoke|parameter|property)name[^>\r\n]*$`)

	// Pattern for parsing unclosed <invoke name="..."> tags (truncated output)
	// Precompiled at package level per Go regex best practice to avoid per-call compilation overhead.
	// AI Review (2026-01-03): Moved from parseUnclosedInvokes function to package level.
	reUnclosedInvoke = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"[^>]*>`)

	// Pattern for extracting tool calls from embedded JSON content
	// Matches: "name":"ToolName" or "name": "ToolName"
	// Precompiled at package level per Go regex best practice to avoid per-call compilation overhead.
	// AI Review (2026-01-03): Moved from extractToolCallsFromJSONContent function to package level.
	reToolNameInJSON = regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)

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
	// STRUCTURAL APPROACH: Detect JSON structure patterns instead of keyword matching
	// This matches:
	//   1. JSON object start with quoted field: {"field": or {'field':
	//   2. JSON field pattern: "field": or 'field':
	//   3. JSON array of objects: [{"
	// Performance: O(n) character class matching, no backtracking
	// NOTE: This is intentionally broad to catch any leaked JSON, not just specific fields
	reCCJSONStructIndicator = regexp.MustCompile(`(?:^\s*[\[{]|["'][a-zA-Z_][a-zA-Z0-9_]*["']\s*:)`)

	xmlEscaper = strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)

	// Precompiled pattern for compressing consecutive blank lines.
	reConsecutiveNewlines = regexp.MustCompile(`\n{3,}`)

	// Precompiled patterns for repairMalformedJSON function.
	// These are compiled once at init to avoid repeated compilation in hot path.
	// Performance: Precompilation reduces regex overhead by ~10x per call.
	reJSONMissingComma   = regexp.MustCompile(`\}[ \t\n]*\{`)
	reJSONExtraQuote     = regexp.MustCompile(`\\",`)
	reJSONTrailingComma  = regexp.MustCompile(`,\s*[\]\}]`)
	reJSONMalformedTodo  = regexp.MustCompile(`\{"id":\s*"(\d+)",\s*:\s*`)
	reJSONMissingQuotes  = regexp.MustCompile(`:\s*([a-zA-Z][a-zA-Z0-9_]*)([,}\]])`)
	// Pattern to fix malformed field patterns from real-world production log (real production log)
	// Matches: {"id": "1",": " or {"id":"1",":"  (id field followed by malformed field separator)
	// This handles cases where the model outputs: {"id": "1",": "content value"
	// and converts it to: {"id": "1", "content": "content value"
	// ENHANCED: Also matches without spaces and with various quote patterns
	reJSONMalformedField = regexp.MustCompile(`"id"\s*:\s*"([^"]+)"\s*,\s*"\s*:\s*"`)
	// Pattern to fix malformed field with state instead of status
	// Matches: "state": "pending" -> "status": "pending"
	reJSONMalformedState = regexp.MustCompile(`"state"\s*:\s*"(pending|in_progress|completed)"`)
	// Pattern to fix malformed activeForm field (Form instead of activeForm)
	// Matches: "Form": "..." -> "activeForm": "..."
	reJSONMalformedForm = regexp.MustCompile(`"Form"\s*:\s*"`)
	// Pattern to fix malformed content field from real production log
	// Matches: ,": "探索... or ,": "正在... (missing field name after comma)
	// This handles cases where the model outputs: {"id": "1",": "探索最佳实践"
	// and converts it to: {"id": "1", "content": "探索最佳实践"
	// ENHANCED: Use non-greedy match with lookahead simulation for better accuracy
	// Pattern matches: ,": " followed by content until next ", or "}
	reJSONMalformedContent = regexp.MustCompile(`,\s*"\s*:\s*"([^"]*)"`)
	// Pattern to fix malformed field with colon-only separator (no field name at all)
	// Matches: {"id":"1",:"content" -> {"id":"1","content":"content"
	// This handles severely malformed JSON where field name is completely missing
	reJSONMalformedColonOnly = regexp.MustCompile(`([,{])\s*:\s*"([^"]*)"`)
	// Pattern to fix numeric value followed by missing field name
	// Matches: 1,": " or 2, ": " (numeric id followed by malformed field separator)
	// This handles cases where the model outputs: {"id":1,": "content value"
	// and converts it to: {"id":1,"content": "content value"
	reJSONNumericMissingField = regexp.MustCompile(`(\d),\s*"\s*:\s*"`)
	// Pattern to fix malformed activeForm field with missing opening quote
	// Matches: "activeForm":正在搜索" -> "activeForm":"正在搜索"
	// This handles cases where model outputs Chinese text without opening quote
	// FIXED: Exclude space and quote from first char to avoid matching already-quoted values
	// Pattern: "activeForm": followed by non-quote/space char, then any non-quote chars, then quote
	reJSONMalformedActiveFormChinese = regexp.MustCompile(`"activeForm"\s*:\s*([^",\[\]{}\s][^",\[\]{}]*)"`)
	// Pattern to fix malformed content field with missing opening quote
	// Matches: "content":使用exa工具" -> "content":"使用exa工具"
	// FIXED: Exclude space and quote from first char to avoid matching already-quoted values
	reJSONMalformedContentChinese = regexp.MustCompile(`"content"\s*:\s*([^",\[\]{}\s][^",\[\]{}]*)"`)
	// Pattern to fix malformed status field with missing opening quote
	// Matches: "status":pending" -> "status":"pending"
	// FIXED: Exclude space and quote from first char to avoid matching already-quoted values
	reJSONMalformedStatusChinese = regexp.MustCompile(`"status"\s*:\s*([^",\[\]{}\s][^",\[\]{}]*)"`)
	// Pattern to fix malformed id field with missing opening quote
	// Matches: "id":task-1" -> "id":"task-1"
	// FIXED: Exclude space and quote from first char to avoid matching already-quoted values
	reJSONMalformedIdChinese = regexp.MustCompile(`"id"\s*:\s*([^",\[\]{}\s][^",\[\]{}]*)"`)
	// Pattern to fix malformed priority field with missing opening quote
	// Matches: "priority":high" -> "priority":"high"
	// FIXED: Exclude space and quote from first char to avoid matching already-quoted values
	reJSONMalformedPriorityChinese = regexp.MustCompile(`"priority"\s*:\s*([^",\[\]{}\s][^",\[\]{}]*)"`)

	// Pattern to fix malformed status field with empty value
	// Matches: "status":"} or "status":""} -> "status":"pending"}
	reJSONMalformedStatusEmpty = regexp.MustCompile(`"status"\s*:\s*""\s*}`)
	// Pattern to fix malformed priority field
	// Matches: "priority":medium -> "priority":"medium"
	reJSONMalformedPriority = regexp.MustCompile(`"priority"\s*:\s*(medium|high|low)([,}\]])`)
	// Pattern to fix malformed status field with unquoted value
	// Matches: "status":pending -> "status":"pending"
	reJSONMalformedStatusUnquoted = regexp.MustCompile(`"status"\s*:\s*(pending|in_progress|completed)([,}\]])`)
	// Pattern to fix empty id field values
	// Matches: "id":"", or "id": "", -> "id":"1",
	// This handles cases where model outputs empty id values
	reJSONEmptyIdValue = regexp.MustCompile(`"id"\s*:\s*""\s*,`)
	// Pattern to fix severely truncated field names from production log
	// Matches: "id":"1",": " or "id":1,": " (field value followed by truncated field)
	// This handles cases where model outputs: {"id":"1",": "content value","": "activeForm value"
	// ENHANCED: Handles multiple consecutive truncated fields
	reJSONTruncatedFieldChain = regexp.MustCompile(`",\s*"\s*:\s*"`)
	// Pattern to fix empty field name pattern
	// Matches: "": "value" -> "content": "value" (infer content field)
	reJSONEmptyFieldName = regexp.MustCompile(`""\s*:\s*"`)

	// --- New patterns for production log malformed JSON ---

	// Pattern to fix malformed start of array with id field
	// Matches: [id":": "..." -> [{"id": "..."
	// Pattern to fix malformed start of array with id field
	// Matches: [id":": "..." -> [{"id": "..."
	reJSONMalformedIdStart = regexp.MustCompile(`\[\s*id":":`)

	// Pattern to fix merged status field (e.g. "Hellostatus": "pending")
	// Matches: [text]status": "..." -> [text]", "status": "..."
	reJSONMalformedStatusMerged = regexp.MustCompile(`([a-zA-Z0-9\p{L}]+)status":`)

	// Pattern to fix truncated id field (e.g. d": "4")
	// Matches: d": "..." -> "id": "..."
	// Use \b to avoid matching "id": (which contains "d":)
	reJSONMalformedIdShort = regexp.MustCompile(`\bd":\s*"`)

	// Pattern to fix content text between objects without field name
	// Matches: },检查hello.py是否存在", -> }, {"content": "检查hello.py是否存在",
	// Note: Captures the text content to wrap it properly
	reJSONMalformedContentText = regexp.MustCompile(`\}\s*,([^"{}\[\]]+)",`)

	// Pattern to fix "activeForm": "..."} without closing brace for previous object?
	// Actually the log shows: "status": "in_progress"},检查...
	// This is handled by reJSONMalformedContentText.

	// Pattern to fix missing opening brace for new object
	// Matches: , "activeForm": -> , {"activeForm":
	// Only if preceded by a comma and not inside an object? Hard to tell with regex.
	// But in the log: ...是否存在", "activeForm": ...
	// If we fix the content text first, it becomes: ...", "activeForm": ...
	// We need to ensure the object structure is valid.

	// Pattern to match orphaned thinking block (missing opening tag)
	// Matches from start of string to closing tag (removed ^ to handle leading text)
	// Uses \\{0,2}b to match antmlb, antml\b, antml\\b (need 4 backslashes in Go string for 2 in regex)
	reOrphanedThinkingBlock = regexp.MustCompile(`(?s).*?</antml\\\\{0,2}b:thinking>`)

	// Pattern to fix missing object start after comma
	// Matches: }, "id": -> }, {"id":
	reJSONMalformedMissingObjectStart = regexp.MustCompile(`\},\s*"id":`)

	// Pattern to detect truncated JSON fragments directly after text (no <> prefix)
	// This handles cases where TodoWrite tool output leaks into visible content.
	// Examples from production log:
	//   - '正在读取hello.py文件", "status": "pending"}' -> '正在读取hello.py文件'
	//   - '任务进行中", "activeForm": "正在执行"' -> '任务进行中'
	//   - '查看当前目录结构和hello.py文件内容": "in_progress"' -> '查看当前目录结构和hello.py文件内容'
	// Pattern explanation:
	//   - "?\s*:\s*" : optional quote, colon with optional whitespace, quote (JSON field separator)
	//   - "(?:in_progress|pending|completed)" : status values that indicate JSON leak
	//   - OR ", " followed by field name pattern
	// NOTE: This is a structural pattern - it detects the JSON field separator pattern
	// CRITICAL: Only match when preceded by CJK/text content (not pure JSON)
	// ENHANCED: Also match ": "status_value" pattern (field value without field name)
	reTruncatedJSONAfterText = regexp.MustCompile(`(?:"?\s*:\s*"(?:in_progress|pending|completed)"|",\s*"(?:status|activeForm|content|id|priority|state|Form)"?\s*:)`)

	// Pattern to detect TodoWrite JSON array content leak (consecutive field values)
	// This handles cases where multiple JSON field values appear consecutively without field names.
	// Examples from user report:
	//   - '设计简洁的GUI方案",设计简洁的GUI方案",3"' -> '' (entire segment is JSON fragment)
	//   - '正在修改hello.py为GUI版本",": "4"' -> '' (entire segment is JSON fragment)
	// Pattern: quoted value followed by comma and another quoted value or number
	// STRUCTURAL: Detects consecutive JSON values pattern (value",value" or value",number")
	// NOTE: This pattern is used to detect if a line contains JSON fragments, not to find position
	reTruncatedJSONConsecutiveValues = regexp.MustCompile(`"[^"]*",\s*(?:"[^"]*"|[0-9]+)"?(?:,|$)`)

	// Pattern to detect JSON field value followed by comma and number (id field leak)
	// Examples: '正在测试GUI程序运行",3"' or '设计方案",4"'
	// Pattern: quoted value followed by comma and number with optional quote
	// NOTE: This pattern is used to detect if a line contains JSON fragments, not to find position
	reTruncatedJSONValueThenNumber = regexp.MustCompile(`",\s*[0-9]+"?(?:,|$|")`)

	// Pattern to detect orphaned JSON field separator (": " without field name)
	// Examples: '": "4"' or '": "in_progress"'
	// Pattern: comma or line start followed by colon and quoted value (no field name between)
	// NOTE: This pattern is used to detect if a line contains JSON fragments, not to find position
	// CRITICAL: Must NOT match normal JSON like {"key": "value"} - only match when field name is missing
	// The pattern requires comma before the colon to ensure it's an orphaned separator
	reTruncatedJSONOrphanedFieldSep = regexp.MustCompile(`(?:^|,)\s*"?\s*:\s*"[^"]*"(?:,|$|}|])`)

	// Pattern to detect if entire line is a JSON fragment (starts with JSON-like content)
	// This handles cases where the entire line is leaked JSON content from TodoWrite
	// Examples:
	//   - '设计简洁的GUI方案",设计简洁的GUI方案",3"' -> entire line is JSON fragment
	//   - '正在测试GUI程序运行",3"' -> entire line is JSON fragment
	// Pattern: text ending with quote-comma followed by another text or number (consecutive values)
	// STRUCTURAL: Detects lines that are consecutive JSON values (not field:value pairs)
	// NOTE: This pattern specifically matches consecutive values pattern, NOT field separators
	// The key difference:
	//   - '设计简洁的GUI方案",设计简洁的GUI方案"' -> consecutive values, entire line is JSON
	//   - '查看当前目录结构": "in_progress"' -> field separator, preserve text before
	reTruncatedJSONEntireLine = regexp.MustCompile(`^[^"]*",\s*[^":]+(?:",|"$)`)

	// Pattern to detect text ending with quote-comma followed by orphaned field separator
	// This handles cases like: '正在修改hello.py为GUI版本",": "4"'
	// The entire line is a JSON fragment (activeForm value + orphaned field separator)
	// Pattern: text ending with quote-comma followed by ": "value"
	reTruncatedJSONValueWithOrphanedSep = regexp.MustCompile(`^[^"]*",\s*"?\s*:\s*"[^"]*"$`)

	// Pattern to detect truncated JSON field names at line start (no leading quote)
	// This handles cases where field names appear at the start of text without proper JSON structure.
	// Examples from user report:
	//   - 'activeForm": "正在读取hello.py文件' -> '' (entire line is JSON fragment)
	//   - 'Form":设计简短漂亮的GUI程序方案' -> '' (entire line is JSON fragment)
	//   - 'content": "联网搜索PythonGUI最佳实践' -> '' (entire line is JSON fragment)
	// Pattern explanation:
	//   - ^(?:status|activeForm|content|id|priority|state|Form)"\s*: : field name at start followed by quote-colon
	//   - This catches truncated field names that lost their opening quote and appear at line start
	// NOTE: This is a structural pattern - detects JSON field pattern at line start
	// ENHANCED: Also match field names followed by ": (quote-colon) without space
	reTruncatedJSONFieldAtStart = regexp.MustCompile(`^(?:status|activeForm|content|id|priority|state|Form)"?\s*:\s*`)

	// Pattern to detect truncated JSON field names without leading quote (in middle of text)
	// This handles cases where field names appear directly after text without proper JSON structure.
	// Examples from user report:
	//   - '查找hello.py文件", "state":_progress' -> '查找hello.py文件'
	// Pattern explanation:
	//   - (?:status|activeForm|content|id|priority|state|Form)": : field name followed by quote-colon
	//   - This catches truncated field names that lost their opening quote
	// NOTE: This is a structural pattern - detects JSON field pattern without opening quote
	reTruncatedJSONFieldNoQuote = regexp.MustCompile(`(?:status|activeForm|content|id|priority|state|Form)"?\s*:\s*`)

	// Pattern to detect truncated JSON array/object fragments after text
	// Examples:
	//   - '任务列表}, {"id": "2"' -> '任务列表'
	//   - '第一个任务"}, {"id": "2"' -> '第一个任务'
	// Pattern: closing brace/bracket followed by comma and opening brace/bracket
	// ENHANCED: Also match closing brace followed by comma and quote (JSON field)
	reTruncatedJSONObjectAfterText = regexp.MustCompile(`[}\]]\s*,\s*[\[{"]`)

	// Pattern to detect JSON fragments starting with closing brace/bracket followed by text
	// Examples from user report:
	//   - 'pending"},设计简短漂亮的GUI程序方案' -> '' (entire line is JSON fragment)
	// Pattern: closing brace/bracket followed by comma or text
	reTruncatedJSONClosingFragment = regexp.MustCompile(`^(?:pending|completed|in_progress)"?\s*[}\]],?`)


	// Pattern to fix status field ending with bracket
	// Matches: "status":] -> "status": "pending"]
	reJSONMalformedStatusBracket = regexp.MustCompile(`"status":\s*\]`)

	// Pattern to match just the closing thinking tag for manual scanning
	reANTMLClosingTag = regexp.MustCompile(`(?s)</antml\\{0,2}b:thinking>`)
)

var (
	_ = reCCPlanHeader
	_ = reJSONMalformedTodo
	_ = reJSONMalformedField
	_ = reJSONMalformedActiveFormChinese
	_ = reJSONMalformedContentChinese
	_ = reJSONMalformedStatusChinese
	_ = reJSONMalformedIdChinese
	_ = reJSONMalformedPriorityChinese
	_ = reJSONMalformedStatusEmpty
	_ = reJSONMalformedState
	_ = reJSONMalformedForm
	_ = reJSONMalformedContent
	_ = reJSONMalformedPriority
	_ = reJSONMalformedStatusUnquoted
	_ = reJSONTruncatedFieldChain
	_ = reJSONEmptyFieldName
	_ = reJSONMalformedIdStart
	_ = reJSONMalformedStatusMerged
	_ = reJSONMalformedIdShort
	_ = reJSONMalformedContentText
	_ = isMarkdownHeader
)

// isPotentialMalformedTagStart checks if a string could be the start of a malformed
// XML tag pattern that should be buffered during streaming. This is used to prevent
// partial malformed tags from being emitted to the client.
//
// Patterns detected:
//   - <> (empty tag prefix)
//   - <><invokename, <><parametername (malformed merged tags)
//   - <invokename, <parametername (malformed tags without <> prefix)
//   - <<CALL_ (trigger signal)
//   - <function_calls, <invoke, <parameter (standard XML tags)
func isPotentialMalformedTagStart(s string) bool {
	// Must start with < to be a potential tag
	if len(s) == 0 || s[0] != '<' {
		return false
	}

	// Single < at end of content - could be start of any tag
	if len(s) == 1 {
		return true
	}

	// Check for known tag prefixes
	prefixes := []string{
		"<>",
		"<<CALL_",
		"<invokename",
		"<parametername",
		"<propertyname",
		"<function_calls",
		"<function_call",
		"<invoke",
		"<parameter",
		"<tool_call",
		"<invocation",
		"<antml",
		"</antml",
		"<thinking",
		"</thinking",
		"<think",
		"</think",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}

		// Check if s is a prefix of the pattern (partial match)
		if len(s) <= len(prefix) {
			if prefix[:len(s)] == s {
				return true
			}
		}
	}

	return false
}

// applyFunctionCallRequestRewrite rewrites an OpenAI chat completions request body
// to enable middleware-based function call. It injects a system prompt describing
// available tools and removes native tools/tool_choice fields so the upstream model
// only sees the prompt-based contract.
//
// When thinking model is enabled (detected via gin context), the prompt includes
// additional instructions for handling <thinking> tags to ensure proper integration
// between extended thinking and function calling.
//
// Returns:
//   - rewritten body bytes (or the original body if no rewrite is needed)
//   - trigger signal string used to mark the function-calls XML section
//   - error when parsing fails (in which case the caller should fall back to the
//     original body)
func (ps *ProxyServer) applyFunctionCallRequestRewrite(
	c *gin.Context,
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
		logrus.WithField("group", safeGroupName(group)).Debug("applyFunctionCallRequestRewrite: No 'tools' field in request")
		return bodyBytes, "", nil
	}

	toolsSlice, ok := toolsVal.([]any)
	if !ok || len(toolsSlice) == 0 {
		logrus.WithField("group", safeGroupName(group)).Debug("applyFunctionCallRequestRewrite: 'tools' field is empty or not an array")
		return bodyBytes, "", nil
	}

	// Extract messages array. Skip rewrite if messages is missing or malformed,
	// as this indicates a non-chat request that shouldn't be rewritten.
	msgsVal, hasMessages := req["messages"]
	if !hasMessages {
		logrus.WithField("group", safeGroupName(group)).Debug("applyFunctionCallRequestRewrite: No 'messages' field in request")
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
		logrus.WithField("group", safeGroupName(group)).Debug("applyFunctionCallRequestRewrite: No valid function definitions found in tools")
		return bodyBytes, "", nil
	}

	toolsXml := buildToolsXml(toolDefs)
	toolSummaries := buildToolSummaries(toolDefs)

	// Compose final prompt content injected as a new system message.
	// Enhanced based on b4u2cc reference implementation with ANTML-style tags and
	// detailed tool call procedure. Only strict <invoke>/<parameter name="...">
	// format is allowed to reduce malformed XML outputs in CC + force_function_call mode.
	prompt := fmt.Sprintf(
		"In this environment you have access to a set of tools you can use to answer the user's question.\n\n"+
			"When you need to use a tool, you MUST strictly follow the format below.\n\n"+
			"**1. Available Tools:**\n"+
			"Here is the list of tools you can use. You have access ONLY to these tools and no others.\n"+
			"<antml\\b:tools>\n%s\n</antml\\b:tools>\n\n"+
			"**2. Tool Call Procedure:**\n"+
			"When you decide to call a tool, you MUST output EXACTLY this trigger signal: %s\n"+
			"The trigger signal MUST be output on a completely empty line by itself before any tool calls.\n"+
			"Do NOT add any other text, spaces, or characters before or after %s on that line.\n"+
			"You may provide explanations or reasoning before outputting %s, but once you decide to make a tool call, %s must come first.\n"+
			"You MUST output the trigger signal %s ONLY ONCE per response. Never output multiple trigger signals in a single response.\n\n"+
			"After outputting the trigger signal, immediately provide your tool calls enclosed in <invoke> XML tags.\n\n"+
			"**3. XML Format for Tool Calls:**\n"+
			"Your tool calls must be structured EXACTLY as follows. This is the ONLY format you can use, and any deviation will result in failure.\n\n"+
			"<antml\\b:format>\n"+
			"%s\n"+
			"<invoke name=\"Write\">\n"+
			"<parameter name=\"file_path\">C:\\\\path\\\\weather.css</parameter>\n"+
			"<parameter name=\"content\"> body {{ background-color: lightblue; }} </parameter>\n"+
			"</invoke>\n"+
			"</antml\\b:format>\n\n"+
			"IMPORTANT RULES:\n"+
			"- You may provide explanations or reasoning before deciding to call a tool.\n"+
			"- Once you decide to call a tool, you must first output the trigger signal %s on a separate line by itself.\n"+
			"- The trigger signal may only appear once per response and must not be repeated.\n"+
			"- Tool calls must use the exact XML format: immediately after the trigger signal, use <invoke> and <parameter> tags.\n"+
			"- No additional text may be added after the closing </invoke> tag.\n"+
			"- Parameters must retain punctuation (including hyphen prefixes) exactly as defined.\n"+
			"- Encode arrays and objects in JSON before placing inside <parameter>.\n"+
			"- Be concise when not using tools.\n"+
			"- After calling a tool, you will receive the tool execution result, so please wait for the result before calling the next tool.\n\n"+
			"Quick reference:\n%s\n",
		toolsXml,
		triggerSignal, triggerSignal, triggerSignal, triggerSignal, triggerSignal,
		triggerSignal,
		triggerSignal,
		strings.Join(toolSummaries, "\n\n"),
	)

	// Add thinking model instructions when extended thinking is enabled.
	// This ensures proper integration between thinking models (like deepseek-reasoner)
	// and function calling. The model should output thinking content in <thinking> tags
	// and then proceed with tool calls using the trigger signal.
	thinkingModelApplied := false
	if c != nil {
		thinkingModelApplied = c.GetBool("thinking_model_applied")
	}
	if thinkingModelApplied {
		thinkingInstructions := "\n\n**4. Extended Thinking Mode:**\n" +
			"You are running in extended thinking mode. Follow these rules:\n" +
			"- You may use <thinking>...</thinking> tags to show your reasoning process.\n" +
			"- Thinking content will be displayed to the user as your thought process.\n" +
			"- After thinking, you MUST still output the trigger signal " + triggerSignal + " and tool calls.\n" +
			"- Do NOT put tool calls inside <thinking> tags - they must come after.\n" +
			"- Example flow:\n" +
			"  <thinking>Let me analyze the user's request...</thinking>\n" +
			"  " + triggerSignal + "\n" +
			"  <invoke name=\"ToolName\">...</invoke>\n" +
			"- CRITICAL: You must output actual tool calls, not just think about them.\n"
		prompt += thinkingInstructions
	}

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

	// Append ANTML role hint to the last message to guide the model to continue
	// responding as an assistant. This follows b4u2cc reference implementation.
	if len(newMessages) > 0 {
		lastIdx := len(newMessages) - 1
		if lastMsg, ok := newMessages[lastIdx].(map[string]any); ok {
			if content, ok := lastMsg["content"].(string); ok {
				lastMsg["content"] = content + "\n\n<antml\\b:role>\n\nPlease continue responding as an assistant.\n\n</antml>"
				newMessages[lastIdx] = lastMsg
			}
		}
	}

	req["messages"] = newMessages
	if isCCRequest(c) {
		if _, ok := req["tool_choice"]; !ok {
			req["tool_choice"] = "required"
		}
	}

	if !isCCRequest(c) {
		delete(req, "tools")
		delete(req, "tool_choice")
	}

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
				cleaned := removeFunctionCallsBlocks(contentStr, cleanupModeFull)
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
		msg["content"] = removeFunctionCallsBlocks(contentStr, cleanupModeFull)
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
									// ENHANCED: Also detect JSON arrays with task fields
									hasMalformedXml := strings.Contains(text, "<>") ||
										strings.Contains(text, "<invokename") ||
										strings.Contains(text, "<parametername") ||
										(strings.Contains(text, `"todos"`) && strings.Contains(text, `[{`)) ||
										(strings.Contains(text, `"activeForm"`) && strings.Contains(text, `"`))

									// If malformed XML is detected, use full removeFunctionCallsBlocks cleanup.
									// This ensures malformed fragments are never sent to the client.
									if hasMalformedXml {
										// Apply full cleanup to remove malformed tags and their content
										cleaned := removeFunctionCallsBlocks(text, cleanupModeFull)
										if cleaned != text && logrus.IsLevelEnabled(logrus.DebugLevel) {
											logrus.WithFields(logrus.Fields{
												"removed_bytes":    len(text) - len(cleaned),
												"original_preview": utils.TruncateString(text, 80),
												"cleaned_preview":  utils.TruncateString(cleaned, 80),
											}).Debug("FC streaming: cleaned malformed XML/JSON from content chunk")
										}
										text = cleaned
										// Do NOT set insideFunctionCalls = true here
										// Let the normal <function_calls> block detection handle suppression
									}

									// Detect partial XML: content containing < followed by valid tag start character.
									// This catches character-by-character streaming where tags are split.
									// To avoid false positives with comparison operators (e.g. "x < 5"),
									// we only trigger if < is followed by a letter or / for closing tags.
									// CRITICAL: Exclude <think and <thinking tags - these are thinking mode tags
									// that should NOT trigger function call suppression. They are handled by
									// ThinkingParser and removeFunctionCallsBlocks separately.
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
													// CRITICAL: Do NOT suppress <think or <thinking tags
													// These are thinking mode tags, not function call XML
													isThinkingTag := strings.HasPrefix(remaining, "think")
													if !isThinkingTag {
														hasPartialXmlStart = true
													}
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

// processGLMBlockContent processes the content inside a <glm_block> tag.
// It removes tool call RESULTS while preserving tool call REQUESTS.
// Tool call results have indicators like is_error:true, status:completed/error, non-empty result.
// Tool call requests only have name and parameters (or is_error:false with empty result).
// GLM blocks typically contain tool call results or thinking content that should be removed.
// Only preserve content that looks like a valid tool call request (JSON with name field but no result indicators).
//
// AI Review Note (2026-01-03): This function intentionally discards non-JSON text inside GLM blocks.
// Based on production log analysis, GLM blocks only contain tool call JSON or thinking content,
// never user-visible prose. If GLM format changes to include mixed content, this logic should be revisited.
func processGLMBlockContent(content string) string {
	// If content doesn't contain JSON object, it's likely thinking content - remove it
	if !strings.Contains(content, "{") {
		return ""
	}

	// Find all JSON objects in the content
	// We need to identify each JSON object and check if it's a result or request
	result := content
	var preserved strings.Builder

	// Find JSON objects by looking for { and matching }
	for {
		// Find the start of a JSON object
		jsonStart := strings.Index(result, "{")
		if jsonStart == -1 {
			break
		}

		// Find the matching closing brace
		braceDepth := 0
		jsonEnd := -1
		inString := false
		escaped := false
		for i := jsonStart; i < len(result); i++ {
			c := result[i]
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
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
			if c == '{' {
				braceDepth++
			} else if c == '}' {
				braceDepth--
				if braceDepth == 0 {
					jsonEnd = i + 1
					break
				}
			}
		}

		if jsonEnd == -1 {
			// No matching closing brace found - malformed JSON, remove remaining content
			break
		}

		// Extract the JSON object
		jsonObj := result[jsonStart:jsonEnd]

		// Check if this JSON object is a tool call result
		if isToolCallResultJSON(jsonObj) {
			// Remove this JSON object - it's a result
			result = result[jsonEnd:]
		} else {
			// This might be a tool call request - preserve it
			preserved.WriteString(jsonObj)
			result = result[jsonEnd:]
		}
	}

	return preserved.String()
}


// removeThinkBlocks removes all thinking blocks from input text, but FIRST extracts
// any <function_calls>, <invoke>, or trigger signal blocks inside them.
// This is critical for reasoning models (DeepSeek-R1, GLM-thinking, etc.) that may
// embed tool calls within their thinking process.
//
// Supported thinking block formats:
//   - <think>...</think>
//   - <thinking>...</thinking>
//   - <antml\b:thinking>...</antml\b:thinking> (ANTML format)
//   - <antml\\b:thinking>...</antml\\b:thinking> (escaped ANTML format)
//
// Returns the cleaned text with function_calls/invoke blocks preserved at the end.
func removeThinkBlocks(text string) string {
	var extractedCalls strings.Builder

	// Helper to extract tool calls from thinking content
	extractToolCalls := func(thinkContent string) {
		// Look for function_calls blocks
		fcStart := strings.Index(thinkContent, "<function_calls>")
		if fcStart >= 0 {
			fcEnd := strings.LastIndex(thinkContent, "</function_calls>")
			if fcEnd > fcStart {
				fcBlock := thinkContent[fcStart : fcEnd+len("</function_calls>")]
				extractedCalls.WriteString("\n")
				extractedCalls.WriteString(fcBlock)
				return
			}
		}

		// Look for flat invoke format: <invoke name="...">...</invoke>
		// This handles cases where thinking models output tool calls in invoke format
		if strings.Contains(thinkContent, "<invoke ") && strings.Contains(thinkContent, "</invoke>") {
			// Find trigger signal if present
			triggerIdx := strings.Index(thinkContent, "<<CALL_")
			startIdx := 0
			if triggerIdx >= 0 {
				startIdx = triggerIdx
			} else {
				startIdx = strings.Index(thinkContent, "<invoke ")
			}
			if startIdx >= 0 {
				// Find the last </invoke> to capture all tool calls
				endIdx := strings.LastIndex(thinkContent, "</invoke>")
				if endIdx > startIdx {
					invokeBlock := thinkContent[startIdx : endIdx+len("</invoke>")]
					extractedCalls.WriteString("\n")
					extractedCalls.WriteString(invokeBlock)
				}
			}
		}
	}

	// Process standard <think> and <thinking> tags
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
			extractToolCalls(thinkContent)

			// Remove the think block (including its content)
			end += start + len(closeTag)
			text = text[:start] + text[end:]
		}
	}

	// Process ANTML format thinking tags: <antml\b:thinking>...</antml\b:thinking>
	// Also handles escaped format: <antml\\b:thinking>...</antml\\b:thinking>
	// and generic closer: </antml>
	antmlPatterns := []struct {
		open  string
		close []string // Multiple possible closing tags
	}{
		{
			open:  "<antml\\b:thinking>",
			close: []string{"</antml\\b:thinking>", "</antml>"},
		},
		{
			open:  "<antml\\\\b:thinking>",
			close: []string{"</antml\\\\b:thinking>", "</antml>"},
		},
		{
			open:  `<antml\b:thinking>`,
			close: []string{`</antml\b:thinking>`, "</antml>"},
		},
	}

	for _, pattern := range antmlPatterns {
		for {
			start := strings.Index(text, pattern.open)
			if start == -1 {
				break
			}

			// Find the closest closing tag
			minEnd := -1
			closerLen := 0
			for _, closer := range pattern.close {
				end := strings.Index(text[start+len(pattern.open):], closer)
				if end >= 0 && (minEnd == -1 || end < minEnd) {
					minEnd = end
					closerLen = len(closer)
				}
			}

			if minEnd == -1 {
				break
			}

			// Extract the content inside the ANTML thinking block
			thinkContent := text[start+len(pattern.open) : start+len(pattern.open)+minEnd]
			extractToolCalls(thinkContent)

			// Remove the ANTML thinking block
			endPos := start + len(pattern.open) + minEnd + closerLen
			text = text[:start] + text[endPos:]
		}
	}

	// Process GLM model's <glm_block>...</glm_block> tags
	// GLM blocks may contain either:
	// 1. Tool call RESULTS (with is_error:true, status:completed/error, non-empty result) - should be removed
	// 2. Tool call REQUESTS (just name and parameters, or is_error:false with empty result) - should be preserved
	for {
		start := strings.Index(text, "<glm_block>")
		if start == -1 {
			break
		}

		end := strings.Index(text[start+len("<glm_block>"):], "</glm_block>")
		if end == -1 {
			break
		}

		// Extract the content inside the glm_block
		glmContent := text[start+len("<glm_block>") : start+len("<glm_block>")+end]
		endPos := start + len("<glm_block>") + end + len("</glm_block>")

		// Process the content to remove tool call results while preserving requests
		// Find all JSON objects in the content and check each one
		processedContent := processGLMBlockContent(glmContent)

		// Also try to extract tool calls from the processed content
		extractToolCalls(processedContent)

		if strings.TrimSpace(processedContent) == "" {
			// All content was tool call results - remove the entire block
			text = text[:start] + text[endPos:]
		} else {
			// Some content remains - preserve it but remove the tags
			text = text[:start] + processedContent + text[endPos:]
		}
	}

	// Handle orphaned closing tags (e.g., </glm_block> without opening tag)
	// This can happen when the opening tag was truncated in streaming or the model
	// outputs malformed content. We need to remove content before orphaned closing tags
	// that looks like JSON tool call results.
	orphanedClosers := []string{"</glm_block>", "</antml>", "</antml\\b:thinking>", "</antml\\\\b:thinking>"}
	for _, closer := range orphanedClosers {
		for {
			closeIdx := strings.Index(text, closer)
			if closeIdx == -1 {
				break
			}

			// Check if there's a corresponding opening tag before this closer
			// For glm_block, check for <glm_block>
			// For antml closers, check for various antml opening patterns
			hasOpener := false
			searchText := text[:closeIdx]
			switch closer {
			case "</glm_block>":
				hasOpener = strings.Contains(searchText, "<glm_block>")
			case "</antml>", "</antml\\b:thinking>", "</antml\\\\b:thinking>":
				hasOpener = strings.Contains(searchText, "<antml") ||
					strings.Contains(searchText, `<antml\b:`) ||
					strings.Contains(searchText, `<antml\\b:`)
			}

			if hasOpener {
				// There's a matching opener, skip this closer (it will be handled by the main loop)
				break
			}

			// No matching opener - this is an orphaned closer
			// Try to find JSON-like content before the closer that should be removed
			// Look for patterns like: ..."name":"Read"..."is_error":true...}</glm_block>
			// We need to find where the JSON object starts (look for { before the closer)
			startIdx := closeIdx
			braceDepth := 0
			foundBrace := false
			for i := closeIdx - 1; i >= 0; i-- {
				c := text[i]
				if c == '}' {
					braceDepth++
					foundBrace = true
				} else if c == '{' {
					if braceDepth > 0 {
						braceDepth--
					}
					if braceDepth == 0 && foundBrace {
						// Found the start of the JSON object
						startIdx = i
						break
					}
				}
			}

			// If we found a JSON-like structure, check if it contains tool call result indicators
			if startIdx < closeIdx {
				jsonContent := text[startIdx:closeIdx]
				// Check for tool call result indicators using stricter validation
				// Tool call results have specific patterns that distinguish them from requests:
				// - "is_error":true/false (boolean value, not just field name)
				// - "status":"completed"/"error" (specific status values)
				// - "result":"..." with non-empty value
				// - "duration":"..." (execution time)
				// - "display_result":"..." (display output)
				isToolResult := isToolCallResultJSON(jsonContent)

				if isToolResult {
					// Remove the JSON content and the orphaned closer
					text = text[:startIdx] + text[closeIdx+len(closer):]
					logrus.WithFields(logrus.Fields{
						"closer":         closer,
						"removed_length": closeIdx + len(closer) - startIdx,
					}).Debug("removeThinkBlocks: Removed orphaned closer with tool call result JSON")
					continue
				}
			}

			// Fallback: brace matching failed (truncated JSON), try to find tool result content
			// by searching backward for tool call result indicators in the content before closer.
			// This handles cases where the opening { is truncated but the content still contains
			// tool call result fields like "status":"completed", "is_error":false, etc.
			if startIdx == closeIdx {
				// Search backward from the closer to find where the tool result content starts
				// Look for patterns that indicate the start of tool result content
				contentBefore := text[:closeIdx]

				// First, check if content before closer contains tool result indicators
				if isToolCallResultJSON(contentBefore) {
					// Find a reasonable start point by looking for sentence boundary or newline
					toolResultStart := -1

					// Look for newline first
					for i := closeIdx - 1; i >= 0; i-- {
						if text[i] == '\n' {
							// Check if content after newline contains tool result indicators
							afterNewline := text[i+1 : closeIdx]
							if isToolCallResultJSON(afterNewline) {
								toolResultStart = i + 1
								break
							}
						}
					}

					// If no newline found, look for sentence boundary (Chinese punctuation)
					if toolResultStart == -1 {
						for i := closeIdx - 1; i >= 2; i-- {
							// Check for Chinese punctuation (3-byte UTF-8)
							if text[i-2:i+1] == "。" || text[i-2:i+1] == "！" || text[i-2:i+1] == "？" {
								afterPunct := text[i+1 : closeIdx]
								if isToolCallResultJSON(afterPunct) {
									toolResultStart = i + 1
									break
								}
							}
						}
					}

					// If still no start point found, look for the first " that starts JSON-like content
					if toolResultStart == -1 {
						for i := 0; i < closeIdx; i++ {
							if text[i] == '"' {
								// Check if content from here contains tool result indicators
								segment := text[i:closeIdx]
								if isToolCallResultJSON(segment) {
									toolResultStart = i
									break
								}
							}
						}
					}

					if toolResultStart >= 0 && toolResultStart < closeIdx {
						text = text[:toolResultStart] + text[closeIdx+len(closer):]
						logrus.WithFields(logrus.Fields{
							"closer":         closer,
							"removed_length": closeIdx + len(closer) - toolResultStart,
						}).Debug("removeThinkBlocks: Removed orphaned closer with truncated tool result content")
						continue
					}
				}
			}

			// If no JSON structure found, just remove the orphaned closer tag
			text = text[:closeIdx] + text[closeIdx+len(closer):]
			logrus.WithField("closer", closer).Debug("removeThinkBlocks: Removed orphaned closer tag")
		}
	}

	// Clean up any remaining inline tool result JSON fragments
	// This handles cases where tool result JSON appears inline without any closing tag
	text = cleanTruncatedToolResultJSON(text)

	// Append extracted function_calls/invoke blocks to the end of text
	if extractedCalls.Len() > 0 {
		text = strings.TrimSpace(text) + extractedCalls.String()
	}
	return text
}

// isToolCallResultJSON checks if the given content contains tool call result indicators.
// This function uses stricter validation to distinguish tool call results from tool call requests.
//
// Tool call results have specific patterns:
//   - "is_error":true or "is_error":false (boolean value with colon)
//   - "status":"completed" or "status":"error" (specific status values)
//   - "result":"..." with non-empty value (actual execution result)
//   - "duration":"..." (execution time indicator)
//   - "display_result":"..." (display output)
//   - "mcp_server":{...} (MCP server metadata)
//
// Tool call requests typically only have:
//   - "name":"ToolName"
//   - Tool-specific parameters (query, file_path, command, etc.)
//
// This distinction is important because thinking models may output tool call parameters
// that happen to contain field names like "is_error" as part of their reasoning,
// but these should not be treated as tool call results.
//
// IMPORTANT: To avoid false positives, we use a scoring system:
//   - Primary indicators (is_error:true, status:completed/error, non-empty result) are definitive
//   - is_error:false alone is NOT sufficient (could be a tool request with default value)
//   - Secondary indicators (duration, display_result, mcp_server) require multiple to confirm
//
// AI Review Note (2026-01-03): This function uses raw substring matching which could
// theoretically match any JSON/text containing these patterns. However, it is ONLY called
// from GLM block processing and truncated JSON cleanup paths where the context is already
// known to be tool-related. Do NOT use this as a general-purpose JSON classifier.
func isToolCallResultJSON(content string) bool {
	// Primary indicators - any single one is definitive

	// Pattern 1: "is_error":true (definitive - indicates tool call failure)
	if strings.Contains(content, `"is_error":true`) || strings.Contains(content, `"is_error": true`) {
		return true
	}

	// Pattern 2: "status":"completed" or "status":"error" (specific status values)
	if strings.Contains(content, `"status":"completed"`) || strings.Contains(content, `"status":"error"`) ||
		strings.Contains(content, `"status": "completed"`) || strings.Contains(content, `"status": "error"`) {
		return true
	}

	// Pattern 3: "result":"..." with non-empty value (check for result field with content)
	// Look for "result":" followed by non-empty content before the next quote
	resultIdx := strings.Index(content, `"result":`)
	if resultIdx >= 0 {
		afterResultStr := content[resultIdx+len(`"result":`):]
		// Skip whitespace
		afterResultStr = strings.TrimLeft(afterResultStr, " \t")
		if len(afterResultStr) > 0 && afterResultStr[0] == '"' {
			// Find the closing quote
			closeQuote := strings.Index(afterResultStr[1:], `"`)
			if closeQuote > 0 {
				// Non-empty result value - definitive indicator
				return true
			}
		}
	}

	// Secondary indicators - require multiple to confirm
	// These alone might appear in thinking model output as references to previous tool results
	secondaryCount := 0

	// Pattern 4: "duration":"..." (execution time indicator)
	if strings.Contains(content, `"duration":`) {
		secondaryCount++
	}

	// Pattern 5: "display_result":"..." (display output)
	if strings.Contains(content, `"display_result":`) {
		secondaryCount++
	}

	// Pattern 6: "mcp_server":{...} (MCP server metadata)
	if strings.Contains(content, `"mcp_server":`) {
		secondaryCount++
	}

	// Pattern 7: "is_error":false counts as secondary indicator (not primary)
	// This is because tool requests may have is_error:false as a default value
	// but combined with other indicators it confirms a tool result
	if strings.Contains(content, `"is_error":false`) || strings.Contains(content, `"is_error": false`) {
		secondaryCount++
	}

	// Require at least 2 secondary indicators to confirm it's a tool result
	// This prevents false positives when thinking models reference previous tool results
	// Examples:
	//   - "is_error":false alone = NOT a result (might be tool request)
	//   - "duration":"0s" alone = NOT a result (might be reference)
	//   - "is_error":false + "duration":"0s" = is a result
	//   - "duration":"0s" + "display_result":"" = is a result
	//   - "duration":"0s" + "display_result":"" + "mcp_server":{} = is a result
	return secondaryCount >= 2
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

// cleanTruncatedToolResultJSON removes truncated tool call result JSON fragments from text.
// This handles cases where the JSON is truncated (missing opening {) but still contains
// tool result indicators like "is_error", "status", "result", "mcp_server".
//
// Production log example (2026-01-02):
// Input: "...搜索Python GUI最佳实践。\n GUI Hello World minimal code tkinter\",\"tokensNum\":\"3000\"}","display_result":"","duration":"0s"..."
// Expected: "...搜索Python GUI最佳实践。"
//
// This function handles multiple patterns of truncated tool result JSON:
// 1. \"}",  pattern - end of nested JSON string, followed by tool result fields
// 2. }","   pattern - end of JSON object, followed by tool result fields
// 3. ,"     pattern after sentence boundary - JSON field separator
// 4. Newline followed by JSON-like content
// 5. Sentence boundary followed by JSON-like content
//
// IMPORTANT: This function should NOT remove content inside <invoke> or <parameter> tags,
// as those are tool call requests, not tool call results.
//
// AI Review Note (2026-01-03): This function only removes the FIRST detected fragment.
// Based on production log analysis, multiple truncated result fragments in a single response
// are extremely rare. If this becomes an issue, the function can be wrapped in a loop.
// Current design prioritizes simplicity and performance over handling edge cases.
func cleanTruncatedToolResultJSON(text string) string {
	// Fast path: if text contains <invoke> or <parameter> tags, skip cleanup
	// These are tool call requests, not tool call results
	if strings.Contains(text, "<invoke") || strings.Contains(text, "<parameter") {
		return text
	}

	// Fast path: no tool result indicators (both regular and escaped quote patterns)
	if !strings.Contains(text, `"is_error"`) && !strings.Contains(text, `"status":"completed"`) &&
		!strings.Contains(text, `"status":"error"`) && !strings.Contains(text, `"mcp_server"`) &&
		!strings.Contains(text, `"display_result"`) && !strings.Contains(text, `"duration"`) &&
		!strings.Contains(text, `"result":`) &&
		!strings.Contains(text, `\"is_error\"`) && !strings.Contains(text, `\"status\":\"completed\"`) &&
		!strings.Contains(text, `\"status\":\"error\"`) && !strings.Contains(text, `\"mcp_server\"`) &&
		!strings.Contains(text, `\"display_result\"`) && !strings.Contains(text, `\"duration\"`) &&
		!strings.Contains(text, `\"result\":`) {
		return text
	}

	logrus.WithField("text_length", len(text)).Debug("cleanTruncatedToolResultJSON: Starting cleanup")

	result := text

	// Tool result indicator patterns to search for (both regular and escaped quote patterns)
	indicatorPatterns := []string{
		// Regular quote patterns
		`"display_result"`,
		`"is_error":true`,
		`"is_error":false`,
		`"is_error": true`,
		`"is_error": false`,
		`"status":"completed"`,
		`"status":"error"`,
		`"status": "completed"`,
		`"status": "error"`,
		`"mcp_server"`,
		`"duration"`,
		// Escaped quote patterns (for JSON inside strings)
		`\"is_error\":true`,
		`\"is_error\":false`,
		`\"status\":\"completed\"`,
		`\"status\":\"error\"`,
		`\"mcp_server\"`,
		`\"display_result\"`,
		`\"duration\"`,
	}

	// Find the first tool result indicator
	firstIndicatorIdx := -1
	isEscapedPattern := false
	for _, pattern := range indicatorPatterns {
		idx := strings.Index(result, pattern)
		if idx >= 0 && (firstIndicatorIdx == -1 || idx < firstIndicatorIdx) {
			firstIndicatorIdx = idx
			isEscapedPattern = strings.HasPrefix(pattern, `\"`)
		}
	}

	if firstIndicatorIdx == -1 {
		return result
	}

	startIdx := firstIndicatorIdx
	endIdx := len(result)

	// Search in a slightly larger range to include patterns that might overlap with indicator
	searchEnd := firstIndicatorIdx + 1
	if searchEnd > len(result) {
		searchEnd = len(result)
	}
	searchText := result[:searchEnd]

	// Try different patterns in order of priority
	foundStart := false

	// Pattern 1: \"}",  (end of nested JSON string) - highest priority
	// This handles: ...content\"}","display_result"...
	// Production log example (2026-01-03):
	//   query\":\"Python GUI tkinter...\",\"tokensNum\":\"3000\"}","display_result":"","duration":"0s"...
	// The \"}",  pattern indicates the end of a nested JSON string value, followed by outer JSON fields
	// NOTE: We start deletion from the \"}",  pattern itself, NOT from the sentence boundary before it.
	// This preserves the nested JSON string value (which may contain user-visible content like search queries)
	// while removing the tool result metadata fields.
	if !foundStart {
		patternIdx := strings.LastIndex(searchText, `\"}",`)
		if patternIdx >= 0 {
			startIdx = patternIdx
			foundStart = true
			// Check if there's a newline before the pattern - if so, start from the newline
			// This handles cases where the JSON fragment is on a separate line
			for i := patternIdx - 1; i >= 0; i-- {
				if result[i] == '\n' {
					startIdx = i
					break
				}
			}
		}
	}

	// Pattern 1a: \"}"," (end of nested JSON string followed by comma) - specific for Scenario A
	// This handles: ...3000\"}","display_result...
	// Production log example: query\":\"Python...\",\"tokensNum\":\"3000\"}","display_result...
	if !foundStart {
		patternIdx := strings.LastIndex(searchText, `\"}","`)
		if patternIdx >= 0 {
			startIdx = patternIdx
			foundStart = true
			// Check if there's a newline before the pattern
			for i := patternIdx - 1; i >= 0; i-- {
				if result[i] == '\n' {
					startIdx = i
					break
				}
			}
		}
	}

	// Pattern 1b: \"}" (end of nested JSON string object) - general case
	if !foundStart {
		patternIdx := strings.LastIndex(searchText, `\"}"`)
		if patternIdx >= 0 {
			startIdx = patternIdx
			foundStart = true
			// Check if there's a newline before the pattern
			for i := patternIdx - 1; i >= 0; i-- {
				if result[i] == '\n' {
					startIdx = i
					break
				}
			}
		}
	}

	// Pattern 2: }","  (end of JSON object followed by field)
	if !foundStart {
		patternIdx := strings.LastIndex(searchText, `}","`)
		if patternIdx >= 0 {
			startIdx = patternIdx
			foundStart = true
			// Check if there's a newline before the pattern
			for i := patternIdx - 1; i >= 0; i-- {
				if result[i] == '\n' {
					startIdx = i
					break
				}
			}
		}
	}

	// Pattern 3: Newline - if there's a newline before the indicator, start from there
	if !foundStart {
		for i := firstIndicatorIdx - 1; i >= 0; i-- {
			if result[i] == '\n' {
				startIdx = i
				foundStart = true
				break
			}
			// Check for sentence boundary (Chinese punctuation - 3 bytes)
			if i >= 2 {
				prevStr := result[i-2 : i+1]
				if prevStr == "。" || prevStr == "！" || prevStr == "？" {
					startIdx = i + 1
					foundStart = true
					break
				}
			}
			// Check for sentence boundary (ASCII punctuation)
			if result[i] == '.' || result[i] == '!' || result[i] == '?' {
				startIdx = i + 1
				foundStart = true
				break
			}
		}
	}

	// Pattern 4: Sentence boundary followed by ," or ","
	// This handles: "...最佳实践。","display_result"..." or "...文件。,"display_result"..."
	if !foundStart {
		// Look for sentence boundary followed by JSON separator
		for i := firstIndicatorIdx - 1; i >= 0; i-- {
			// Check for ," or "," pattern
			if i >= 1 && result[i] == '"' && result[i-1] == ',' {
				// Found ," - check if there's a sentence boundary before it
				if i >= 2 {
					prevChar := result[i-2]
					if prevChar == '.' || prevChar == '!' || prevChar == '?' {
						startIdx = i - 1 // Start from comma, keep sentence boundary
						foundStart = true
						break
					}
				}
				// Check for Chinese punctuation before ,"
				if i >= 4 {
					prevStr := result[i-4 : i-1]
					if prevStr == "。" || prevStr == "！" || prevStr == "？" {
						startIdx = i - 1 // Start from comma, keep Chinese punctuation
						foundStart = true
						break
					}
				}
			}
			// Check for ,  pattern (comma followed by quote)
			if result[i] == ',' && i+1 < firstIndicatorIdx && result[i+1] == '"' {
				// Found ," - check if there's a sentence boundary before it
				if i >= 1 {
					prevChar := result[i-1]
					if prevChar == '.' || prevChar == '!' || prevChar == '?' {
						startIdx = i // Start from comma, keep sentence boundary
						foundStart = true
						break
					}
				}
				// Check for Chinese punctuation before ,"
				if i >= 3 {
					prevStr := result[i-3 : i]
					if prevStr == "。" || prevStr == "！" || prevStr == "？" {
						startIdx = i // Start from comma, keep Chinese punctuation
						foundStart = true
						break
					}
				}
			}
		}
	}

	// Pattern 5: Just sentence boundary (fallback)
	if !foundStart {
		for i := firstIndicatorIdx - 1; i >= 0; i-- {
			c := result[i]
			if c == '.' || c == '!' || c == '?' {
				startIdx = i + 1
				foundStart = true
				break
			}
			if i >= 2 && (result[i-2:i+1] == "。" || result[i-2:i+1] == "！" || result[i-2:i+1] == "？") {
				startIdx = i + 1
				foundStart = true
				break
			}
		}
	}

	// Find the end of the tool result JSON
	foundEnd := false

	if isEscapedPattern {
		// For escaped JSON patterns, find the end by looking for CJK characters
		for i := firstIndicatorIdx; i < len(result); i++ {
			r, size := utf8.DecodeRuneInString(result[i:])
			if size == 3 && r >= 0x4E00 && r <= 0x9FFF {
				endIdx = i
				foundEnd = true
				break
			}
		}
	} else {
		// For regular JSON, find the end by looking for closing braces or CJK characters
		braceDepth := 0
		inString := false
		escaped := false
		for i := startIdx; i < len(result); i++ {
			c := result[i]
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
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
			if c == '{' {
				braceDepth++
			} else if c == '}' {
				braceDepth--
				if i+1 < len(result) {
					r, size := utf8.DecodeRuneInString(result[i+1:])
					if size == 3 && r >= 0x4E00 && r <= 0x9FFF {
						endIdx = i + 1
						foundEnd = true
						break
					}
				}
			}
			r, size := utf8.DecodeRuneInString(result[i:])
			if size == 3 && r >= 0x4E00 && r <= 0x9FFF && !inString {
				endIdx = i
				foundEnd = true
				break
			}
		}
	}

	// If no CJK end found, check for truncated JSON ending patterns
	if !foundEnd {
		truncatedPatterns := []string{`},"type`, `}},"type`, `}}`}
		for _, tp := range truncatedPatterns {
			idx := strings.Index(result[startIdx:], tp)
			if idx >= 0 {
				endIdx = startIdx + idx + len(tp)
				break
			}
		}
	}

	// Remove the JSON fragment
	if startIdx < endIdx && endIdx <= len(result) {
		result = result[:startIdx] + result[endIdx:]
		logrus.WithFields(logrus.Fields{
			"start":   startIdx,
			"end":     endIdx,
		}).Debug("cleanTruncatedToolResultJSON: Removed fragment")
	}

	return result
}

type functionCallCleanupMode uint8

const (
	cleanupModeFull functionCallCleanupMode = iota
	cleanupModeArtifactsOnly
)

func removeFunctionCallsBlocks(text string, mode ...functionCallCleanupMode) string {
	cleanupMode := cleanupModeFull
	if len(mode) > 0 {
		cleanupMode = mode[0]
	}

	// Fast path: if no XML-like content and no orphaned JSON fragments, return early.
	// strings.Contains is ~8x faster than regex for simple checks; reOrphanedJSON
	// is only used here to catch bare JSON lines that may appear in separate
	// chunks after "<>" prefix was removed earlier in the stream.
	hasOrphanJson := reOrphanedJSON.MatchString(text)
	// ENHANCED: Also check for truncated JSON fragments after text (no <> prefix)
	// This handles cases like: '正在读取hello.py文件", "status": "pending"}'
	// Also check for truncated field names without leading quote (e.g., 'activeForm": "...')
	// Also check for JSON fragments at line start (e.g., 'Form":设计...')
	// Also check for consecutive JSON values (e.g., '设计简洁的GUI方案",设计简洁的GUI方案",3"')
	hasTruncatedJSON := reTruncatedJSONAfterText.MatchString(text) ||
		reTruncatedJSONObjectAfterText.MatchString(text) ||
		reTruncatedJSONFieldNoQuote.MatchString(text) ||
		reTruncatedJSONFieldAtStart.MatchString(text) ||
		reTruncatedJSONClosingFragment.MatchString(text) ||
		reTruncatedJSONConsecutiveValues.MatchString(text) ||
		reTruncatedJSONValueThenNumber.MatchString(text) ||
		reTruncatedJSONOrphanedFieldSep.MatchString(text) ||
		reTruncatedJSONEntireLine.MatchString(text) ||
		reTruncatedJSONValueWithOrphanedSep.MatchString(text)
	if !strings.Contains(text, "<") && !hasOrphanJson && !hasTruncatedJSON {
		if strings.ContainsAny(text, "\n\r") {
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				trimmedLine := strings.TrimSpace(line)
				if trimmedLine == "" {
					continue
				}
				if shouldSkipMalformedLine(trimmedLine) {
					hasOrphanJson = true
					break
				}
				// ENHANCED: Check for truncated JSON in each line
				if findJSONLeakStartIndex(trimmedLine) >= 0 {
					hasTruncatedJSON = true
					break
				}
			}
		}
	}
	if !strings.Contains(text, "<") && !hasOrphanJson && !hasTruncatedJSON {
		trimmed := strings.TrimSpace(text)
		if (trimmed == "●" || trimmed == "•" || trimmed == "‣") &&
			!strings.ContainsAny(text, "\n\r") &&
			(strings.HasSuffix(text, " ") || strings.HasSuffix(text, "\t")) {
			return trimmed
		}
		// Even without XML tags, check for Claude Code preambles and remove them
		if cleanupMode == cleanupModeFull {
			text = removeClaudeCodePreamble(text)
		}
		return strings.TrimSpace(text)
	}

	// ENHANCED: Handle truncated JSON fragments even without XML markers
	// This handles cases where JSON fragments appear directly after text
	if hasTruncatedJSON && !strings.Contains(text, "<") {
		lines := strings.Split(text, "\n")
		cleanedLines := make([]string, 0, len(lines))
		inCodeBlock := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Track code block state - content inside code blocks should be preserved
			if strings.HasPrefix(trimmed, "```") {
				inCodeBlock = !inCodeBlock
				cleanedLines = append(cleanedLines, line)
				continue
			}
			if inCodeBlock {
				cleanedLines = append(cleanedLines, line)
				continue
			}

			if trimmed == "" {
				cleanedLines = append(cleanedLines, line)
				continue
			}
			// CRITICAL: Check for truncated JSON FIRST, before shouldSkipMalformedLine
			// This ensures we preserve text before the JSON leak
			// When cut == 0, the entire line is JSON fragment and should be skipped
			if cut := findJSONLeakStartIndex(trimmed); cut >= 0 {
				if cut == 0 {
					// Entire line is JSON fragment, skip it
					continue
				}
				kept := strings.TrimSpace(trimmed[:cut])
				// Skip if kept is empty or only contains bullet points
				if kept == "" || kept == "●" || kept == "•" || kept == "‣" {
					continue
				}
				// Preserve leading whitespace from original line
				leadingWS := ""
				for i, c := range line {
					if c != ' ' && c != '\t' {
						leadingWS = line[:i]
						break
					}
				}
				cleanedLines = append(cleanedLines, leadingWS+kept)
				continue
			}
			// Only skip if the entire line is malformed (no text to preserve)
			if shouldSkipMalformedLine(trimmed) {
				continue
			}
			cleanedLines = append(cleanedLines, line)
		}
		text = strings.Join(cleanedLines, "\n")
		if cleanupMode == cleanupModeFull {
			text = removeClaudeCodePreamble(text)
		}
		return strings.TrimSpace(text)
	}

	// Phase 0: Remove orphaned thinking blocks (missing opening tag)
	// This must be done first to remove the "useless info" described by user
	if strings.Contains(text, "</antml") {
		text = removeOrphanedThinkingBlocks(text)
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
	// ENHANCED: Added <propertyname and <property detection for new malformed format
	hasMalformed := strings.Contains(text, "<>") || strings.Contains(text, "<invokename") || strings.Contains(text, "<parametername") || strings.Contains(text, "<propertyname") || strings.Contains(text, "<property ") || hasOrphanJson
	// Check for ANTML role tags that should be removed (internal markers)
	// ENHANCED: Also check for backslash-b escape pattern like </antml\b:role>
	// ENHANCED: Also check for </function_calls> closing tag that may leak
	hasANTML := strings.Contains(text, "<antml") || strings.Contains(text, "</antml") || strings.Contains(text, `\b:`) || strings.Contains(text, "</function_calls>")
	// Check for truncated thinking tags (e.g., "● <thinking" without closing)
	hasTruncatedThinking := strings.Contains(text, "<think") && !strings.Contains(text, "</think")

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
	// Remove ANTML role tags (internal markers that should not be visible)
	if hasANTML {
		// First remove entire ANTML blocks with content (e.g., <antml\b:format>...</antml\b:format>)
		text = reANTMLBlockWithContent.ReplaceAllString(text, "")
		// Then remove individual ANTML tags
		text = reANTMLRoleTag.ReplaceAllString(text, "")
		text = reMalformedANTMLBackslash.ReplaceAllString(text, "")
		// Finally remove incomplete/truncated ANTML tags at end of string
		// These occur when streaming is interrupted or model outputs partial tags
		text = reIncompleteANTMLTag.ReplaceAllString(text, "")
	}
	// Remove truncated thinking tags (e.g., "● <thinking" without closing)
	if hasTruncatedThinking {
		text = reTruncatedThinkingTag.ReplaceAllString(text, "")
	}
	if hasMalformed {
		// Remove malformed patterns in order of specificity
		// CRITICAL: Remove JSON-containing tags first (match to end of line),
		// then chained tags, then non-JSON tags (preserve trailing text).
		// Loop until no more malformed tags found (handles multiple tags on same line)
		// Limit iterations to prevent infinite loops on pathological input
		// OPTIMIZATION: Use const for max iterations to allow compiler optimization
		const maxIterations = 10
		for i := 0; i < maxIterations; i++ {
			before := text
			// Phase 1: Remove malformed tags with JSON content (highest priority)
			text = reMalformedInvokeJSON.ReplaceAllString(text, "")             // <><invokename=...>[JSON]
			text = reMalformedEmptyTagPrefixJSON.ReplaceAllString(text, "")     // <><tagname=...>[JSON]
			text = reMalformedParamJSONName.ReplaceAllString(text, "")          // <><parameter name="field":"value"...
			// Phase 2: Remove chained malformed tags
			text = reMalformedEmptyTagPrefixChained.ReplaceAllString(text, "")  // <><tag1=...>val<tag2=...>
			// Phase 3: Remove malformed tags with proper closing (with context)
			text = reMalformedParamTagClosed.ReplaceAllString(text, "")         // <><parameter ...>value</parameter>
			// Phase 4: Remove malformed tags without closing
			text = reMalformedParamTag.ReplaceAllString(text, "")               // <><parameter ...>
			text = reMalformedMergedTag.ReplaceAllString(text, "")              // <parametername=...>
			text = reMalformedPropertyTag.ReplaceAllString(text, "")            // <propertyname=...value=...>
			// Phase 5: Remove general malformed prefixes
			text = reMalformedEmptyTagPrefix.ReplaceAllString(text, "")         // <><tagname=...>value
			// Phase 6: Remove short CJK header followed by malformed JSON (entire segment)
			// MUST be before reTextBeforeMalformedJSON to catch "任务清单<>[...]" pattern
			text = reShortCJKHeaderWithJSON.ReplaceAllString(text, "")          // 任务清单<>[...]
			// Phase 7: Remove <> followed by JSON (preserves longer text before <>)
			text = reTextBeforeMalformedJSON.ReplaceAllString(text, "")         // <>[...]
			text = reTruncatedJSONField.ReplaceAllString(text, "")              // <>id":"1",...
			text = reBareJSONAfterEmpty.ReplaceAllString(text, "")              // <>[...] or <>{...}
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
		// Remove <> followed by non-XML content (glob patterns, file paths, etc.)
		text = reBareContentAfterEmpty.ReplaceAllString(text, "")
		// Finally, remove any remaining inline "<>" markers that did not belong to
		// a well-formed malformed tag pattern (e.g., "...<>"). In practice, "<>"
		// is only used by models as a broken prefix, so stripping it is safe.
		text = strings.ReplaceAll(text, "<>", "")
		text = reUnclosedMalformedNameTag.ReplaceAllString(text, "")

		// Additional cleanup for malformed JSON arrays and objects from TodoWrite
		// Remove lines that look like malformed JSON arrays/objects
		lines := strings.Split(text, "\n")
		cleanedLines := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			// Use helper function to check if line should be skipped
			if shouldSkipMalformedLine(trimmed) {
				continue
			}
			// ENHANCED: Check for truncated JSON fragments after text (no <> prefix)
			// This handles cases like: '正在读取hello.py文件", "status": "pending"}'
			// When cut == 0, the entire line is JSON fragment and should be skipped
			if cut := findJSONLeakStartIndex(trimmed); cut >= 0 {
				if cut == 0 {
					// Entire line is JSON fragment, skip it
					continue
				}
				kept := strings.TrimSpace(trimmed[:cut])
				// Skip if kept is empty or only contains bullet points
				if kept == "" || kept == "●" || kept == "•" || kept == "‣" {
					continue
				}
				// Preserve leading whitespace from original line
				leadingWS := ""
				for i, c := range line {
					if c != ' ' && c != '\t' {
						leadingWS = line[:i]
						break
					}
				}
				cleanedLines = append(cleanedLines, leadingWS+kept)
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
	if cleanupMode == cleanupModeFull {
		text = removeClaudeCodePreamble(text)
	}

	// Clean up consecutive blank lines left after tag removal
	// This compresses multiple blank lines into single blank lines
	text = cleanConsecutiveBlankLines(text)

	return strings.TrimSpace(text)
}

// removeOrphanedThinkingBlocks removes thinking blocks that are missing their opening tag.
// It scans backwards from the closing tag </antml:thinking> until it hits a "safe" boundary
// (start of string, or > / ] / } characters indicating end of previous structure).
func removeOrphanedThinkingBlocks(text string) string {
	// Find all closing tags
	locs := reANTMLClosingTag.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text
	}

	var sb strings.Builder
	lastPos := 0

	for _, loc := range locs {
		end := loc[1]
		// Start scanning backwards from loc[0] (start of closing tag)
		start := loc[0]

		// Find the "safe" start of this thinking block
		// We look for the last occurrence of >, ], or } before this tag
		safeStart := lastPos
		for i := start - 1; i >= lastPos; i-- {
			c := text[i]
			if c == '>' || c == ']' || c == '}' {
				safeStart = i + 1
				break
			}
		}

		// Append text from lastPos to safeStart (this is the preserved content)
		sb.WriteString(text[lastPos:safeStart])

		// The text from safeStart to end is the thinking block, skip it (remove it)
		lastPos = end
	}

	// Append remaining text
	sb.WriteString(text[lastPos:])

	return sb.String()
}
// Returns true if the line contains malformed JSON/XML patterns that should be removed.
//
// Structural patterns detected:
// 1. Pure JSON lines: lines starting with { or [ that contain JSON field patterns
// 2. Malformed XML tags: <tagname (no space before attribute name)
// 3. Malformed JSON fragments: orphaned field values, truncated structures
//
// Performance: Uses fast string operations with early exit on common cases.
func shouldSkipMalformedLine(trimmed string) bool {
	// Empty lines are kept
	if trimmed == "" {
		return false
	}

	// Fast path: lines without JSON/XML indicators are kept
	hasJSONIndicator := strings.ContainsAny(trimmed, `"{}[]`)
	hasXMLIndicator := strings.Contains(trimmed, "<")
	if !hasJSONIndicator && !hasXMLIndicator {
		return false
	}

	if hasJSONIndicator && !hasXMLIndicator {
		hasNonPunct := false
	loopPunctScan:
		for _, r := range trimmed {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				hasNonPunct = true
				break
			}
			if r > 127 {
				hasNonPunct = true
				break
			}
			switch r {
			case ' ', '\t', '\r', '\n', '{', '}', '[', ']', '"', ':', ',', '.':
				continue
			default:
				hasNonPunct = true
				break loopPunctScan
			}
		}
		if !hasNonPunct {
			return true
		}
	}

	if len(trimmed) >= 2 && (trimmed[0] == ']' || trimmed[0] == '}') && trimmed[1] != ' ' && trimmed[1] != '\t' {
		tail := trimmed[1:]
		if strings.ContainsAny(tail, `":,`) || strings.Contains(tail, ".py") {
			return true
		}
	}

	// STRUCTURAL CHECK 1: Malformed XML tag fragments
	// Pattern: <tagname=" (no space between tag and attribute) indicates malformed XML
	if hasXMLIndicator {
		// Check for <> followed by < (chained malformed tags)
		if strings.Contains(trimmed, "<><") {
			return true
		}
		// Check for malformed tags: <tagname=" pattern (no space before attribute)
		// This catches <invokename=", <parametername=", <propertyname=", etc.
		if isMalformedXMLTagPattern(trimmed) {
			return true
		}
	}

	// STRUCTURAL CHECK 2: Pure JSON structures (lines starting with { or [)
	// Only skip if it's a pure JSON line with field patterns
	// ENHANCED: Also check for bullet + JSON pattern (e.g., "● [{"id":1,...}]")
	trimmedForJSON := trimmed
	// Check for bullet characters (multi-byte UTF-8, must use rune comparison)
	if len(trimmed) >= 3 { // Bullet chars are 3 bytes in UTF-8
		firstRune, runeSize := utf8.DecodeRuneInString(trimmed)
		if firstRune == '●' || firstRune == '•' || firstRune == '‣' {
			// Skip bullet and following whitespace
			trimmedForJSON = strings.TrimLeft(trimmed[runeSize:], " \t")
		}
	}
	if strings.HasPrefix(trimmedForJSON, "{") || strings.HasPrefix(trimmedForJSON, "[") {
		// Count JSON structural elements to determine if it's pure JSON
		// A line with 2+ field patterns ("field":) is likely leaked JSON
		fieldPatternCount := strings.Count(trimmedForJSON, `":`)
		if fieldPatternCount >= 2 {
			return true
		}
		// Single field pattern with JSON brackets is also likely leaked
		if fieldPatternCount >= 1 && (strings.Contains(trimmedForJSON, `},`) || strings.Contains(trimmedForJSON, `}]`)) {
			return true
		}
	}

	// STRUCTURAL CHECK 3: Orphaned JSON field values at line start
	// Pattern: line starts with "field": which indicates leaked JSON
	if strings.HasPrefix(trimmed, `"`) && strings.Contains(trimmed, `":`) {
		colonIdx := strings.Index(trimmed, `":`)
		if colonIdx > 0 && colonIdx < 30 { // Field name should be short identifier
			fieldName := trimmed[1:colonIdx]
			// Check if field name is a valid identifier (alphanumeric + underscore)
			if isValidJSONFieldName(fieldName) {
				return true
			}
		}
	}

	// STRUCTURAL CHECK 3b: Truncated JSON field without opening quote
	// Pattern: field": which indicates leaked JSON (e.g., id":"1","content":"...")
	// This handles cases where the opening quote was in a previous chunk
	if hasJSONIndicator {
		// Look for pattern: identifier followed by ": (field name without opening quote)
		quoteColonIdx := strings.Index(trimmed, `":`)
		if quoteColonIdx > 0 && quoteColonIdx < 30 {
			// Check if characters before ": form a valid field name
			potentialField := trimmed[:quoteColonIdx]
			if isValidJSONFieldName(potentialField) && strings.Count(trimmed, `":`) >= 2 {
				return true
			}
		}
	}

	// STRUCTURAL CHECK 4: Malformed JSON fragments (structural patterns)
	// These patterns indicate broken JSON structure
	if strings.Contains(trimmed, `,:`) || // Missing field name before colon
		strings.Contains(trimmed, `",": "`) || // Malformed field separator
		strings.HasPrefix(trimmed, `[":`) || // Malformed array start
		strings.HasPrefix(trimmed, `["":`) { // Empty string in array
		return true
	}

	// STRUCTURAL CHECK 5: Lines with high JSON field density
	// If a line has many "field": patterns, it's likely leaked JSON
	if hasJSONIndicator {
		fieldPatternCount := strings.Count(trimmed, `":`)
		// Lines with 3+ field patterns are almost certainly leaked JSON
		if fieldPatternCount >= 3 {
			return true
		}
	}

	return false
}

// isMalformedXMLTagPattern checks if text contains malformed XML tag pattern.
// Pattern: <tagname=" where there's no space between tag name and attribute.
// This is a structural check that catches any malformed tag, not just specific ones.
func isMalformedXMLTagPattern(text string) bool {
	// Find all < characters and check what follows
	idx := 0
	for {
		pos := strings.Index(text[idx:], "<")
		if pos == -1 {
			break
		}
		pos += idx
		// Check if this is a malformed tag: <tagname=" pattern
		// Valid XML: <tag name="..."> (space before attribute)
		// Malformed: <tagname="..."> (no space before attribute)
		remaining := text[pos+1:]
		if len(remaining) < 3 {
			break
		}
		// Skip closing tags </...>
		if remaining[0] == '/' {
			idx = pos + 1
			continue
		}
		// Skip special tags like <? or <!
		if remaining[0] == '?' || remaining[0] == '!' {
			idx = pos + 1
			continue
		}
		// Find the end of tag name (first non-alphanumeric character)
		tagEnd := 0
		for i, c := range remaining {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
				tagEnd = i
				break
			}
		}
		if tagEnd > 0 && tagEnd < len(remaining) {
			// Check if immediately followed by =" (malformed) vs space (valid)
			afterTag := remaining[tagEnd:]
			if strings.HasPrefix(afterTag, `="`) || strings.HasPrefix(afterTag, `='`) {
				return true
			}
		}
		idx = pos + 1
	}
	return false
}

// isValidJSONFieldName checks if a string is a valid JSON field name (identifier).
// Valid field names are short alphanumeric strings with underscores.
func isValidJSONFieldName(name string) bool {
	if len(name) == 0 || len(name) > 30 {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
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
// outputs before function calls.
//
// STRUCTURAL APPROACH: All filtering is based on structure detection, not keywords.
// This removes:
// - Pure JSON structures (detected by { or [ prefix + field patterns)
// - Citation markers (detected by [citation:N] pattern)
// - Internal markers (detected by CamelCase structure or space-separated title words)
// - Malformed XML fragments (detected by tag structure)
// - Lines with text followed by malformed JSON/XML (entire line removed)
// - Standalone bold headers (**Title**) that are meta-commentary
//
// Natural language descriptions and content headers (## Title) are preserved.
//
// Performance: Line-by-line filtering with early exit for lines without indicators.
func removeClaudeCodePreamble(text string) string {
	// First pass: remove citation markers (structural pattern [citation:N])
	if strings.Contains(text, "[citation:") {
		text = reCCCitation.ReplaceAllString(text, "")
	}

	lines := strings.Split(text, "\n")
	cleanedLines := make([]string, 0, len(lines))
	inCodeBlock := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			cleanedLines = append(cleanedLines, line)
			continue
		}
		if inCodeBlock {
			cleanedLines = append(cleanedLines, line)
			continue
		}

		// Empty lines are kept (will be compressed later)
		if trimmed == "" {
			cleanedLines = append(cleanedLines, line)
			continue
		}

		// Keep tool output lines (Claude Code uses "⎿" prefix)
		if strings.HasPrefix(trimmed, "⎿") {
			cleanedLines = append(cleanedLines, line)
			continue
		}
		if shouldSkipMalformedLine(trimmed) {
			continue
		}

		if strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣") {
			bulletLen := len("●")
			if strings.HasPrefix(trimmed, "•") {
				bulletLen = len("•")
			} else if strings.HasPrefix(trimmed, "‣") {
				bulletLen = len("‣")
			}
			rest := strings.TrimSpace(trimmed[bulletLen:])
			if rest != "" && isStandaloneToolMarker(rest) {
				cleanedLines = append(cleanedLines, line)
				continue
			}
		}
		if (strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣")) && strings.Contains(trimmed, ".py") {
			idx := -1
			for k := 1; k+1 < len(trimmed); k++ {
				if trimmed[k] == ':' && (trimmed[k+1] == '\\' || trimmed[k+1] == '/') {
					prev := trimmed[k-1]
					if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') {
						idx = k - 1
						break
					}
				}
			}
			if idx == -1 {
				idx = strings.LastIndex(trimmed, "**/")
			}
			if idx > 0 {
				prev := trimmed[idx-1]
				if prev != ' ' && prev != '\t' && prev != '(' && prev != '[' && prev != '{' {
					tail := trimmed[idx:]
					if !strings.ContainsAny(tail, " \t") && (strings.Contains(tail, ".py") || strings.Contains(tail, ".pyw")) {
						trimmed = strings.TrimSpace(trimmed[:idx])
						line = trimmed
						if trimmed == "" {
							continue
						}
					}
				}
			}
		}

		// Drop standalone bullet markers (●, •, ‣)
		if trimmed == "●" || trimmed == "•" || trimmed == "‣" {
			seenBlank := false
			for j := i + 1; j < len(lines); j++ {
				nextTrimmed := strings.TrimSpace(lines[j])
				if nextTrimmed == "" {
					seenBlank = true
					continue
				}
				if !seenBlank {
					break
				}
				if strings.HasPrefix(nextTrimmed, "```") || strings.HasPrefix(nextTrimmed, "⎿") {
					break
				}
				if nextTrimmed == "●" || nextTrimmed == "•" || nextTrimmed == "‣" {
					break
				}
				if strings.HasPrefix(nextTrimmed, "●") || strings.HasPrefix(nextTrimmed, "•") || strings.HasPrefix(nextTrimmed, "‣") {
					break
				}
				if isPureJSONLine(nextTrimmed) || isMalformedXMLLine(nextTrimmed) || isLeakedJSONFieldLine(nextTrimmed) {
					break
				}
				if isInternalMarkerLine(nextTrimmed) || isStandaloneBoldHeader(nextTrimmed) {
					break
				}

				lines[j] = trimmed + " " + nextTrimmed
				i = j - 1
				break
			}
			continue
		}

		if strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣") {
			bullet := "●"
			bulletLen := len("●")
			if strings.HasPrefix(trimmed, "•") {
				bullet = "•"
				bulletLen = len("•")
			} else if strings.HasPrefix(trimmed, "‣") {
				bullet = "‣"
				bulletLen = len("‣")
			}

			rest := strings.TrimSpace(trimmed[bulletLen:])
			if rest == "" {
				continue
			}
			if isStandaloneToolMarker(rest) {
				cleanedLines = append(cleanedLines, line)
				continue
			}
			if isPureJSONLine(rest) {
				continue
			}
			if cut := findJSONLeakStartIndex(rest); cut >= 0 {
				kept := strings.TrimSpace(rest[:cut])
				if kept == "" {
					continue
				}
				bulletIndex := strings.Index(line, bullet)
				if bulletIndex < 0 {
					cleanedLines = append(cleanedLines, bullet+" "+kept)
					continue
				}
				leading := line[:bulletIndex]
				cleanedLines = append(cleanedLines, leading+bullet+" "+kept)
				continue
			}
			cleanedLines = append(cleanedLines, line)
			continue
		}

		// STRUCTURAL CHECK: Skip lines that are internal markers
		// Detected by CamelCase structure (e.g., ImplementationPlan, TaskList)
		// or space-separated title words (e.g., "Task List and Thought in Chinese")
		if isInternalMarkerLine(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip standalone bold headers (**Title**)
		// These are meta-commentary headers, not content headers
		// Content headers with # are preserved
		if isStandaloneBoldHeader(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip pure JSON structures
		// Detected by { or [ prefix + field patterns
		if isPureJSONLine(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip malformed XML fragments
		// Detected by <> or <tagname patterns
		if isMalformedXMLLine(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip leaked JSON field values at line start
		// Detected by "field": pattern at line start
		if isLeakedJSONFieldLine(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip standalone tool markers (not part of bullet points)
		// Detected by ToolName( pattern
		if isStandaloneToolMarker(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip lines with text followed by malformed JSON/XML
		// Pattern: "任务清单<>[...]" - text immediately followed by malformed structure
		if hasTrailingMalformedStructure(trimmed) {
			continue
		}

		// STRUCTURAL CHECK: Skip orphaned header text left after malformed structure removal
		// Pattern: "任务清单" (short text without punctuation, not a sentence)
		if isOrphanedHeaderText(trimmed) {
			continue
		}

		cleanedLines = append(cleanedLines, line)
	}

	return strings.Join(cleanedLines, "\n")
}

func findJSONLeakStartIndex(text string) int {
	if text == "" {
		return -1
	}
	if (text[0] == '{' || text[0] == '[') && isPureJSONLine(text) {
		return 0
	}

	// ENHANCED: Check for bullet + JSON pattern (e.g., "● [{"id":1,...}]")
	// If line starts with bullet followed by whitespace and JSON, return position after bullet
	if len(text) >= 3 {
		firstRune, runeSize := utf8.DecodeRuneInString(text)
		if firstRune == '●' || firstRune == '•' || firstRune == '‣' {
			// Skip bullet and following whitespace
			afterBullet := strings.TrimLeft(text[runeSize:], " \t")
			if len(afterBullet) > 0 && (afterBullet[0] == '{' || afterBullet[0] == '[') && isPureJSONLine(afterBullet) {
				// Return position of the JSON start (after bullet and whitespace)
				jsonStartPos := len(text) - len(afterBullet)
				return jsonStartPos
			}
		}
	}

	// ENHANCED: Check for truncated JSON fragment patterns
	// Return the earliest match position among all patterns
	minPos := -1

	// Pattern 0: Check if line starts with JSON field pattern (entire line is JSON fragment)
	// This handles cases like: 'Form":设计简短漂亮的GUI程序方案'
	if loc := reTruncatedJSONFieldAtStart.FindStringIndex(text); loc != nil && loc[0] == 0 {
		return 0
	}

	// Pattern 0b: Check if line starts with JSON closing fragment
	// This handles cases like: 'pending"},设计简短漂亮的GUI程序方案'
	if loc := reTruncatedJSONClosingFragment.FindStringIndex(text); loc != nil && loc[0] == 0 {
		return 0
	}

	// Pattern 0c: Check if entire line is a JSON fragment (leaked TodoWrite content)
	// This handles cases like:
	//   - '设计简洁的GUI方案",设计简洁的GUI方案",3"' -> entire line is JSON fragment
	//   - '正在测试GUI程序运行",3"' -> entire line is JSON fragment
	//   - '正在修改hello.py为GUI版本",": "4"' -> entire line is JSON fragment
	// These are JSON array content leaks where the text looks like normal content but ends with JSON patterns
	if reTruncatedJSONEntireLine.MatchString(text) {
		return 0
	}

	// Pattern 0d: Check for text ending with quote-comma followed by orphaned field separator
	// This handles cases like: '正在修改hello.py为GUI版本",": "4"'
	// The entire line is a JSON fragment (activeForm value + orphaned field separator)
	if reTruncatedJSONValueWithOrphanedSep.MatchString(text) {
		return 0
	}

	// Pattern 1: text followed by ", "field": (JSON field separator after string value)
	// This handles cases like: '正在读取hello.py文件", "status": "pending"}'
	if loc := reTruncatedJSONAfterText.FindStringIndex(text); loc != nil {
		// ENHANCED: Check if there's a sentence boundary before the JSON leak
		// If the text before the JSON leak ends with a sentence-ending punctuation,
		// and there's more text between the punctuation and the JSON leak,
		// truncate at the punctuation instead
		beforeJSON := text[:loc[0]]
		if sentenceEnd := findLastSentenceEnd(beforeJSON); sentenceEnd >= 0 {
			// Check if there's non-whitespace content between sentence end and JSON leak
			afterSentence := strings.TrimSpace(beforeJSON[sentenceEnd+1:])
			if len(afterSentence) > 0 {
				// There's content between sentence end and JSON leak
				// This content is likely part of the JSON fragment (e.g., task content)
				// Truncate at the sentence end
				return sentenceEnd + 1
			}
		}
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// Pattern 2: text followed by }, { or ], [ (JSON object/array separator)
	// This handles cases like: '任务列表}, {"id": "2"}'
	if loc := reTruncatedJSONObjectAfterText.FindStringIndex(text); loc != nil {
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// Pattern 3: truncated field name without leading quote (e.g., 'activeForm": "...')
	// This handles cases from user report where field names appear without proper JSON structure
	// Only use this pattern if it's not at the start (start case handled by Pattern 0)
	if loc := reTruncatedJSONFieldNoQuote.FindStringIndex(text); loc != nil && loc[0] > 0 {
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// Pattern 4: consecutive JSON values (e.g., '设计简洁的GUI方案",设计简洁的GUI方案",3"')
	// This handles TodoWrite JSON array content leak
	if loc := reTruncatedJSONConsecutiveValues.FindStringIndex(text); loc != nil {
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// Pattern 5: JSON value followed by comma and number (e.g., '正在测试GUI程序运行",3"')
	// This handles id field leak in TodoWrite
	if loc := reTruncatedJSONValueThenNumber.FindStringIndex(text); loc != nil {
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// Pattern 6: orphaned JSON field separator (e.g., '": "4"' or '": "in_progress"')
	// This handles cases where field name is missing but separator and value remain
	if loc := reTruncatedJSONOrphanedFieldSep.FindStringIndex(text); loc != nil {
		if minPos == -1 || loc[0] < minPos {
			minPos = loc[0]
		}
	}

	// If we found a match from the regex patterns, return it
	if minPos >= 0 {
		return minPos
	}

	// Fallback: scan for JSON field patterns manually
	fieldCount := 0
	fieldStart := -1
	for i := 0; i < len(text); i++ {
		if text[i] != '"' {
			continue
		}

		j := i + 1
		for ; j < len(text) && j-i <= 32; j++ {
			c := text[j]
			if c == '"' {
				break
			}
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				j = -1
				break
			}
		}
		if j > i+1 && j < len(text) && text[j] == '"' {
			name := text[i+1 : j]
			if isValidJSONFieldName(name) {
				k := j + 1
				for k < len(text) && (text[k] == ' ' || text[k] == '\t') {
					k++
				}
				if k < len(text) && text[k] == ':' {
					fieldCount++
					if fieldStart == -1 {
						fieldStart = i
					}
					i = k
					continue
				}
			}
		}

		k := i + 1
		for k < len(text) && (text[k] == ' ' || text[k] == '\t') {
			k++
		}
		if k < len(text) && text[k] == ':' {
			start := i
			for start > 0 {
				c := text[start-1]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
					start--
					continue
				}
				break
			}
			if start < i {
				name := text[start:i]
				if isValidJSONFieldName(name) {
					fieldCount++
					if fieldStart == -1 {
						fieldStart = start
					}
					i = k
					continue
				}
			}
		}
	}
	if fieldCount >= 1 {
		return fieldStart
	}
	return -1
}

// isInternalMarkerLine checks if a line is an internal marker that should be filtered.
// STRUCTURAL APPROACH: Detects internal markers by structure patterns only, no keyword matching.
// Patterns detected:
// 1. CamelCase concatenated words (e.g., "ImplementationPlan", "TaskList")
// 2. Space-separated title words with "and" connector (e.g., "Task List and Thought in Chinese")
// 3. Comma-separated CamelCase markers (e.g., "ImplementationPlan, TaskList")
// 4. Lines with only CamelCase words and connectors (no natural language content)
// 5. Mixed CamelCase with lowercase connectors (e.g., "TaskListandThoughtinChinese")
// Performance: O(n) character scanning
func isInternalMarkerLine(trimmed string) bool {
	if len(trimmed) < 8 || len(trimmed) > 150 {
		return false
	}

	// Remove leading # symbols for markdown headers
	cleanedTrimmed := strings.TrimLeft(trimmed, "# \t")
	if len(cleanedTrimmed) < 8 {
		return false
	}

	// STRUCTURAL PATTERN 1: Check for comma-separated or "and"-connected words
	// If line contains ", " or " and ", check if it's composed mainly of CamelCase/TitleCase words
	if strings.Contains(trimmed, ", ") || strings.Contains(trimmed, " and ") {
		camelCount := 0
		titleWordCount := 0
		totalWords := 0
		words := strings.FieldsFunc(trimmed, func(r rune) bool {
			return r == ',' || r == ' '
		})
		for _, word := range words {
			// Skip common connectors
			lowerWord := strings.ToLower(word)
			if lowerWord == "and" || lowerWord == "in" || lowerWord == "the" || lowerWord == "a" || lowerWord == "of" {
				continue
			}
			if len(word) < 2 {
				continue
			}
			totalWords++
			if isCamelCaseWord(word) {
				camelCount++
			}
			if isTitleCaseWord(word) {
				titleWordCount++
			}
		}
		// If most words are CamelCase or TitleCase, it's likely an internal marker
		// Require at least 2 CamelCase words OR 4+ TitleCase words (to avoid filtering normal sentences)
		if camelCount >= 2 || (titleWordCount >= 4 && totalWords > 0 && float64(titleWordCount)/float64(totalWords) > 0.7) {
			return true
		}
	}

	// STRUCTURAL PATTERN 2: Check for single CamelCase marker (no spaces, multiple uppercase)
	// This catches patterns like "ImplementationPlan", "TaskListandThought"
	if isCamelCaseWord(cleanedTrimmed) {
		return true
	}

	// STRUCTURAL PATTERN 3: Check for mixed CamelCase with lowercase connectors
	// This catches patterns like "ImplementationPlan,TaskListandThoughtinChinese"
	// or "TaskListandThoughtinChinese" (CamelCase words joined by lowercase connectors)
	if isMixedCamelCaseMarker(cleanedTrimmed) {
		return true
	}

	return false
}

// isMixedCamelCaseMarker checks if text is a mixed CamelCase marker with lowercase connectors.
// Examples: "ImplementationPlan,TaskListandThoughtinChinese", "TaskListandThought"
// Pattern: Multiple CamelCase words joined by lowercase connectors (and, in, the, etc.)
// This catches internal markers that models output without spaces.
func isMixedCamelCaseMarker(text string) bool {
	if len(text) < 12 {
		return false
	}

	// Count uppercase letters and transitions from lowercase to uppercase
	upperCount := 0
	transitions := 0
	prevLower := false

	for _, r := range text {
		if r >= 'A' && r <= 'Z' {
			upperCount++
			if prevLower {
				transitions++
			}
			prevLower = false
		} else if r >= 'a' && r <= 'z' {
			prevLower = true
		} else if r == ',' || r == ' ' {
			// Allow comma/space separators
			prevLower = false
		} else {
			// Contains non-letter characters (except comma/space), not a pure marker
			return false
		}
	}

	// Mixed CamelCase markers have 3+ uppercase letters and 2+ transitions
	// This catches "TaskListandThoughtinChinese" (T, L, T, C = 4 uppercase, 3 transitions)
	return upperCount >= 3 && transitions >= 2
}

// isTitleCaseWord checks if a word starts with uppercase (title case).
// Used to detect space-separated title words like "Task", "List", "Thought".
func isTitleCaseWord(word string) bool {
	if len(word) < 2 {
		return false
	}
	// Must start with uppercase letter
	if word[0] < 'A' || word[0] > 'Z' {
		return false
	}
	// Rest should be lowercase letters only
	for _, r := range word[1:] {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// isCamelCaseWord checks if a word is CamelCase (internal marker pattern)
// Examples: "ImplementationPlan", "TaskList", "ThoughtinChinese"
func isCamelCaseWord(word string) bool {
	if len(word) < 8 {
		return false
	}
	// Must start with uppercase letter
	if word[0] < 'A' || word[0] > 'Z' {
		return false
	}
	// Count uppercase letters and check for lowercase-to-uppercase transitions
	upperCount := 0
	hasLower := false
	transitions := 0
	prevLower := false

	for _, r := range word {
		if r >= 'A' && r <= 'Z' {
			upperCount++
			if prevLower {
				transitions++
			}
			prevLower = false
		} else if r >= 'a' && r <= 'z' {
			hasLower = true
			prevLower = true
		} else if r != '_' && r != ',' {
			// Contains non-letter characters (except underscore/comma), not a pure marker
			return false
		}
	}
	// Internal markers have 2+ uppercase letters, some lowercase, and at least 1 transition
	// This catches "ImplementationPlan" (I->P transition) but not "ALLCAPS"
	return upperCount >= 2 && hasLower && transitions >= 1
}

// isMarkdownHeader checks if a line is a markdown header that should be filtered.
// STRUCTURAL APPROACH: Detects markdown structure patterns.
// Examples: "## Implementation Plan", "**实施方案**", "### 任务清单"
// Performance: O(1) prefix checks
func isMarkdownHeader(trimmed string) bool {
	// Check for # headers (## Title, ### Title, etc.)
	if strings.HasPrefix(trimmed, "#") {
		// Count # characters
		hashCount := 0
		for _, r := range trimmed {
			if r == '#' {
				hashCount++
			} else {
				break
			}
		}
		// Valid markdown header has 1-6 # followed by space
		if hashCount >= 1 && hashCount <= 6 && len(trimmed) > hashCount {
			nextChar := trimmed[hashCount]
			if nextChar == ' ' || nextChar == '\t' {
				return true
			}
		}
	}

	// Check for bold text headers (**Title** or __Title__)
	if (strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**")) ||
		(strings.HasPrefix(trimmed, "__") && strings.HasSuffix(trimmed, "__")) {
		// This is a standalone bold line, likely a header
		return true
	}

	return false
}

// isStandaloneBoldHeader checks if a line is a standalone bold header (**Title**).
// These are meta-commentary headers that should be filtered.
// Content headers with # prefix are preserved.
func isStandaloneBoldHeader(trimmed string) bool {
	if len(trimmed) < 5 {
		return false
	}
	// Check for **Title** or __Title__ pattern
	if (strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**")) ||
		(strings.HasPrefix(trimmed, "__") && strings.HasSuffix(trimmed, "__")) {
		return true
	}
	return false
}

// hasTrailingMalformedStructure checks if a line has text followed by malformed JSON/XML.
// Pattern: "任务清单<>[...]" or "实施方案[{...}]"
// The entire line should be removed when text is immediately followed by malformed structure.
// Lines with bullet points (●, •, ‣) are preserved as they are user-facing content.
func hasTrailingMalformedStructure(trimmed string) bool {
	// Preserve lines with bullet points - these are user-facing content
	if strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣") {
		return false
	}

	// Check for <> followed by [ or {
	if idx := strings.Index(trimmed, "<>"); idx > 0 {
		rest := trimmed[idx+2:]
		if strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "{") ||
			strings.HasPrefix(rest, "<") {
			return true
		}
	}
	// Check for text followed directly by [{ (malformed JSON array/object)
	// Pattern: non-JSON text ending with [{
	if idx := strings.Index(trimmed, "[{"); idx > 0 {
		// Check if there's non-JSON text before [{
		prefix := trimmed[:idx]
		// If prefix doesn't look like JSON (no quotes, colons), it's text + malformed JSON
		if !strings.Contains(prefix, `":`) && !strings.HasPrefix(prefix, "{") {
			return true
		}
	}
	return false
}

// isOrphanedHeaderText is disabled to avoid over-filtering.
// The reTextBeforeMalformedJSON regex handles "CJK text + <> + JSON" patterns.
// Standalone CJK text without malformed JSON should be preserved.
func isOrphanedHeaderText(trimmed string) bool {
	_ = trimmed
	return false
}

// findLastSentenceEnd finds the position of the last sentence-ending punctuation in text.
// Returns -1 if no sentence end is found.
// Sentence-ending punctuation includes: 。！？.!?
// NOTE: For ASCII punctuation (.!?), only consider it a sentence end if followed by
// whitespace or CJK character (to avoid matching file extensions like "hello.py")
func findLastSentenceEnd(text string) int {
	lastPos := -1
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		// Chinese punctuation is always a sentence end
		if r == '。' || r == '！' || r == '？' {
			lastPos = i + size - 1 // Position of the last byte of the punctuation
		} else if r == '.' || r == '!' || r == '?' {
			// ASCII punctuation: check what follows
			nextPos := i + size
			if nextPos >= len(text) {
				// End of string, not a sentence end (could be truncated)
				i += size
				continue
			}
			nextRune, _ := utf8.DecodeRuneInString(text[nextPos:])
			// Only consider it a sentence end if followed by whitespace or CJK
			if nextRune == ' ' || nextRune == '\t' || nextRune == '\n' ||
				(nextRune >= 0x4E00 && nextRune <= 0x9FFF) || // CJK Unified Ideographs
				(nextRune >= 0x3400 && nextRune <= 0x4DBF) { // CJK Extension A
				lastPos = i + size - 1
			}
		}
		i += size
	}
	return lastPos
}

// isPureJSONLine checks if a line is pure JSON that should be filtered.
// STRUCTURAL APPROACH: Detects JSON by structure, not content.
// Performance: O(n) character counting
func isPureJSONLine(trimmed string) bool {
	if len(trimmed) == 0 || len(trimmed) > 500 {
		return false
	}
	// Must start with { or [
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return false
	}
	// Count JSON structural elements
	fieldPatternCount := strings.Count(trimmed, `":`)
	// A line with 2+ field patterns is likely leaked JSON
	if fieldPatternCount >= 2 {
		return true
	}
	// Single field pattern with JSON brackets is also likely leaked
	if fieldPatternCount >= 1 && (strings.Contains(trimmed, `},`) || strings.Contains(trimmed, `}]`)) {
		return true
	}
	return false
}

// isMalformedXMLLine checks if a line contains malformed XML fragments.
// STRUCTURAL APPROACH: Detects malformed XML by tag structure.
// Performance: O(n) string contains checks
func isMalformedXMLLine(trimmed string) bool {
	if !strings.Contains(trimmed, "<") {
		return false
	}
	return strings.Contains(trimmed, "<><") ||
		strings.Contains(trimmed, "<invokename") ||
		strings.Contains(trimmed, "<parametername") ||
		strings.Contains(trimmed, "<propertyname")
}

// isLeakedJSONFieldLine checks if a line is a leaked JSON field value.
// STRUCTURAL APPROACH: Detects lines starting with "field": pattern.
// Performance: O(1) prefix check + O(n) index search
func isLeakedJSONFieldLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, `"`) {
		return false
	}
	// Find the closing quote and colon
	colonIdx := strings.Index(trimmed, `":`)
	if colonIdx <= 0 || colonIdx > 30 {
		return false
	}
	// Check if the field name is a short identifier (not natural text)
	fieldName := trimmed[1:colonIdx]
	// Field names are typically short alphanumeric identifiers
	if len(fieldName) > 20 {
		return false
	}
	for _, r := range fieldName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// isStandaloneToolMarker checks if a line is a standalone tool marker.
// STRUCTURAL APPROACH: Detects tool markers by function call pattern.
// Performance: O(1) prefix checks
func isStandaloneToolMarker(trimmed string) bool {
	if strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣") {
		return false
	}

	candidate := trimmed
	if strings.HasPrefix(candidate, "●") {
		candidate = strings.TrimSpace(candidate[len("●"):])
	} else if strings.HasPrefix(candidate, "•") {
		candidate = strings.TrimSpace(candidate[len("•"):])
	} else if strings.HasPrefix(candidate, "‣") {
		candidate = strings.TrimSpace(candidate[len("‣"):])
	}

	op := strings.IndexByte(candidate, '(')
	if op <= 0 || op > 40 {
		return false
	}
	if op > 0 && candidate[op-1] == ' ' {
		return false
	}
	if !strings.Contains(candidate[op+1:], ")") {
		return false
	}

	name := candidate[:op]
	if name == "" {
		return false
	}
	if name[0] < 'A' || name[0] > 'z' || (name[0] > 'Z' && name[0] < 'a') {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == ' ' {
			continue
		}
		return false
	}
	return true
}

// isRetryPhrase checks if a line contains retry/correction phrases
// that indicate tool call retries.
//
// STRUCTURAL APPROACH: Detects retry intent by semantic structure patterns:
// 1. First-person pronoun + action verb (fix/retry/redo/correct)
// 2. Action verb + object (call/format/issue)
// 3. Works for both Chinese and English through pattern matching
//
// NOTE: This function is currently used for TEST VALIDATION ONLY.
// Retry phrase filtering in removeClaudeCodePreamble is intentionally DISABLED
// to avoid over-filtering in streaming mode.
//
// Performance: Uses structural pattern detection instead of keyword lists.
func isRetryPhrase(line string) bool {
	// Check for bullet prefix and remove it for matching
	trimmed := line
	if strings.HasPrefix(trimmed, "●") || strings.HasPrefix(trimmed, "•") || strings.HasPrefix(trimmed, "‣") {
		trimmed = strings.TrimSpace(trimmed[len("●"):])
	}

	// Convert to lowercase for English matching
	lower := strings.ToLower(trimmed)

	// STRUCTURAL PATTERN 1: Chinese first-person + action structure
	// Pattern: [我/让我/我来/需要] + [修正/修复/重新/重试]
	if containsRetryStructureChinese(trimmed) {
		return true
	}

	// STRUCTURAL PATTERN 2: English first-person + action structure
	// Pattern: [I/Let me/I'll/I need to] + [fix/retry/redo/correct/recreate]
	if containsRetryStructureEnglish(lower) {
		return true
	}

	return false
}

// containsRetryStructureChinese detects Chinese retry patterns by structure.
// Pattern: subject marker + action verb for correction/retry
func containsRetryStructureChinese(text string) bool {
	// Subject markers that indicate self-action
	subjectMarkers := []string{"我", "让我", "我来", "需要"}
	// Action verbs for correction/retry
	actionVerbs := []string{"修正", "修复", "重新", "重试", "再次", "纠正"}

	for _, subject := range subjectMarkers {
		if strings.Contains(text, subject) {
			for _, action := range actionVerbs {
				if strings.Contains(text, action) {
					return true
				}
			}
		}
	}
	return false
}

// containsRetryStructureEnglish detects English retry patterns by structure.
// Pattern: first-person subject + action verb for correction/retry
func containsRetryStructureEnglish(lower string) bool {
	// First-person patterns (already lowercased)
	firstPersonPatterns := []string{
		"i need to ", "i'll ", "i will ", "let me ", "i should ",
		"i have to ", "i must ", "i'm going to ", "i am going to ",
	}
	// Action verbs for correction/retry
	actionVerbs := []string{
		"fix", "retry", "redo", "correct", "recreate", "regenerate",
		"rewrite", "revise", "update", "modify", "adjust", "repair",
	}

	for _, pattern := range firstPersonPatterns {
		if strings.Contains(lower, pattern) {
			for _, action := range actionVerbs {
				if strings.Contains(lower, action) {
					return true
				}
			}
		}
	}
	return false
}

// cleanConsecutiveBlankLines compresses consecutive blank lines into single blank lines.
// This is used after XML tag removal to clean up extra blank lines left behind.
// Performance: Uses regexp to replace multiple consecutive newlines with single newline.
func cleanConsecutiveBlankLines(text string) string {
	// Fast path: if no consecutive blank lines, return early
	if !strings.Contains(text, "\n\n\n") {
		return text
	}
	return reConsecutiveNewlines.ReplaceAllString(text, "\n\n")
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
// parseFunctionCallsXML attempts to parse standard XML-formatted function calls.
// It supports both <function_calls> blocks and flat <invoke> tags.
//
// Parsing Strategy:
// 1. We process the RAW text, including <thinking> blocks. This is CRITICAL for
//    reasoning models (like DeepSeek-R1, GLM-Thinking) which may embed tool calls
//    within or interleaved with thinking content. Previous versions removed thinking
//    blocks first, which caused tool calls inside them (especially unclosed ones) to be lost.
// 2. We look for a trigger signal if provided, as it anchors the parsing.
// 3. We fallback to scanning for <function_calls> or <invoke>.
func parseFunctionCallsXML(text, triggerSignal string) []functionCall {
	if text == "" {
		return nil
	}

	// Optimization: Fast path check if any XML-like content exists
	if !strings.Contains(text, "<") {
		return nil
	}

	// Prefer to anchor parsing near the trigger signal when the model
	// correctly follows the prompt convention. Fall back to the first
	// <function_calls> block if no trigger is found.
	start := 0
	hasTrigger := triggerSignal != "" && strings.Contains(text, triggerSignal)
	hasFunctionCalls := strings.Contains(text, "<function_calls>")

	// If a trigger signal is present, we start parsing AFTER the trigger.
	// This helps avoid parsing hallucinated examples that might appear before the actual call.
	if hasTrigger {
		if idx := strings.Index(text, triggerSignal); idx >= 0 {
			start = idx + len(triggerSignal)
		}
	} else if hasFunctionCalls {
		// If no trigger but has <function_calls>, start at the first block
		if idx := strings.Index(text, "<function_calls>"); idx >= 0 {
			start = idx
		}
	}

	// Extract the segment to parse based on start position
	segment := text
	if start > 0 && start < len(text) {
		segment = text[start:]
	}

	// Remove any orphaned trigger signals from the segment only if they exist
	// This is applied to segment, not the original 'text'
	if hasTrigger || strings.Contains(segment, "<Function_") || strings.Contains(segment, "<<CALL_") {
		segment = reTriggerSignal.ReplaceAllString(segment, "")
	}

	// Prefer the flat <invoke name="..."> format when present to reduce parsing overhead.
	// Use content check to fall back to text mode if there's non-tool content after </invoke>
	// (b4u2cc behavior: prevents treating explanatory text as tool calls).
	if flatMatches := reInvokeFlat.FindAllStringSubmatch(segment, -1); len(flatMatches) > 0 {
		// First try with content check (b4u2cc strict mode)
		flatCalls := parseFlatInvokesWithContentCheck(segment, flatMatches)
		if flatCalls == nil {
			// Content check failed, try without content check as fallback
			// This handles cases where the model outputs valid tool calls with trailing content
			// that should still be processed (e.g., thinking content after tool call)
			flatCalls = parseFlatInvokes(flatMatches)
		}
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

	// Try parsing <invoke name="Tool<arg_key>param</arg_key><arg_value>value</arg_value> format
	// This handles cases where thinking models output tool calls with arg_key/arg_value pairs
	if argKeyValueCalls := parseInvokeArgKeyValue(segment); len(argKeyValueCalls) > 0 {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithField("parsed_count", len(argKeyValueCalls)).Debug("Function call parsing: parsed invoke arg_key/arg_value format")
		}
		return argKeyValueCalls
	}

	// Try parsing unclosed <invoke name="..."> format (handles truncated output)
	// This helps when models stop abruptly before outputting </invoke>
	// NOTE: This must come AFTER specialized formats like parseInvokeArgKeyValue to avoid misinterpretation
	if unclosedCalls := parseUnclosedInvokes(segment); len(unclosedCalls) > 0 {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithField("parsed_count", len(unclosedCalls)).Debug("Function call parsing: parsed unclosed invoke format")
		}
		return unclosedCalls
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

	// Final fuzzy fallback: if still nothing, try parsing any unclosed <invoke> in the callsContent
	if len(results) == 0 && strings.Contains(callsContent, "<invoke") {
		if unclosed := parseUnclosedInvokes(callsContent); len(unclosed) > 0 {
			results = append(results, unclosed...)
			logrus.WithField("parsed_count", len(unclosed)).Debug("Function call parsing: extracted via unclosed invoke fallback in callsContent")
		}
	}

	if len(results) == 0 {
		return nil
	}
	return results
}

// parseFlatInvokes parses flat <invoke name="...">...</invoke> format tool calls.
// Following b4u2cc reference implementation, only the FIRST valid tool call is returned.
// This prevents issues when models output multiple tool calls in a single response,
// which can cause confusion and errors in multi-turn conversations.
// Subsequent tool calls are logged and filtered out.
func parseFlatInvokes(matches [][]string) []functionCall {
	if len(matches) == 0 {
		return nil
	}
	// Only return the first valid tool call (b4u2cc behavior)
	// This ensures one tool call per response for cleaner multi-turn conversations
	for i, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		args := extractParameters(m[2], reMcpParam, reGenericParam)

		// Log if there are additional tool calls being filtered
		if i < len(matches)-1 {
			filteredCount := 0
			for j := i + 1; j < len(matches); j++ {
				if len(matches[j]) >= 3 && strings.TrimSpace(matches[j][1]) != "" {
					filteredCount++
				}
			}
			if filteredCount > 0 {
				logrus.WithFields(logrus.Fields{
					"first_tool":     name,
					"filtered_count": filteredCount,
				}).Debug("Function call parsing: filtered subsequent tool calls (b4u2cc single-call policy)")
			}
		}

		return []functionCall{{Name: name, Args: args}}
	}
	return nil
}

// parseFlatInvokesWithContentCheck parses flat <invoke> format and checks for content after </invoke>.
// Following b4u2cc reference implementation: if there is non-whitespace content after </invoke>
// that is NOT another <invoke> tag or closing tag, it falls back to text mode (returns nil).
// This prevents issues when models output explanatory text after tool calls.
// Parameters:
//   - segment: the full text segment containing potential invoke tags
//   - matches: regex matches from reInvokeFlat
//
// Returns nil if content after </invoke> indicates this should be treated as text.
func parseFlatInvokesWithContentCheck(segment string, matches [][]string) []functionCall {
	if len(matches) == 0 {
		return nil
	}

	// Find the first valid match
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}

		// Find the position of this invoke block in the segment
		fullMatch := m[0] // The full matched string including <invoke>...</invoke>
		matchIdx := strings.Index(segment, fullMatch)
		if matchIdx == -1 {
			continue
		}

		// Check content after </invoke>
		afterInvoke := segment[matchIdx+len(fullMatch):]
		afterTrimmed := strings.TrimSpace(afterInvoke)

		// If there's non-whitespace content after </invoke>, check if it's acceptable
		// Acceptable patterns:
		// 1. Empty or whitespace only
		// 2. Another <invoke> tag
		// 3. Closing tags like </invoke>, </function_calls>, </function_call>
		// 4. Orphaned closing tags (model sometimes outputs extra </invoke>)
		// 5. Malformed closing tags like </antlinvoke>, </antml>, </antinvoke> (model typos)
		if afterTrimmed != "" {
			afterLower := strings.ToLower(afterTrimmed)
			isAcceptable := strings.HasPrefix(afterLower, "<invoke") ||
				strings.HasPrefix(afterLower, "</invoke>") ||
				strings.HasPrefix(afterLower, "</function") ||
				strings.HasPrefix(afterLower, "</tool") ||
				strings.HasPrefix(afterLower, "</ant") || // Handles </antlinvoke>, </antml>, </antinvoke>
				strings.HasPrefix(afterLower, "</antml")

			if !isAcceptable {
				logrus.WithFields(logrus.Fields{
					"tool_name":     name,
					"after_content": utils.TruncateString(afterTrimmed, 100),
				}).Debug("Function call parsing: non-tool content after </invoke>, falling back to text mode (b4u2cc behavior)")
				return nil
			}
		}

		// Parse and return the first valid tool call
		args := extractParameters(m[2], reMcpParam, reGenericParam)
		return []functionCall{{Name: name, Args: args}}
	}
	return nil
}

// parseUnclosedInvokes parses unclosed <invoke name="..."> formats (truncated output).
// It captures the name and all remaining content as the parameter source.
// Uses precompiled reUnclosedInvoke regex at package level for performance.
func parseUnclosedInvokes(text string) []functionCall {
	matches := reUnclosedInvoke.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}

	results := make([]functionCall, 0, len(matches))
	for i, mIdx := range matches {
		name := strings.TrimSpace(text[mIdx[2]:mIdx[3]])
		if name == "" {
			continue
		}

		// Content starts from end of this tag
		start := mIdx[1]
		// and ends at start of next tag or end of text
		end := len(text)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}

		content := text[start:end]
		// If it contains </invoke>, then it actually matched a closed one (or partial)
		// we strip the closer if it exists to clean up.
		if idx := strings.Index(content, "</invoke>"); idx != -1 {
			content = content[:idx]
		}

		args := extractParameters(content, reMcpParam, reGenericParam)
		results = append(results, functionCall{Name: name, Args: args})
	}

	// Follow b4u2cc policy: only return the first one if multiple found in this mode
	if len(results) > 1 {
		logrus.WithField("filtered_count", len(results)-1).Debug("parseUnclosedInvokes: filtered subsequent unclosed tool calls")
		return results[:1]
	}
	return results
}

// parseMalformedInvokes parses malformed <invokename="..."> format tool calls.
// This handles cases where models output <><invokename="TodoWrite"><parametername="todos">[...]
// instead of the correct <invoke name="TodoWrite"><parameter name="todos">[...]</parameter></invoke>
// Following b4u2cc reference implementation, only the FIRST valid tool call is returned.
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

	// Only return the first valid tool call (b4u2cc single-call policy)
	for i, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}

		// Parse malformed parameter tags from the remaining content
		args := extractMalformedParameters(m[2])

		// Log if there are additional tool calls being filtered
		if i < len(matches)-1 {
			filteredCount := len(matches) - i - 1
			if filteredCount > 0 {
				logrus.WithFields(logrus.Fields{
					"first_tool":     name,
					"filtered_count": filteredCount,
				}).Debug("Function call parsing: filtered subsequent malformed tool calls (b4u2cc single-call policy)")
			}
		}

		return []functionCall{{Name: name, Args: args}}
	}
	return nil
}

// parseInvokeArgKeyValue parses <invoke name="Tool<arg_key>param</arg_key><arg_value>value</arg_value> format.
// This handles cases where thinking models output tool calls with arg_key/arg_value pairs instead of
// standard <parameter name="...">...</parameter> format.
// Example: <invoke name="Bash<arg_key>command</arg_key><arg_value>ls -la</arg_value><arg_key>description</arg_key><arg_value>list files</arg_value>
// Following b4u2cc reference implementation, only the FIRST valid tool call is returned.
// Performance: Uses strings.Contains for fast pre-check before regex matching.
func parseInvokeArgKeyValue(text string) []functionCall {
	// Fast path: check if arg_key pattern exists
	if !strings.Contains(text, "<arg_key>") {
		return nil
	}

	matches := reInvokeArgKeyValue.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	// Only return the first valid tool call (b4u2cc single-call policy)
	for i, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}

		// Parse arg_key/arg_value pairs from the remaining content
		args := extractArgKeyValuePairs(m[2])

		// Log if there are additional tool calls being filtered
		if i < len(matches)-1 {
			filteredCount := len(matches) - i - 1
			if filteredCount > 0 {
				logrus.WithFields(logrus.Fields{
					"first_tool":     name,
					"filtered_count": filteredCount,
				}).Debug("Function call parsing: filtered subsequent arg_key/arg_value tool calls (b4u2cc single-call policy)")
			}
		}

		if len(args) > 0 {
			return []functionCall{{Name: name, Args: args}}
		}
	}
	return nil
}

// extractArgKeyValuePairs parses <arg_key>name</arg_key><arg_value>value</arg_value> pairs.
// This handles the thinking model output format where parameters are specified as key-value pairs.
// Example: "<arg_key>command</arg_key><arg_value>ls -la</arg_value><arg_key>description</arg_key><arg_value>list files</arg_value>"
// Returns a map of parameter names to values.
func extractArgKeyValuePairs(content string) map[string]any {
	args := make(map[string]any)
	if content == "" {
		return args
	}

	// Find all arg_key/arg_value pairs
	pairMatches := reArgKeyValuePair.FindAllStringSubmatch(content, -1)
	for _, pm := range pairMatches {
		if len(pm) < 3 {
			continue
		}
		key := strings.TrimSpace(pm[1])
		value := strings.TrimSpace(pm[2])
		if key == "" {
			continue
		}

		// Try to parse the value as JSON (for arrays/objects)
		if len(value) > 0 {
			firstChar := value[0]
			if firstChar == '[' || firstChar == '{' {
				if jsonVal, ok := tryParseJSON(value); ok {
					args[key] = jsonVal
					continue
				}
			}
		}

		// Store as string if not valid JSON
		args[key] = value
	}

	return args
}

// extractMalformedParameters parses malformed <parametername="..."> format parameters.
// This handles cases where models output <parametername="todos">[...] instead of
// <parameter name="todos">[...]</parameter>
// Also handles cases without closing tags and partially malformed JSON.
// ENHANCED: Also handles <propertyname="..."value="..."> format from production logs.
// Performance: Uses strings.Contains for fast pre-check before regex matching.
// Robustness: Attempts to fix common JSON formatting issues before parsing.
func extractMalformedParameters(content string) map[string]any {
	args := make(map[string]any)
	if content == "" {
		return args
	}

	// Check for malformed property tags first (new format from production logs)
	// Pattern: <propertyname="activeForm"value="正在分析"> or <property name="id"value="2">
	if strings.Contains(content, "<property") && strings.Contains(content, `value="`) {
		propMatches := reMalformedProperty.FindAllStringSubmatch(content, -1)
		for _, pm := range propMatches {
			if len(pm) >= 3 {
				propName := strings.TrimSpace(pm[1])
				propValue := strings.TrimSpace(pm[2])
				if propName != "" {
					args[propName] = propValue
				}
			}
		}
		// If we found property tags, return the args
		if len(args) > 0 {
			return args
		}
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
// STRUCTURAL APPROACH: Fixes JSON based on structural patterns, not specific field names.
// Known structural issues:
// 1. Missing commas between array elements or object items: }{ -> },{
// 2. Unbalanced braces/brackets: add missing closing brackets
// 3. Missing field names after comma: ,": " -> ,"_": " (placeholder field)
// 4. Truncated field names: fieldName": -> "fieldName":
// 5. Missing quotes around values: "field":value -> "field":"value"
// 6. Trailing commas: ,} or ,] -> } or ]
// 7. Severely malformed arrays: [": -> attempt to extract content or return []
// Returns a potentially repaired JSON string (best effort).
// Performance: Uses precompiled regex patterns where possible.
func repairMalformedJSON(s string) string {
	// Work on a copy
	result := s

	// Replace newlines with spaces to prevent "invalid character '\n' in string literal" errors
	// This is safe because JSON allows whitespace between tokens, and we want to preserve
	// the content even if it loses formatting.
	result = strings.ReplaceAll(result, "\n", " ")
	result = strings.ReplaceAll(result, "\r", " ")

	// STRUCTURAL FIX 0: Handle severely malformed JSON arrays starting with ":
	// Pattern: [": "text",Form":... - try to extract content from malformed structure
	// These patterns occur when model outputs truncated field names
	if strings.HasPrefix(result, `[":`) || strings.HasPrefix(result, `["":`) {
		// Try to extract meaningful content from the malformed JSON
		// Pattern: [": "content text", ...] - extract the first quoted string as content
		extracted := extractContentFromMalformedArray(result)
		if extracted != "" {
			return extracted
		}
		return "[]"
	}

	// STRUCTURAL FIX 0.3: Fix empty id values first (before other fixes)
	// Pattern: "id":"", -> "id":"1", (generate sequential id)
	if strings.Contains(result, `"id":""`) {
		idCounter := 1
		for strings.Contains(result, `"id":""`) {
			result = strings.Replace(result, `"id":""`, fmt.Sprintf(`"id":"%d"`, idCounter), 1)
			idCounter++
		}
	}
	// Also handle with spaces: "id": "",
	result = reJSONEmptyIdValue.ReplaceAllStringFunc(result, func(match string) string {
		return `"id":"1",`
	})

	// STRUCTURAL FIX 0.5: Fix empty field names first (before other fixes)
	// Pattern: "": "value" -> "content": "value"
	if strings.Contains(result, `"":`) {
		result = reJSONEmptyFieldName.ReplaceAllString(result, `"content": "`)
	}

	// STRUCTURAL FIX 1: Fix missing quotes around field values
	// Pattern: "field":unquotedValue -> "field":"unquotedValue"
	result = fixUnquotedFieldValues(result)

	// STRUCTURAL FIX 2: Fix truncated field names (missing opening quote)
	// Pattern: ,fieldName": -> ,"fieldName":
	result = fixTruncatedFieldNames(result)

	// STRUCTURAL FIX 2.5: Fix truncated field chain pattern from production log
	// Pattern: ",": " -> ","content": " (infer content field for consecutive truncated fields)
	if strings.Contains(result, `",":`) || strings.Contains(result, `", ":`) {
		result = reJSONTruncatedFieldChain.ReplaceAllString(result, `","content": "`)
	}

	// STRUCTURAL FIX 3: Add missing commas between objects in arrays
	// Pattern: }{ -> },{
	result = reJSONMissingComma.ReplaceAllString(result, `},{`)

	// STRUCTURAL FIX 4: Remove extra quotes before field names
	// Pattern: \"", -> ," (extra quote after comma)
	result = reJSONExtraQuote.ReplaceAllString(result, `,"`)

	// STRUCTURAL FIX 5: Remove trailing commas before } or ]
	// Pattern: ,} -> } or ,] -> ]
	result = reJSONTrailingComma.ReplaceAllStringFunc(result, func(match string) string {
		return string(match[len(match)-1])
	})

	// STRUCTURAL FIX 6: Fix missing quotes around string values
	// Pattern: :"unquotedWord" -> :"quotedWord"
	result = reJSONMissingQuotes.ReplaceAllString(result, `: "$1"$2`)

	// STRUCTURAL FIX 7: Fix missing field name after comma (structural pattern)
	// Pattern: ,": " or , ": " -> ,"_field": " (use placeholder field name)
	// This handles cases where model outputs: {"id": "1",": "value"
	result = fixMissingFieldName(result)

	// STRUCTURAL FIX 8: Fix colon-only separator (no field name at all)
	// Pattern: {:"value" or ,:"value" -> {"content":"value" or ,"content":"value"
	if strings.Contains(result, `{:`) || strings.Contains(result, `,:`) {
		result = reJSONMalformedColonOnly.ReplaceAllString(result, `$1"content": "$2"`)
	}

	// STRUCTURAL FIX 9: Fix common field name typos (state->status, Form->activeForm)
	result = fixMalformedFieldNames(result)

	// STRUCTURAL FIX 10: Fix missing opening quotes for field values
	// Pattern: "activeForm":正在搜索" -> "activeForm":"正在搜索"
	// NOTE: The regex now excludes space/quote as first char, so it won't match already-quoted values
	// We still check for the field name to avoid unnecessary regex operations
	if strings.Contains(result, `"activeForm":`) {
		result = reJSONMalformedActiveFormChinese.ReplaceAllString(result, `"activeForm":"$1"`)
	}
	// Pattern: "content":使用exa工具" -> "content":"使用exa工具"
	if strings.Contains(result, `"content":`) {
		result = reJSONMalformedContentChinese.ReplaceAllString(result, `"content":"$1"`)
	}
	// Pattern: "status":pending" -> "status":"pending"
	if strings.Contains(result, `"status":`) {
		result = reJSONMalformedStatusChinese.ReplaceAllString(result, `"status":"$1"`)
	}
	// Pattern: "id":task-1" -> "id":"task-1"
	if strings.Contains(result, `"id":`) {
		result = reJSONMalformedIdChinese.ReplaceAllString(result, `"id":"$1"`)
	}
	// Pattern: "priority":high" -> "priority":"high"
	if strings.Contains(result, `"priority":`) {
		result = reJSONMalformedPriorityChinese.ReplaceAllString(result, `"priority":"$1"`)
	}

	// STRUCTURAL FIX 11: Fix empty status values
	// Pattern: "status":""} -> "status":"pending"}
	if strings.Contains(result, `"status":""`) {
		result = reJSONMalformedStatusEmpty.ReplaceAllString(result, `"status":"pending"}`)
	}

	// --- Fixes for production log malformed JSON ---
	// --- Fixes for production log malformed JSON ---
	// Remove strings.Contains checks to ensure regexes run (they are fast enough)
	result = reJSONMalformedIdStart.ReplaceAllString(result, `[{"id":`)
	result = reJSONMalformedStatusMerged.ReplaceAllString(result, `$1", "status":`)
	result = reJSONMalformedIdShort.ReplaceAllString(result, `"id": "`)

	// Fix content text between objects: },text", -> },{"content":"text",
	result = reJSONMalformedContentText.ReplaceAllString(result, `},{"content": "$1",`)

	// Fix missing object start: }, "id": -> }, {"id":
	result = reJSONMalformedMissingObjectStart.ReplaceAllString(result, `}, {"id":`)

	// Fix status ending with bracket: "status":] -> "status": "pending"}]
	// We need to close the object before closing the array
	result = reJSONMalformedStatusBracket.ReplaceAllString(result, `"status": "pending"}]`)

	// STRUCTURAL FIX 12: Balance braces/brackets - add missing closing brackets
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

// extractContentFromMalformedArray attempts to extract meaningful content from
// severely malformed JSON arrays that start with [": or ["":
// Pattern: [": "检查当前目录",Form":检查...",": "in_progress"]
// The actual content is BETWEEN quotes, not inside them (malformed JSON structure).
// This extracts text segments and creates a valid todo item.
// Returns a valid JSON array string or empty string if extraction fails.
func extractContentFromMalformedArray(s string) string {
	// The malformed pattern is: [": "content",field":content",": "status"]
	// Where content appears AFTER a quote followed by non-quote characters
	// Strategy: Find all text segments that look like content (Chinese text, long strings)

	var contents []string
	runes := []rune(s)
	var current strings.Builder

	for i := 0; i < len(runes); i++ {
		c := runes[i]
		// Skip structural characters
		if c == '[' || c == ']' || c == '{' || c == '}' || c == '"' || c == ':' || c == ',' {
			// Save current segment if meaningful
			segment := strings.TrimSpace(current.String())
			if len([]rune(segment)) >= 4 && !isLikelyFieldValue(segment) && !isLikelyFieldName(segment) && !isPunctuationOnly(segment) {
				contents = append(contents, segment)
			}
			current.Reset()
			continue
		}
		current.WriteRune(c)
	}

	// Check last segment
	segment := strings.TrimSpace(current.String())
	if len([]rune(segment)) >= 4 && !isLikelyFieldValue(segment) && !isLikelyFieldName(segment) && !isPunctuationOnly(segment) {
		contents = append(contents, segment)
	}

	// If we found meaningful content, create a valid todo item
	if len(contents) > 0 {
		// Use the first meaningful content as the task content
		todoContent := contents[0]
		// Escape quotes in content for JSON
		todoContent = strings.ReplaceAll(todoContent, `"`, `\"`)
		return fmt.Sprintf(`[{"id":"1","content":"%s","status":"pending","priority":"medium"}]`, todoContent)
	}

	return ""
}

// isLikelyFieldName checks if a string looks like a truncated field name
func isLikelyFieldName(s string) bool {
	lower := strings.ToLower(s)
	// Common truncated field names from malformed JSON
	fieldNames := []string{
		"form", "activeform", "status", "id", "content", "priority",
		"in_progress", "pending", "completed",
	}
	for _, fn := range fieldNames {
		if lower == fn || strings.HasPrefix(lower, fn) {
			return true
		}
	}
	return false
}

// isPunctuationOnly checks if a string contains only punctuation and whitespace
// or starts with punctuation (likely a malformed field separator)
func isPunctuationOnly(s string) bool {
	if len(s) == 0 {
		return true
	}
	// Check if starts with punctuation (likely malformed field separator like ",Form")
	firstRune := []rune(s)[0]
	if firstRune == ',' || firstRune == ':' || firstRune == ';' {
		return true
	}
	// Check if all characters are punctuation or whitespace
	for _, r := range s {
		if r != ':' && r != ',' && r != ';' && r != '.' && r != ' ' && r != '\t' && r != '\n' {
			return false
		}
	}
	return true
}

// isLikelyFieldValue checks if a string is likely a field value (status, priority, etc.)
// rather than actual content. Used to filter out non-content strings.
func isLikelyFieldValue(s string) bool {
	lower := strings.ToLower(s)
	// Common field values that should be filtered
	fieldValues := []string{
		"pending", "in_progress", "completed",
		"high", "medium", "low",
		"true", "false",
	}
	for _, fv := range fieldValues {
		if lower == fv {
			return true
		}
	}
	// Check if it looks like a truncated field name (ends with ":")
	if strings.HasSuffix(s, ":") {
		return true
	}
	return false
}

// fixMissingFieldName fixes JSON patterns where field name is missing after comma.
// Pattern: ,": " or , ": " -> ,"content": " (infer content field for TodoWrite context)
// This is a structural fix that infers the most likely field name based on context.
// ENHANCED: Also handles numeric values followed by missing field name (e.g., 1,": ")
func fixMissingFieldName(s string) string {
	// Pattern 1: ,": " (comma, colon, space, quote) - likely missing "content" field
	// Handles: {"id":"1",": "content value"} -> {"id":"1","content": "content value"}
	s = strings.ReplaceAll(s, `",": "`, `","content": "`)
	s = strings.ReplaceAll(s, `", ": "`, `", "content": "`)
	// Pattern 2: ,":"  (comma, colon, quote without space)
	s = strings.ReplaceAll(s, `",":"`, `","content":"`)

	// Pattern 3: Numeric value followed by missing field name
	// Handles: {"id":1,": "content value"} -> {"id":1,"content": "content value"}
	// Use regex to match digit followed by ,": " pattern
	s = reJSONNumericMissingField.ReplaceAllString(s, `$1,"content": "`)

	return s
}

// fixMalformedFieldNames fixes common field name typos in TodoWrite JSON.
// This handles cases where models output wrong field names:
// - "state" -> "status" (common typo)
// - "Form" -> "activeForm" (capitalization issue)
// - "priority":medium -> "priority":"medium" (unquoted value)
// - "status":pending -> "status":"pending" (unquoted value)
func fixMalformedFieldNames(s string) string {
	// Fix "state" -> "status" (common typo in TodoWrite)
	s = reJSONMalformedState.ReplaceAllString(s, `"status":"$1"`)
	// Fix "Form" -> "activeForm" (capitalization issue)
	// FIXED: Remove space after colon to maintain consistent formatting
	s = reJSONMalformedForm.ReplaceAllString(s, `"activeForm":"`)
	// Fix unquoted priority values
	s = reJSONMalformedPriority.ReplaceAllString(s, `"priority":"$1"$2`)
	// Fix unquoted status values
	s = reJSONMalformedStatusUnquoted.ReplaceAllString(s, `"status":"$1"$2`)
	return s
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
// Pattern 1: "content":WebSearch -> "content":"WebSearch" (completely unquoted)
// Pattern 2: "content":正在搜索" -> "content":"正在搜索" (missing opening quote only)
// This handles cases where the model outputs unquoted or partially quoted string values.
// Performance: O(n) single pass through the string.
func fixUnquotedFieldValues(s string) string {
	// Fast path: if no colon, no fix needed
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
					(c >= '0' && c <= '9') || c == '-' || c == ']' || c == '}' {
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

				// Read until comma, closing bracket/brace, quote, or end of line
				valueStart := i
				hasTrailingQuote := false
				for i < len(s) {
					c := s[i]
					if c == ',' || c == '}' || c == ']' || c == '\n' || c == '\r' {
						break
					}
					// Check for trailing quote (missing opening quote case)
					if c == '"' {
						hasTrailingQuote = true
						i++
						break
					}
					i++
				}

				// Write the value
				value := s[valueStart:i]
				if hasTrailingQuote && len(value) > 0 && value[len(value)-1] == '"' {
					// Remove trailing quote from value since we need to write it separately
					value = value[:len(value)-1]
				}
				result.WriteString(strings.TrimSpace(value))
				result.WriteByte('"') // Always add closing quote
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
	// ENHANCED: Also handles [content": pattern (array start)
	if strings.Contains(result, `content":`) && !strings.Contains(result, `"content":`) {
		result = strings.ReplaceAll(result, `,content":`, `,"content":`)
		result = strings.ReplaceAll(result, `{content":`, `{"content":`)
		result = strings.ReplaceAll(result, `[content":`, `[{"content":`)
	}

	// Fix truncated activeForm field (without Form prefix)
	// Pattern: ,activeForm": -> ,"activeForm":
	if strings.Contains(result, `activeForm":`) && !strings.Contains(result, `"activeForm":`) {
		result = strings.ReplaceAll(result, `,activeForm":`, `,"activeForm":`)
		result = strings.ReplaceAll(result, `{activeForm":`, `{"activeForm":`)
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

	// Fix truncated task field
	// Pattern: ,task": -> ,"task":
	if strings.Contains(result, `task":`) && !strings.Contains(result, `"task":`) {
		result = strings.ReplaceAll(result, `,task":`, `,"task":`)
		result = strings.ReplaceAll(result, `{task":`, `{"task":`)
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
		// First try direct parsing
		if err := json.Unmarshal([]byte(trimmed), &jsonVal); err == nil {
			// If it's already a map, return directly
			if mapVal, ok := jsonVal.(map[string]any); ok {
				return mapVal
			}
			// For arrays, primitives (string, number, bool, null), wrap under "value" key
			// so the caller still gets a map structure with the parsed content.
			return map[string]any{"value": jsonVal}
		}
		// If direct parsing fails, try repairing malformed JSON first
		// This handles cases like {"id": "1",": "content"} (missing field name)
		repaired := repairMalformedJSON(trimmed)
		if err := json.Unmarshal([]byte(repaired), &jsonVal); err == nil {
			if mapVal, ok := jsonVal.(map[string]any); ok {
				return mapVal
			}
			return map[string]any{"value": jsonVal}
		}
		// If JSON parsing still fails (e.g. due to unescaped characters), fall back to regex parsing.
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
	// or <parameter name="p1">v1 <parameter name="p2">v2
	// Uses positional splitting to find multiple unclosed parameters.
	if trimmed != "" {
		// 1. Try matching multiple <parameter name="..."> patterns
		// SUPPORT: both single and double quotes for name attribute
		mcpOpenRe := regexp.MustCompile(`<(?:parameter|param)\s+name=['"]([^'"]+)['"][^>]*>`)
		openMatches := mcpOpenRe.FindAllStringSubmatchIndex(content, -1)
		if len(openMatches) > 0 {
			for i, mIdx := range openMatches {
				name := strings.TrimSpace(content[mIdx[2]:mIdx[3]])
				if name == "" || reservedTags[strings.ToLower(name)] {
					continue
				}
				if _, exists := args[name]; exists {
					continue
				}

				start := mIdx[1]
				end := len(content)
				if i+1 < len(openMatches) {
					end = openMatches[i+1][0]
				}

				valStr := content[start:end]
				// Strip closing tag or partial closing fragments
				if closeIdx := strings.Index(valStr, "</"); closeIdx != -1 {
					valStr = valStr[:closeIdx]
				}
				// Also strip trailing > or /> or quotes that might be leftover from malformed input
				valStr = strings.TrimRight(valStr, ">/ \"")
				args[name] = parseValueOrString(strings.TrimSpace(valStr))
			}
		}

		// 2. Try generic <tag> patterns for those not yet found
		genericOpenRe := regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)>`)
		genericMatches := genericOpenRe.FindAllStringSubmatchIndex(content, -1)
		for i, mIdx := range genericMatches {
			name := strings.TrimSpace(content[mIdx[2]:mIdx[3]])
			if name == "" || reservedTags[strings.ToLower(name)] {
				continue
			}
			if _, exists := args[name]; exists {
				continue
			}

			start := mIdx[1]
			end := len(content)
			if i+1 < len(genericMatches) {
				end = genericMatches[i+1][0]
			}

			valStr := content[start:end]
			if closeIdx := strings.Index(valStr, "</"); closeIdx != -1 {
				valStr = valStr[:closeIdx]
			}
			valStr = strings.TrimRight(valStr, ">/ \"")
			args[name] = parseValueOrString(strings.TrimSpace(valStr))
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
// Also attempts to repair malformed JSON before parsing.
func parseValueOrString(s string) any {
	// Sanitize special tokens first
	s = sanitizeModelTokens(s)

	var val any
	// First try direct parsing
	if err := json.Unmarshal([]byte(s), &val); err == nil {
		// Recursively sanitize string values within parsed JSON
		return sanitizeJsonValue(val)
	}

	// If direct parsing fails and it looks like JSON, try repairing
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 0 {
		firstChar := trimmed[0]
		if firstChar == '{' || firstChar == '[' {
			repaired := repairMalformedJSON(trimmed)
			if err := json.Unmarshal([]byte(repaired), &val); err == nil {
				return sanitizeJsonValue(val)
			}
		}
	}

	return s
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

// extractToolCallsFromEmbeddedJSON extracts tool calls from embedded JSON structures in content.
// This handles cases where thinking models output tool call info in JSON format instead of XML.
// The function looks for JSON objects containing tool call patterns like:
//   - {"name":"Read","file_path":"..."}
//   - {"name":"WebSearch","query":"..."}
//   - {"id":"call_xxx","name":"Bash","command":"..."}
//   - Double-escaped JSON: {\"name\":\"Read\",\"file_path\":\"...\"}
//
// This is a fallback strategy for when XML parsing fails but the content contains
// recognizable tool call structures in JSON format.
//
// Performance: Uses structural detection to identify JSON tool call patterns.
// Only processes content that contains JSON-like structures.
//
// AI Review Note (2026-01-03): This function is called from cc_support.go as a fallback
// when parseFunctionCallsXML returns no results. It is NOT called directly from
// parseFunctionCallsXML to maintain separation of concerns. If JSON-only tool calls
// become more common, consider integrating this as a fallback within parseFunctionCallsXML.
func extractToolCallsFromEmbeddedJSON(content string) []functionCall {
	if content == "" {
		return nil
	}

	// Fast path: check if content contains JSON-like structures with tool indicators
	// Look for patterns like "name":"ToolName" or escaped variants
	hasNameField := strings.Contains(content, `"name"`) ||
		strings.Contains(content, `\"name\"`) ||
		strings.Contains(content, `\\"name\\"`)
	if !hasNameField {
		return nil
	}

	// Known tool names that we should extract (structural detection)
	knownTools := map[string]bool{
		"Read": true, "Write": true, "Edit": true, "Bash": true,
		"Glob": true, "Grep": true, "WebSearch": true, "WebFetch": true,
		"TodoWrite": true, "AskUserQuestion": true, "Task": true,
		"TaskOutput": true, "NotebookEdit": true, "Skill": true,
		"EnterPlanMode": true, "ExitPlanMode": true, "KillShell": true,
	}

	var calls []functionCall

	// Try multiple extraction strategies for different escape levels
	// Strategy 1: Direct JSON (no escaping)
	calls = extractToolCallsFromJSONContent(content, knownTools)
	if len(calls) > 0 {
		return limitToFirstCall(calls)
	}

	// Strategy 2: Single-escaped JSON (\" -> ")
	unescaped := strings.ReplaceAll(content, `\"`, `"`)
	unescaped = strings.ReplaceAll(unescaped, `\\`, `\`)
	calls = extractToolCallsFromJSONContent(unescaped, knownTools)
	if len(calls) > 0 {
		return limitToFirstCall(calls)
	}

	// Strategy 3: Double-escaped JSON (\\\" -> ")
	doubleUnescaped := unescapeJSONString(content)
	if doubleUnescaped != content {
		calls = extractToolCallsFromJSONContent(doubleUnescaped, knownTools)
		if len(calls) > 0 {
			return limitToFirstCall(calls)
		}
	}

	return nil
}

// extractToolCallsFromJSONContent extracts tool calls from content that may contain JSON objects.
// This is a helper function that handles the actual JSON extraction logic.
// Uses precompiled reToolNameInJSON regex at package level for performance.
func extractToolCallsFromJSONContent(content string, knownTools map[string]bool) []functionCall {
	matches := reToolNameInJSON.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}

	var calls []functionCall
	processedRanges := make([][2]int, 0) // Track processed JSON ranges to avoid duplicates

	for _, matchIdx := range matches {
		if len(matchIdx) < 4 {
			continue
		}

		// Extract tool name from capture group
		toolName := strings.TrimSpace(content[matchIdx[2]:matchIdx[3]])
		if toolName == "" {
			continue
		}

		// Only process known tool names to avoid false positives
		if !knownTools[toolName] {
			continue
		}

		nameFieldStart := matchIdx[0]

		// Find the start of the JSON object by counting braces backward
		// When going backward: } increases depth, { decreases depth
		startIdx := -1
		depth := 0
		for i := nameFieldStart - 1; i >= 0; i-- {
			c := content[i]
			if c == '}' {
				depth++
			} else if c == '{' {
				if depth == 0 {
					startIdx = i
					break
				}
				depth--
			}
		}

		if startIdx == -1 {
			continue
		}

		// Find the end of the JSON object by counting braces forward
		endIdx := -1
		depth = 0
		for i := startIdx; i < len(content); i++ {
			c := content[i]
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					endIdx = i + 1
					break
				}
			}
		}

		if endIdx == -1 || endIdx <= startIdx {
			continue
		}

		// Check if this range overlaps with already processed ranges
		overlaps := false
		for _, r := range processedRanges {
			if (startIdx >= r[0] && startIdx < r[1]) || (endIdx > r[0] && endIdx <= r[1]) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}

		jsonStr := content[startIdx:endIdx]

		// Try to parse the JSON object
		var obj map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
			// Try to repair common JSON issues
			repairedJSON := repairMalformedJSON(jsonStr)
			if err := json.Unmarshal([]byte(repairedJSON), &obj); err != nil {
				logrus.WithFields(logrus.Fields{
					"tool_name":    toolName,
					"json_preview": utils.TruncateString(jsonStr, 200),
					"error":        err.Error(),
				}).Debug("CC+FC: Failed to parse embedded JSON tool call")
				continue
			}
		}

		// Verify the parsed object actually contains the expected tool name
		if parsedName, ok := obj["name"].(string); !ok || parsedName != toolName {
			continue
		}

		// Skip tool call results - these contain execution results, not new tool call requests
		// Tool call results have fields like: is_error, status, result, duration, display_result
		// Structural detection: if object has "is_error" field (true or false), it's a result not a request
		// The presence of is_error field indicates this is a tool execution result, regardless of value
		if _, hasIsError := obj["is_error"]; hasIsError {
			logrus.WithFields(logrus.Fields{
				"tool_name": toolName,
			}).Debug("CC+FC: Skipping tool call result (has 'is_error' field)")
			continue
		}
		if result, hasResult := obj["result"]; hasResult {
			// Only skip if result is non-empty (empty result might be a pending request)
			if resultStr, ok := result.(string); ok && resultStr != "" {
				logrus.WithFields(logrus.Fields{
					"tool_name": toolName,
				}).Debug("CC+FC: Skipping tool call result (has non-empty 'result' field)")
				continue
			}
		}
		// Check status field - "error" or "completed" indicates a result, not a new request
		// Tool call results have status field set by the execution system
		if status, ok := obj["status"].(string); ok {
			if status == "error" || status == "completed" {
				logrus.WithFields(logrus.Fields{
					"tool_name": toolName,
					"status":    status,
				}).Debug("CC+FC: Skipping tool call result (has status field)")
				continue
			}
		}

		// Extract tool arguments from the JSON object
		args := make(map[string]any)
		for k, v := range obj {
			// Skip metadata fields that are not tool arguments
			if k == "name" || k == "id" || k == "status" || k == "is_error" ||
				k == "duration" || k == "display_result" || k == "result" ||
				k == "mcp_server" || k == "type" {
				continue
			}
			args[k] = v
		}

		// Only add if we have arguments
		if len(args) > 0 {
			calls = append(calls, functionCall{Name: toolName, Args: args})
			processedRanges = append(processedRanges, [2]int{startIdx, endIdx})
			logrus.WithFields(logrus.Fields{
				"tool_name":  toolName,
				"args_count": len(args),
			}).Debug("CC+FC: Extracted tool call from embedded JSON")
		}
	}

	return calls
}

// limitToFirstCall returns only the first tool call (b4u2cc single-call policy)
func limitToFirstCall(calls []functionCall) []functionCall {
	if len(calls) == 0 {
		return nil
	}
	if len(calls) > 1 {
		logrus.WithFields(logrus.Fields{
			"first_tool":     calls[0].Name,
			"filtered_count": len(calls) - 1,
		}).Debug("CC+FC: Filtered subsequent embedded JSON tool calls (b4u2cc single-call policy)")
	}
	return []functionCall{calls[0]}
}

// unescapeJSONString unescapes a JSON string that may be double-escaped.
// This handles cases where JSON is embedded in another JSON string and has
// escaped backslashes and quotes.
// Examples:
//   - `\\\"` -> `"`
//   - `\\\\` -> `\`
//   - `\\/` -> `/`
func unescapeJSONString(s string) string {
	// First pass: unescape double backslashes
	s = strings.ReplaceAll(s, `\\\\`, `\`)
	// Second pass: unescape escaped quotes
	s = strings.ReplaceAll(s, `\\\"`, `"`)
	s = strings.ReplaceAll(s, `\\"`, `"`)
	// Third pass: unescape escaped forward slashes
	s = strings.ReplaceAll(s, `\\/`, `/`)
	return s
}
