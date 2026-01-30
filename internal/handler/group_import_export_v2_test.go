package handler

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/models"
)

// TestGroupExportInfo_ModelRedirectRulesV2 tests that V2 rules are correctly exported and imported
func TestGroupExportInfo_ModelRedirectRulesV2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		v2Rules     map[string]*models.ModelRedirectRuleV2
		expectEmpty bool
	}{
		{
			name: "single_source_multiple_targets",
			v2Rules: map[string]*models.ModelRedirectRuleV2{
				"quicklite": {
					Targets: []models.ModelRedirectTarget{
						{Model: "tencent/Hunyuan-Turbo", Weight: 100},
						{Model: "Qwen/Qwen3-88", Weight: 100},
					},
				},
			},
			expectEmpty: false,
		},
		{
			name: "multiple_sources",
			v2Rules: map[string]*models.ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []models.ModelRedirectTarget{
						{Model: "gpt-4-turbo", Weight: 70},
						{Model: "gpt-4-0125-preview", Weight: 30},
					},
				},
				"claude-3": {
					Targets: []models.ModelRedirectTarget{
						{Model: "claude-3-opus", Weight: 50},
						{Model: "claude-3-sonnet", Weight: 50},
					},
				},
			},
			expectEmpty: false,
		},
		{
			name:        "empty_rules",
			v2Rules:     map[string]*models.ModelRedirectRuleV2{},
			expectEmpty: true,
		},
		{
			name:        "nil_rules",
			v2Rules:     nil,
			expectEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Marshal V2 rules to JSON
			var v2JSON json.RawMessage
			if tt.v2Rules != nil && len(tt.v2Rules) > 0 {
				data, err := json.Marshal(tt.v2Rules)
				if err != nil {
					t.Fatalf("Failed to marshal V2 rules: %v", err)
				}
				v2JSON = json.RawMessage(data)
			}

			// Create export data
			exportData := GroupExportInfo{
				Name:                 "test-group",
				ModelRedirectRulesV2: v2JSON,
			}

			// Export to JSON
			exportJSON, err := json.Marshal(exportData)
			if err != nil {
				t.Fatalf("Failed to export: %v", err)
			}

			// Import from JSON
			var importData GroupExportInfo
			if err := json.Unmarshal(exportJSON, &importData); err != nil {
				t.Fatalf("Failed to import: %v", err)
			}

			// Verify name
			if importData.Name != "test-group" {
				t.Errorf("Expected name 'test-group', got '%s'", importData.Name)
			}

			// Verify V2 rules
			if tt.expectEmpty {
				if len(importData.ModelRedirectRulesV2) > 0 {
					t.Errorf("Expected empty V2 rules, got: %s", string(importData.ModelRedirectRulesV2))
				}
			} else {
				if len(importData.ModelRedirectRulesV2) == 0 {
					t.Error("Expected non-empty V2 rules, got empty")
				}

				// Parse and verify V2 rules
				var parsedV2 map[string]*models.ModelRedirectRuleV2
				if err := json.Unmarshal(importData.ModelRedirectRulesV2, &parsedV2); err != nil {
					t.Fatalf("Failed to parse imported V2 rules: %v", err)
				}

				// Verify number of source models
				if len(parsedV2) != len(tt.v2Rules) {
					t.Errorf("Expected %d source models, got %d", len(tt.v2Rules), len(parsedV2))
				}

				// Verify each source model and its targets
				for sourceModel, expectedRule := range tt.v2Rules {
					actualRule, exists := parsedV2[sourceModel]
					if !exists {
						t.Errorf("Source model '%s' not found in imported rules", sourceModel)
						continue
					}

					if len(actualRule.Targets) != len(expectedRule.Targets) {
						t.Errorf("Source model '%s': expected %d targets, got %d",
							sourceModel, len(expectedRule.Targets), len(actualRule.Targets))
						continue
					}

					// Verify each target
					for i, expectedTarget := range expectedRule.Targets {
						actualTarget := actualRule.Targets[i]
						if actualTarget.Model != expectedTarget.Model {
							t.Errorf("Source model '%s', target %d: expected model '%s', got '%s'",
								sourceModel, i, expectedTarget.Model, actualTarget.Model)
						}
						if actualTarget.Weight != expectedTarget.Weight {
							t.Errorf("Source model '%s', target %d: expected weight %d, got %d",
								sourceModel, i, expectedTarget.Weight, actualTarget.Weight)
						}
					}
				}
			}
		})
	}
}

