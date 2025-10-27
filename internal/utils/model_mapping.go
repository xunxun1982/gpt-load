package utils

import (
	"encoding/json"
	"errors"
)

// ApplyModelMapping applies model name mapping based on the provided mapping configuration.
// It supports chained redirects (A->B->C) and detects circular references.
// Returns the mapped model name or the original if no mapping is found.
func ApplyModelMapping(originalModel string, mappingJSON string) (string, bool, error) {
	if mappingJSON == "" || mappingJSON == "{}" {
		return originalModel, false, nil
	}

	var modelMap map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &modelMap); err != nil {
		return "", false, errors.New("invalid model mapping JSON format")
	}

	return ApplyModelMappingFromMap(originalModel, modelMap)
}

// ApplyModelMappingFromMap applies model mapping using a pre-parsed map.
// This is more efficient when the map is cached.
// Logic matches new-api implementation exactly.
func ApplyModelMappingFromMap(originalModel string, modelMap map[string]string) (string, bool, error) {
	if len(modelMap) == 0 {
		return originalModel, false, nil
	}

	currentModel := originalModel
	visited := map[string]bool{currentModel: true}
	mapped := false

	for {
		mappedModel, exists := modelMap[currentModel]
		if !exists || mappedModel == "" {
			break
		}

		// Circular reference detection
		if visited[mappedModel] {
			// If it maps to itself and is the original model, treat as no mapping
			if mappedModel == currentModel {
				if currentModel == originalModel {
					return originalModel, false, nil
				}
				// Already mapped to something else, keep the mapping
				return currentModel, true, nil
			}
			// Circular reference detected
			return "", false, errors.New("model mapping contains circular reference")
		}

		visited[mappedModel] = true
		currentModel = mappedModel
		mapped = true
	}

	return currentModel, mapped, nil
}

// ParseModelMapping parses a JSON string into a model mapping map.
// Returns nil if the JSON is empty or invalid.
func ParseModelMapping(mappingJSON string) (map[string]string, error) {
	if mappingJSON == "" || mappingJSON == "{}" {
		return nil, nil
	}

	var modelMap map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &modelMap); err != nil {
		return nil, errors.New("invalid model mapping JSON format")
	}

	return modelMap, nil
}

// ValidateModelMapping validates a model mapping configuration.
// It checks for JSON format validity and circular references.
func ValidateModelMapping(mappingJSON string) error {
	if mappingJSON == "" || mappingJSON == "{}" {
		return nil
	}

	modelMap, err := ParseModelMapping(mappingJSON)
	if err != nil {
		return err
	}

	// Check each mapping for circular references
	for originalModel := range modelMap {
		_, _, err := ApplyModelMappingFromMap(originalModel, modelMap)
		if err != nil {
			return err
		}
	}

	return nil
}
