package proxy

import "github.com/gin-gonic/gin"

const ctxKeyFunctionCallSession = "fc_security_session"

type functionCallChoiceMode uint8

const (
	functionCallChoiceAuto functionCallChoiceMode = iota
	functionCallChoiceNone
	functionCallChoiceSpecific
)

// FunctionCallSession binds response parsing to the exact rewritten request.
type FunctionCallSession struct {
	Trigger      string
	tools        map[string]functionToolDefinition
	choiceMode   functionCallChoiceMode
	selectedTool string
}

type FunctionCallParseResult struct {
	Calls []functionCall
	Start int
	End   int
}

func newFunctionCallSession(trigger string, defs []functionToolDefinition, choice any, choiceSet bool) *FunctionCallSession {
	tools := make(map[string]functionToolDefinition, len(defs))
	for _, def := range defs {
		if def.Name != "" {
			tools[def.Name] = def
		}
	}
	mode, selected := classifyFunctionCallChoice(choice, choiceSet)
	return &FunctionCallSession{Trigger: trigger, tools: tools, choiceMode: mode, selectedTool: selected}
}

func classifyFunctionCallChoice(choice any, choiceSet bool) (functionCallChoiceMode, string) {
	if !choiceSet || choice == nil {
		return functionCallChoiceAuto, ""
	}
	if value, ok := choice.(string); ok {
		switch value {
		case "auto", "required", "any":
			return functionCallChoiceAuto, ""
		case "none":
			return functionCallChoiceNone, ""
		default:
			// Mirror request rewriting: unknown values are treated as unset.
			return functionCallChoiceAuto, ""
		}
	}
	value, ok := choice.(map[string]any)
	if !ok {
		// Mirror request rewriting: unsupported Go values are treated as unset.
		return functionCallChoiceAuto, ""
	}
	typeName, _ := value["type"].(string)
	switch typeName {
	case "auto", "any", "required":
		return functionCallChoiceAuto, ""
	case "none":
		return functionCallChoiceNone, ""
	case "tool":
		name, _ := value["name"].(string)
		if name != "" {
			return functionCallChoiceSpecific, name
		}
	case "function":
		if name, _ := value["name"].(string); name != "" {
			return functionCallChoiceSpecific, name
		}
		if fn, _ := value["function"].(map[string]any); fn != nil {
			if name, _ := fn["name"].(string); name != "" {
				return functionCallChoiceSpecific, name
			}
		}
	}
	// Unknown object shapes are also treated as an unset choice by rewriting.
	return functionCallChoiceAuto, ""
}

func setFunctionCallSession(c *gin.Context, session *FunctionCallSession) {
	if c != nil && session != nil {
		c.Set(ctxKeyFunctionCallSession, session)
	}
}

func functionCallSessionFromContext(c *gin.Context, trigger string) *FunctionCallSession {
	if c == nil || trigger == "" {
		return nil
	}
	value, ok := c.Get(ctxKeyFunctionCallSession)
	if !ok {
		return nil
	}
	session, ok := value.(*FunctionCallSession)
	if !ok || session == nil || session.Trigger != trigger {
		return nil
	}
	return session
}