// TestGroupExportInfo_BackwardCompatibility tests that V1 and V2 rules can coexist
func TestGroupExportInfo_BackwardCompatibility(t *testing.T) {
	t.Parallel()

	// Create export data with both V1 and V2 rules
	v1Rules := map[string]string{
		"gpt-3.5-turbo": "gpt-3.5-turbo-0125",
	}

	v2Rules := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 100},
			},
		},
	}

	v2JSON, err := json.Marshal(v2Rules)
	if err != nil {
		t.Fatalf("Failed to marshal V2 rules: %v", err)
	}

	exportData := GroupExportInfo{
		Name:                 "test-group",
		ModelRedirectRules:   v1Rules,
		ModelRedirectRulesV2: json.RawMessage(v2JSON),
	}

	// Export to JSON
	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Import from JSON
	var importData GroupExportInfo
	if err := json.Unmarshal(exportJSON, &importData); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	// Verify V1 rules
	if len(importData.ModelRedirectRules) != 1 {
		t.Errorf("Expected 1 V1 rule, got %d", len(importData.ModelRedirectRules))
	}
	if importData.ModelRedirectRules["gpt-3.5-turbo"] != "gpt-3.5-turbo-0125" {
		t.Errorf("V1 rule mismatch")
	}

	// Verify V2 rules
	var parsedV2 map[string]*models.ModelRedirectRuleV2
	if err := json.Unmarshal(importData.ModelRedirectRulesV2, &parsedV2); err != nil {
		t.Fatalf("Failed to parse V2 rules: %v", err)
	}

	if len(parsedV2) != 1 {
		t.Errorf("Expected 1 V2 rule, got %d", len(parsedV2))
	}

	gpt4Rule, exists := parsedV2["gpt-4"]
	if !exists {
		t.Fatal("gpt-4 rule not found in V2 rules")
	}

	if len(gpt4Rule.Targets) != 1 {
		t.Errorf("Expected 1 target for gpt-4, got %d", len(gpt4Rule.Targets))
	}

	if gpt4Rule.Targets[0].Model != "gpt-4-turbo" {
		t.Errorf("Expected target model 'gpt-4-turbo', got '%s'", gpt4Rule.Targets[0].Model)
	}
}

// TestGroupExportInfo_V2WithDisabledTargets tests export/import with disabled targets
func TestGroupExportInfo_V2WithDisabledTargets(t *testing.T) {
	t.Parallel()

	disabled := false
	v2Rules := map[string]*models.ModelRedirectRuleV2{
		"test-model": {
			Targets: []models.ModelRedirectTarget{
				{Model: "target-1", Weight: 100, Enabled: nil}, // Default enabled
				{Model: "target-2", Weight: 50, Enabled: &disabled},
			},
		},
	}

	v2JSON, err := json.Marshal(v2Rules)
	if err != nil {
		t.Fatalf("Failed to marshal V2 rules: %v", err)
	}

	exportData := GroupExportInfo{
		Name:                 "test-group",
		ModelRedirectRulesV2: json.RawMessage(v2JSON),
	}

	// Export and import
	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	var importData GroupExportInfo
	if err := json.Unmarshal(exportJSON, &importData); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	// Parse and verify
	var parsedV2 map[string]*models.ModelRedirectRuleV2
	if err := json.Unmarshal(importData.ModelRedirectRulesV2, &parsedV2); err != nil {
		t.Fatalf("Failed to parse V2 rules: %v", err)
	}

	rule := parsedV2["test-model"]
	if rule == nil {
		t.Fatal("test-model rule not found")
	}

	if len(rule.Targets) != 2 {
		t.Fatalf("Expected 2 targets, got %d", len(rule.Targets))
	}

	// Verify first target (enabled by default)
	if !rule.Targets[0].IsEnabled() {
		t.Error("First target should be enabled by default")
	}

	// Verify second target (explicitly disabled)
	if rule.Targets[1].IsEnabled() {
		t.Error("Second target should be disabled")
	}
}

