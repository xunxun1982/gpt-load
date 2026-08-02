package proxy

import (
	"encoding/json"
	"io"
	"strings"
)

const (
	strictInvokeOpen = `<invoke name="`
	strictParamOpen  = `<parameter name="`
)

func (s *FunctionCallSession) ParseAndValidate(text string, complete bool) (FunctionCallParseResult, bool) {
	if s == nil || !complete || len(text) > maxContentBufferBytes || s.Trigger == "" || s.choiceMode == functionCallChoiceNone ||
		s.choiceMode == functionCallChoiceInvalid || strings.Count(text, s.Trigger) != 1 {
		return FunctionCallParseResult{}, false
	}
	triggerAt := strings.Index(text, s.Trigger)
	cursor := skipFunctionCallSpace(text, triggerAt+len(s.Trigger))
	wrapper := strings.HasPrefix(text[cursor:], "<function_calls>")
	if wrapper {
		cursor = skipFunctionCallSpace(text, cursor+len("<function_calls>"))
	}
	calls := make([]functionCall, 0, 2)
	for strings.HasPrefix(text[cursor:], strictInvokeOpen) {
		call, next, ok := parseStrictFunctionInvoke(text, cursor)
		if !ok || !s.validateCall(call) {
			return FunctionCallParseResult{}, false
		}
		calls = append(calls, call)
		cursor = skipFunctionCallSpace(text, next)
	}
	if len(calls) == 0 {
		return FunctionCallParseResult{}, false
	}
	if wrapper {
		if !strings.HasPrefix(text[cursor:], "</function_calls>") {
			return FunctionCallParseResult{}, false
		}
		cursor = skipFunctionCallSpace(text, cursor+len("</function_calls>"))
	}
	if cursor != len(text) {
		return FunctionCallParseResult{}, false
	}
	start := triggerAt
	for start > 0 && isFunctionCallSpace(text[start-1]) {
		start--
	}
	return FunctionCallParseResult{Calls: calls, Start: start, End: cursor}, true
}

func parseStrictFunctionInvoke(text string, start int) (functionCall, int, bool) {
	cursor := start + len(strictInvokeOpen)
	nameEnd := strings.IndexByte(text[cursor:], '"')
	if nameEnd <= 0 {
		return functionCall{}, 0, false
	}
	name := text[cursor : cursor+nameEnd]
	cursor += nameEnd + 1
	if cursor >= len(text) || text[cursor] != '>' || strings.ContainsAny(name, "<>\r\n") {
		return functionCall{}, 0, false
	}
	cursor = skipFunctionCallSpace(text, cursor+1)
	args := make(map[string]any)
	for strings.HasPrefix(text[cursor:], strictParamOpen) {
		cursor += len(strictParamOpen)
		nameEnd = strings.IndexByte(text[cursor:], '"')
		if nameEnd <= 0 {
			return functionCall{}, 0, false
		}
		paramName := text[cursor : cursor+nameEnd]
		cursor += nameEnd + 1
		if cursor >= len(text) || text[cursor] != '>' || strings.ContainsAny(paramName, "<>\r\n") {
			return functionCall{}, 0, false
		}
		cursor++
		closeAt := strings.Index(text[cursor:], "</parameter>")
		if closeAt < 0 {
			return functionCall{}, 0, false
		}
		if _, duplicate := args[paramName]; duplicate {
			return functionCall{}, 0, false
		}
		value, ok := parseStrictFunctionValue(text[cursor : cursor+closeAt])
		if !ok {
			return functionCall{}, 0, false
		}
		args[paramName] = value
		cursor = skipFunctionCallSpace(text, cursor+closeAt+len("</parameter>"))
	}
	if !strings.HasPrefix(text[cursor:], "</invoke>") {
		return functionCall{}, 0, false
	}
	return functionCall{Name: name, Args: args}, cursor + len("</invoke>"), true
}

func parseStrictFunctionValue(raw string) (any, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", true
	}
	if !looksLikeStrictJSON(value) {
		return value, true
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		if strings.ContainsAny(value[:1], "-0123456789") {
			return value, true
		}
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if strings.ContainsAny(value[:1], "-0123456789") {
			return value, true
		}
		return nil, false
	}
	return decoded, true
}

func looksLikeStrictJSON(value string) bool {
	if value == "true" || value == "false" || value == "null" {
		return true
	}
	return strings.ContainsAny(value[:1], `{["-0123456789`)
}

func skipFunctionCallSpace(text string, cursor int) int {
	for cursor < len(text) && isFunctionCallSpace(text[cursor]) {
		cursor++
	}
	return cursor
}

func isFunctionCallSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func removeValidatedFunctionCall(text string, result FunctionCallParseResult) string {
	if result.Start < 0 || result.End < result.Start || result.End > len(text) {
		return text
	}
	return text[:result.Start] + text[result.End:]
}
