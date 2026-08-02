package proxy

import "strings"

func parseAndCleanResponsesFunctionCalls(
	session *FunctionCallSession,
	payload map[string]any,
) ([]functionCall, string, bool, bool) {
	var matchedBlock map[string]any
	var matchedText string
	var matchedResult FunctionCallParseResult
	found := false
	outputTextSeen := false
	triggerCount := 0
	if output, ok := payload["output"].([]any); ok {
		for _, item := range output {
			itemMap, _ := item.(map[string]any)
			content, _ := itemMap["content"].([]any)
			for _, block := range content {
				blockMap, _ := block.(map[string]any)
				if blockMap["type"] != "output_text" {
					continue
				}
				outputTextSeen = true
				text, _ := blockMap["text"].(string)
				triggerCount += strings.Count(text, session.Trigger)
				result, valid := session.ParseAndValidate(text, true)
				if !valid {
					continue
				}
				if found {
					return nil, "", false, false
				}
				found = true
				matchedBlock = blockMap
				matchedText = text
				matchedResult = result
			}
		}
	}
	fromOutput := found
	if outputTextSeen {
		if triggerCount != 1 || !found {
			return nil, "", false, false
		}
	} else {
		text, _ := payload["output_text"].(string)
		result, valid := session.ParseAndValidate(text, true)
		if !valid {
			return nil, "", false, false
		}
		found = true
		matchedText = text
		matchedResult = result
	}
	var rootResult FunctionCallParseResult
	cleanRoot := false
	if fromOutput {
		if rootText, ok := payload["output_text"].(string); ok && strings.Contains(rootText, session.Trigger) {
			var valid bool
			rootResult, valid = session.ParseAndValidate(rootText, true)
			if !valid {
				return nil, "", false, false
			}
			cleanRoot = true
		}
	}
	cleaned := removeValidatedFunctionCall(matchedText, matchedResult)
	if matchedBlock != nil {
		matchedBlock["text"] = cleaned
	} else {
		payload["output_text"] = cleaned
	}
	if cleanRoot {
		rootText, _ := payload["output_text"].(string)
		payload["output_text"] = removeValidatedFunctionCall(rootText, rootResult)
	}
	return matchedResult.Calls, cleaned, fromOutput, found
}

func parseAndCleanAnthropicFunctionCalls(
	session *FunctionCallSession,
	payload map[string]any,
) ([]functionCall, bool) {
	content, ok := payload["content"].([]any)
	if !ok {
		return nil, false
	}
	var matchedBlock map[string]any
	var matchedText string
	var matchedResult FunctionCallParseResult
	triggerCount := 0
	for _, block := range content {
		blockMap, _ := block.(map[string]any)
		if blockMap["type"] != "text" {
			continue
		}
		text, _ := blockMap["text"].(string)
		triggerCount += strings.Count(text, session.Trigger)
		result, valid := session.ParseAndValidate(text, true)
		if !valid {
			continue
		}
		if matchedBlock != nil {
			return nil, false
		}
		matchedBlock = blockMap
		matchedText = text
		matchedResult = result
	}
	if matchedBlock == nil || triggerCount != 1 {
		return nil, false
	}
	matchedBlock["text"] = removeValidatedFunctionCall(matchedText, matchedResult)
	return matchedResult.Calls, true
}