// TestGroupExportInfo_WithChildGroups tests export/import of groups with child groups
func TestGroupExportInfo_WithChildGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		parentGroup       GroupExportInfo
		childGroups       []ChildGroupExportInfo
		expectChildCount  int
		validateChildKeys bool
	}{
		{
			name: "parent_with_single_child",
			parentGroup: GroupExportInfo{
				Name:        "parent-group",
				DisplayName: "Parent Group",
				GroupType:   "standard",
				ChannelType: "openai",
			},
			childGroups: []ChildGroupExportInfo{
				{
					Name:        "child-1",
					DisplayName: "Child 1",
					Description: "First child group",
					Enabled:     true,
					ProxyKeys:   "key1,key2",
					Sort:        1,
					Keys: []KeyExportInfo{
						{KeyValue: "test-child1-key1", Status: "active"},
						{KeyValue: "test-child1-key2", Status: "active"},
					},
				},
			},
			expectChildCount:  1,
			validateChildKeys: true,
		},
		{
			name: "parent_with_multiple_children",
			parentGroup: GroupExportInfo{
				Name:        "parent-group",
				DisplayName: "Parent Group",
				GroupType:   "standard",
				ChannelType: "anthropic",
			},
			childGroups: []ChildGroupExportInfo{
				{
					Name:        "child-1",
					DisplayName: "Child 1",
					Enabled:     true,
					Sort:        1,
					Keys: []KeyExportInfo{
						{KeyValue: "test-child1-key1", Status: "active"},
					},
				},
				{
					Name:        "child-2",
					DisplayName: "Child 2",
					Enabled:     false,
					Sort:        2,
					Keys: []KeyExportInfo{
						{KeyValue: "test-child2-key1", Status: "active"},
						{KeyValue: "test-child2-key2", Status: "invalid"},
					},
				},
				{
					Name:        "child-3",
					DisplayName: "Child 3",
					Enabled:     true,
					Sort:        3,
					Keys:        []KeyExportInfo{}, // No keys
				},
			},
			expectChildCount:  3,
			validateChildKeys: true,
		},
		{
			name: "parent_without_children",
			parentGroup: GroupExportInfo{
				Name:        "standalone-group",
				DisplayName: "Standalone Group",
				GroupType:   "standard",
				ChannelType: "gemini",
			},
			childGroups:       []ChildGroupExportInfo{},
			expectChildCount:  0,
			validateChildKeys: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create export data
			exportData := GroupExportData{
				Group: tt.parentGroup,
				Keys: []KeyExportInfo{
					{KeyValue: "test-parent-key1", Status: "active"},
				},
				ChildGroups: tt.childGroups,
				ExportedAt:  "2025-01-31T00:00:00Z",
				Version:     "2.0",
			}

			// Export to JSON
			exportJSON, err := json.Marshal(exportData)
			if err != nil {
				t.Fatalf("Failed to export: %v", err)
			}

			// Import from JSON
			var importData GroupImportData
			if err := json.Unmarshal(exportJSON, &importData); err != nil {
				t.Fatalf("Failed to import: %v", err)
			}

			// Verify parent group
			if importData.Group.Name != tt.parentGroup.Name {
				t.Errorf("Expected parent name '%s', got '%s'", tt.parentGroup.Name, importData.Group.Name)
			}
			if importData.Group.GroupType != tt.parentGroup.GroupType {
				t.Errorf("Expected group type '%s', got '%s'", tt.parentGroup.GroupType, importData.Group.GroupType)
			}
			if importData.Group.ChannelType != tt.parentGroup.ChannelType {
				t.Errorf("Expected channel type '%s', got '%s'", tt.parentGroup.ChannelType, importData.Group.ChannelType)
			}

			// Verify parent keys
			if len(importData.Keys) != 1 {
				t.Errorf("Expected 1 parent key, got %d", len(importData.Keys))
			}

			// Verify child groups count
			if len(importData.ChildGroups) != tt.expectChildCount {
				t.Fatalf("Expected %d child groups, got %d", tt.expectChildCount, len(importData.ChildGroups))
			}

			// Verify each child group
			for i, expectedChild := range tt.childGroups {
				actualChild := importData.ChildGroups[i]

				if actualChild.Name != expectedChild.Name {
					t.Errorf("Child %d: expected name '%s', got '%s'", i, expectedChild.Name, actualChild.Name)
				}
				if actualChild.DisplayName != expectedChild.DisplayName {
					t.Errorf("Child %d: expected display name '%s', got '%s'", i, expectedChild.DisplayName, actualChild.DisplayName)
				}
				if actualChild.Description != expectedChild.Description {
					t.Errorf("Child %d: expected description '%s', got '%s'", i, expectedChild.Description, actualChild.Description)
				}
				if actualChild.Enabled != expectedChild.Enabled {
					t.Errorf("Child %d: expected enabled %v, got %v", i, expectedChild.Enabled, actualChild.Enabled)
				}
				if actualChild.ProxyKeys != expectedChild.ProxyKeys {
					t.Errorf("Child %d: expected proxy keys '%s', got '%s'", i, expectedChild.ProxyKeys, actualChild.ProxyKeys)
				}
				if actualChild.Sort != expectedChild.Sort {
					t.Errorf("Child %d: expected sort %d, got %d", i, expectedChild.Sort, actualChild.Sort)
				}

				// Verify child keys
				if tt.validateChildKeys {
					if len(actualChild.Keys) != len(expectedChild.Keys) {
						t.Errorf("Child %d: expected %d keys, got %d", i, len(expectedChild.Keys), len(actualChild.Keys))
						continue
					}

					for j, expectedKey := range expectedChild.Keys {
						actualKey := actualChild.Keys[j]
						if actualKey.KeyValue != expectedKey.KeyValue {
							t.Errorf("Child %d, key %d: expected key value '%s', got '%s'",
								i, j, expectedKey.KeyValue, actualKey.KeyValue)
						}
						if actualKey.Status != expectedKey.Status {
							t.Errorf("Child %d, key %d: expected status '%s', got '%s'",
								i, j, expectedKey.Status, actualKey.Status)
						}
					}
				}
			}
		})
	}
}

