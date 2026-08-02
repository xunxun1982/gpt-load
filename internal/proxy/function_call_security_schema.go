package proxy

import (
	"encoding/json"
	"math/big"
)

func (s *FunctionCallSession) validateCall(call functionCall) bool {
	def, allowed := s.tools[call.Name]
	if !allowed || (s.choiceMode == functionCallChoiceSpecific && call.Name != s.selectedTool) {
		return false
	}
	return validateFunctionCallArguments(call.Args, def.Parameters)
}

func validateFunctionCallArguments(args map[string]any, schema map[string]any) bool {
	if schema == nil {
		return true
	}
	for _, name := range functionCallRequiredNames(schema["required"]) {
		if _, ok := args[name]; !ok {
			return false
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, value := range args {
		property, _ := properties[name].(map[string]any)
		if property != nil && !functionCallValueMatchesType(value, property["type"]) {
			return false
		}
	}
	return true
}

func functionCallRequiredNames(value any) []string {
	switch required := value.(type) {
	case []string:
		return required
	case []any:
		result := make([]string, 0, len(required))
		for _, item := range required {
			if name, ok := item.(string); ok {
				result = append(result, name)
			}
		}
		return result
	default:
		return nil
	}
}

func functionCallValueMatchesType(value, schemaType any) bool {
	switch types := schemaType.(type) {
	case nil:
		return true
	case string:
		return functionCallValueMatchesSingleType(value, types)
	case []any:
		for _, item := range types {
			if typeName, ok := item.(string); ok && functionCallValueMatchesSingleType(value, typeName) {
				return true
			}
		}
	}
	return false
}

func functionCallValueMatchesSingleType(value any, typeName string) bool {
	switch typeName {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		rational, ok := new(big.Rat).SetString(number.String())
		return ok && rational.IsInt()
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}