// TestGroupExportInfo_ChildGroupsWithV2Rules tests that child groups can have V2 redirect rules
func TestGroupExportInfo_ChildGroupsWithV2Rules(t *testing.T) {
	t.Parallel()

	// Create V2 rules for parent
	parentV2Rules := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 100},
			},
		},
	}
	parentV2JSON, err := json.Marshal(parentV2Rules)
	if err != nil {
		t.Fatalf("Failed to marshal parent V2 rules: %v", err)
	}

	// Create export data with parent having V2 rules
	exportData := GroupExportData{
		Group: GroupExportInfo{
			Name:                 "parent-with-v2",
			DisplayName:          "Parent with V2 Rules",
			GroupType:            "standard",
			ChannelType:          "openai",
			ModelRedirectRulesV2: json.RawMessage(parentV2JSON),
		},
		Keys: []KeyExportInfo{
			{KeyValue: "test-parent-key", Status: "active"},
		},
		ChildGroups: []ChildGroupExportInfo{
			{
				Name:        "child-1",
				DisplayName: "Child 1",
				Enabled:     true,
				Sort:        1,
				Keys: []KeyExportInfo{
					{KeyValue: "test-child-key", Status: "active"},
				},
			},
		},
		ExportedAt: "2025-01-31T00:00:00Z",
		Version:    "2.0",
	}

	// Export to JSON
	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Import from JSON
	var importData GroupImportData
	if err := json.Unmarshal(exportJSON, &importData); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	// Verify parent has V2 rules
	if len(importData.Group.ModelRedirectRulesV2) == 0 {
		t.Error("Expected parent to have V2 rules")
	}

	var parsedV2 map[string]*models.ModelRedirectRuleV2
	if err := json.Unmarshal(importData.Group.ModelRedirectRulesV2, &parsedV2); err != nil {
		t.Fatalf("Failed to parse parent V2 rules: %v", err)
	}

	if _, exists := parsedV2["gpt-4"]; !exists {
		t.Error("Expected gpt-4 rule in parent V2 rules")
	}

	// Verify child group exists
	if len(importData.ChildGroups) != 1 {
		t.Fatalf("Expected 1 child group, got %d", len(importData.ChildGroups))
	}

	child := importData.ChildGroups[0]
	if child.Name != "child-1" {
		t.Errorf("Expected child name 'child-1', got '%s'", child.Name)
	}
	if len(child.Keys) != 1 {
		t.Errorf("Expected 1 child key, got %d", len(child.Keys))
	}
}
