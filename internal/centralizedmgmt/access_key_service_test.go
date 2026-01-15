package centralizedmgmt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gpt-load/internal/encryption"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}

	// Auto-migrate the HubAccessKey model
	if err := db.AutoMigrate(&HubAccessKey{}); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return db
}

// setupTestService creates a HubAccessKeyService with test dependencies
func setupTestService(t *testing.T) (*HubAccessKeyService, *gorm.DB) {
	db := setupTestDB(t)

	// Create encryption service with a test key
	encSvc, err := encryption.NewService("test-encryption-key-32chars!!")
	if err != nil {
		t.Fatalf("failed to create encryption service: %v", err)
	}

	svc := NewHubAccessKeyService(db, encSvc)
	return svc, db
}

// TestAccessKeyEncryption tests Property 5: Access Key Encryption
// For any Hub access key stored in the database, the key_value field SHALL be encrypted
// using encryption.Service, and the original value SHALL NOT be recoverable from API responses.
// **Validates: Requirements 4.3, 4.5**
func TestAccessKeyEncryption(t *testing.T) {
	svc, db := setupTestService(t)
	ctx := context.Background()

	testCases := []struct {
		name          string
		keyName       string
		keyValue      string
		allowedModels []string
	}{
		{
			name:          "auto-generated key",
			keyName:       "auto-gen-key",
			keyValue:      "", // Will be auto-generated
			allowedModels: []string{},
		},
		{
			name:          "custom key value",
			keyName:       "custom-key",
			keyValue:      "my-custom-secret-key-12345",
			allowedModels: []string{"gpt-4"},
		},
		{
			name:          "key with special characters",
			keyName:       "special-key",
			keyValue:      "key-with-special!@#$%^&*()chars",
			allowedModels: []string{"gpt-4", "claude-3"},
		},
		{
			name:          "long key value",
			keyName:       "long-key",
			keyValue:      "this-is-a-very-long-key-value-that-should-still-be-encrypted-properly-1234567890",
			allowedModels: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dto, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
				Name:          tc.keyName,
				KeyValue:      tc.keyValue,
				AllowedModels: tc.allowedModels,
				Enabled:       true,
			})
			if err != nil {
				t.Fatalf("Failed to create access key: %v", err)
			}

			// If key was auto-generated, use the returned key
			if tc.keyValue == "" {
				tc.keyValue = originalKey
			}

			// Verify: Stored key_value in database should NOT equal the original key
			var storedKey HubAccessKey
			if err := db.First(&storedKey, dto.ID).Error; err != nil {
				t.Fatalf("Failed to fetch stored key: %v", err)
			}

			if storedKey.KeyValue == tc.keyValue {
				t.Error("Stored key_value equals original key (not encrypted)")
			}

			// Verify: Original key should not appear anywhere in the stored value
			if strings.Contains(storedKey.KeyValue, tc.keyValue) {
				t.Error("Original key appears in stored value")
			}

			// Verify: DTO should have a masked key, not the original
			if dto.MaskedKey == tc.keyValue {
				t.Error("DTO MaskedKey equals original key")
			}

			// Verify: Masked key should not contain the full original key
			if len(tc.keyValue) > 10 && strings.Contains(dto.MaskedKey, tc.keyValue) {
				t.Error("MaskedKey contains original key")
			}

			// Verify: ListAccessKeys should also return masked keys
			keys, err := svc.ListAccessKeys(ctx)
			if err != nil {
				t.Fatalf("Failed to list keys: %v", err)
			}

			for _, k := range keys {
				if k.ID == dto.ID {
					if k.MaskedKey == tc.keyValue {
						t.Error("Listed key MaskedKey equals original key")
					}
					break
				}
			}
		})
	}
}

// TestAccessKeyValidation tests Property 6: Access Key Validation
// For any request to Hub endpoints, the system SHALL reject requests with invalid
// or disabled access keys with HTTP 401 status.
// **Validates: Requirements 4.4**
func TestAccessKeyValidation(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create a valid enabled key for testing
	validDTO, validKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "valid-test-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create valid test key: %v", err)
	}

	// Create a disabled key for testing
	disabledDTO, disabledKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "disabled-test-key",
		AllowedModels: []string{},
		Enabled:       false,
	})
	if err != nil {
		t.Fatalf("Failed to create disabled test key: %v", err)
	}

	// Verify the disabled key was actually created with Enabled=false
	if disabledDTO.Enabled {
		t.Fatalf("Disabled key DTO should have Enabled=false, got true")
	}

	t.Run("ValidKeyAccepted", func(t *testing.T) {
		key, err := svc.ValidateAccessKey(ctx, validKey)
		if err != nil {
			t.Errorf("Valid key should be accepted: %v", err)
		}
		if key == nil {
			t.Error("Valid key should return non-nil key object")
		}
		if key != nil && key.ID != validDTO.ID {
			t.Errorf("Returned key ID mismatch: got %d, want %d", key.ID, validDTO.ID)
		}
	})

	t.Run("DisabledKeyRejected", func(t *testing.T) {
		key, err := svc.ValidateAccessKey(ctx, disabledKey)
		if err == nil {
			t.Errorf("Disabled key should be rejected, but got key: %+v", key)
		}
		if err != nil && !strings.Contains(err.Error(), "disabled") {
			t.Errorf("Error should indicate key is disabled: %v", err)
		}
	})

	t.Run("EmptyKeyRejected", func(t *testing.T) {
		_, err := svc.ValidateAccessKey(ctx, "")
		if err == nil {
			t.Error("Empty key should be rejected")
		}
	})

	t.Run("InvalidKeyRejected", func(t *testing.T) {
		invalidKeys := []string{
			"random-invalid-key",
			"hk-nonexistent-key-12345",
			"sk-wrong-prefix-key",
			"   ",
			"a",
		}

		for _, invalidKey := range invalidKeys {
			_, err := svc.ValidateAccessKey(ctx, invalidKey)
			if err == nil {
				t.Errorf("Invalid key '%s' should be rejected", invalidKey)
			}
		}
	})
}

// TestAccessKeyModelAllowed tests the IsModelAllowed function
func TestAccessKeyModelAllowed(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create key with specific allowed models
	allowedModels := []string{"gpt-4", "gpt-3.5-turbo", "claude-3"}
	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "model-restricted-key",
		AllowedModels: allowedModels,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create test key: %v", err)
	}

	// Fetch the key from database
	var key HubAccessKey
	if err := svc.db.First(&key, dto.ID).Error; err != nil {
		t.Fatalf("Failed to fetch key: %v", err)
	}

	t.Run("AllowedModelsAccepted", func(t *testing.T) {
		for _, model := range allowedModels {
			if !svc.IsModelAllowed(&key, model) {
				t.Errorf("Model '%s' should be allowed but was rejected", model)
			}
		}
	})

	t.Run("NotAllowedModelsRejected", func(t *testing.T) {
		notAllowedModels := []string{"gpt-4-turbo", "claude-2", "llama-2", "random-model"}
		for _, model := range notAllowedModels {
			if svc.IsModelAllowed(&key, model) {
				t.Errorf("Model '%s' should NOT be allowed but was accepted", model)
			}
		}
	})

	// Test key with empty allowed models (all models allowed)
	t.Run("EmptyAllowedModelsAllowsAll", func(t *testing.T) {
		allModelsDTO, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "all-models-key",
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Failed to create all-models key: %v", err)
		}

		var allModelsKey HubAccessKey
		if err := svc.db.First(&allModelsKey, allModelsDTO.ID).Error; err != nil {
			t.Fatalf("Failed to fetch all-models key: %v", err)
		}

		testModels := []string{"gpt-4", "claude-3", "llama-2", "any-random-model"}
		for _, model := range testModels {
			if !svc.IsModelAllowed(&allModelsKey, model) {
				t.Errorf("Model '%s' should be allowed when AllowedModels is empty", model)
			}
		}
	})

	t.Run("NilKeyRejectsAll", func(t *testing.T) {
		if svc.IsModelAllowed(nil, "gpt-4") {
			t.Error("Nil key should reject all models")
		}
	})
}

// TestMaskKeyValue tests the key masking function
func TestMaskKeyValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short key",
			input:    "abc",
			expected: "***",
		},
		{
			name:     "exactly 8 chars",
			input:    "12345678",
			expected: "***",
		},
		{
			name:     "normal key",
			input:    "hk-abc123xyz789def",
			expected: "hk-abc...def",
		},
		{
			name:     "long key",
			input:    "hk-abcdefghijklmnopqrstuvwxyz123456789",
			expected: "hk-abc...789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskKeyValue(tt.input)
			if result != tt.expected {
				t.Errorf("MaskKeyValue(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestAccessKeyCRUD tests basic CRUD operations
func TestAccessKeyCRUD(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create
	t.Run("Create", func(t *testing.T) {
		dto, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "test-crud-key",
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if dto.ID == 0 {
			t.Error("Created key should have non-zero ID")
		}
		if originalKey == "" {
			t.Error("Original key should not be empty")
		}
		if !strings.HasPrefix(originalKey, hubAccessKeyPrefix) {
			t.Errorf("Generated key should have prefix %s", hubAccessKeyPrefix)
		}
	})

	// Create with custom key
	t.Run("CreateWithCustomKey", func(t *testing.T) {
		customKey := "my-custom-api-key-12345"
		dto, returnedKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "custom-key-test",
			KeyValue:      customKey,
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create with custom key failed: %v", err)
		}
		if returnedKey != customKey {
			t.Errorf("Returned key should match custom key: got %s, want %s", returnedKey, customKey)
		}
		if dto.ID == 0 {
			t.Error("Created key should have non-zero ID")
		}
	})

	// Read
	t.Run("Read", func(t *testing.T) {
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "read-test-key",
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		readDTO, err := svc.GetAccessKey(ctx, dto.ID)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		if readDTO.Name != dto.Name {
			t.Errorf("Read name mismatch: got %s, want %s", readDTO.Name, dto.Name)
		}
	})

	// Update
	t.Run("Update", func(t *testing.T) {
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "update-test-key",
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		newName := "updated-key-name"
		updatedDTO, err := svc.UpdateAccessKey(ctx, dto.ID, UpdateAccessKeyParams{
			Name: &newName,
		})
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}
		if updatedDTO.Name != newName {
			t.Errorf("Updated name mismatch: got %s, want %s", updatedDTO.Name, newName)
		}
	})

	// List
	t.Run("List", func(t *testing.T) {
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "list-test-key",
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		keys, err := svc.ListAccessKeys(ctx)
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		found := false
		for _, k := range keys {
			if k.ID == dto.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Created key not found in list")
		}
	})

	// Delete
	t.Run("Delete", func(t *testing.T) {
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          "delete-test-key",
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		if err := svc.DeleteAccessKey(ctx, dto.ID); err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify deletion
		_, err = svc.GetAccessKey(ctx, dto.ID)
		if err == nil {
			t.Error("Deleted key should not be found")
		}
	})
}

// TestAccessKeyAllowedModelsJSON tests JSON serialization of allowed models
func TestAccessKeyAllowedModelsJSON(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	allowedModels := []string{"gpt-4", "gpt-3.5-turbo", "claude-3-opus"}

	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "json-test-key",
		AllowedModels: allowedModels,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify DTO has correct allowed models
	if len(dto.AllowedModels) != len(allowedModels) {
		t.Errorf("AllowedModels count mismatch: got %d, want %d", len(dto.AllowedModels), len(allowedModels))
	}

	for i, model := range allowedModels {
		if dto.AllowedModels[i] != model {
			t.Errorf("AllowedModels[%d] mismatch: got %s, want %s", i, dto.AllowedModels[i], model)
		}
	}

	// Verify mode is "specific"
	if dto.AllowedModelsMode != "specific" {
		t.Errorf("AllowedModelsMode should be 'specific', got %s", dto.AllowedModelsMode)
	}

	// Test empty allowed models (all mode)
	allDTO, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "all-mode-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create all-mode key failed: %v", err)
	}

	if allDTO.AllowedModelsMode != "all" {
		t.Errorf("AllowedModelsMode should be 'all', got %s", allDTO.AllowedModelsMode)
	}
}

// TestAccessKeyCacheInvalidation tests that cache is properly invalidated
func TestAccessKeyCacheInvalidation(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create a key
	dto, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "cache-test-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Validate to populate cache
	_, err = svc.ValidateAccessKey(ctx, originalKey)
	if err != nil {
		t.Fatalf("Initial validation failed: %v", err)
	}

	// Disable the key
	enabled := false
	_, err = svc.UpdateAccessKey(ctx, dto.ID, UpdateAccessKeyParams{
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Validation should now fail (cache should be invalidated)
	_, err = svc.ValidateAccessKey(ctx, originalKey)
	if err == nil {
		t.Error("Validation should fail after disabling key")
	}
}

// TestGetAllowedModels tests the GetAllowedModels helper function
func TestGetAllowedModels(t *testing.T) {
	svc, _ := setupTestService(t)

	// Test with nil key
	if models := svc.GetAllowedModels(nil); models != nil {
		t.Error("GetAllowedModels(nil) should return nil")
	}

	// Test with empty allowed models (all models)
	emptyJSON, _ := json.Marshal([]string{})
	emptyKey := &HubAccessKey{
		AllowedModels: emptyJSON,
	}
	if models := svc.GetAllowedModels(emptyKey); models != nil {
		t.Error("GetAllowedModels with empty list should return nil")
	}

	// Test with specific models
	specificJSON, _ := json.Marshal([]string{"gpt-4", "claude-3"})
	specificKey := &HubAccessKey{
		AllowedModels: specificJSON,
	}
	models := svc.GetAllowedModels(specificKey)
	if len(models) != 2 {
		t.Errorf("Expected 2 models, got %d", len(models))
	}
	if models[0] != "gpt-4" || models[1] != "claude-3" {
		t.Errorf("Unexpected models: %v", models)
	}
}

// TestAccessKeyDuplicateNameRejected tests that duplicate names are rejected
func TestAccessKeyDuplicateNameRejected(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create first key
	_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "duplicate-name-test",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("First create failed: %v", err)
	}

	// Try to create second key with same name
	_, _, err = svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "duplicate-name-test",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err == nil {
		t.Error("Should reject duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Error should indicate name already exists: %v", err)
	}
}

// TestAccessKeyEmptyNameRejected tests that empty names are rejected
func TestAccessKeyEmptyNameRejected(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	testCases := []string{"", "   ", "\t", "\n"}

	for _, name := range testCases {
		_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          name,
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err == nil {
			t.Errorf("Should reject empty/whitespace name: %q", name)
		}
	}
}

// TestAccessKeyUpdateAllowedModels tests updating allowed models
func TestAccessKeyUpdateAllowedModels(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create key with initial models
	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "update-models-test",
		AllowedModels: []string{"gpt-4"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Update allowed models
	newModels := []string{"gpt-4", "claude-3", "llama-2"}
	updatedDTO, err := svc.UpdateAccessKey(ctx, dto.ID, UpdateAccessKeyParams{
		AllowedModels: newModels,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if len(updatedDTO.AllowedModels) != len(newModels) {
		t.Errorf("AllowedModels count mismatch: got %d, want %d", len(updatedDTO.AllowedModels), len(newModels))
	}

	for i, model := range newModels {
		if updatedDTO.AllowedModels[i] != model {
			t.Errorf("AllowedModels[%d] mismatch: got %s, want %s", i, updatedDTO.AllowedModels[i], model)
		}
	}
}

// TestInvalidateAllKeyCache tests bulk cache invalidation
func TestInvalidateAllKeyCache(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	// Create and validate a key to populate cache
	_, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bulk-cache-test",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Validate to populate cache
	_, err = svc.ValidateAccessKey(ctx, originalKey)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	// Invalidate all cache
	svc.InvalidateAllKeyCache()

	// Cache should be empty now, but validation should still work (will re-fetch from DB)
	_, err = svc.ValidateAccessKey(ctx, originalKey)
	if err != nil {
		t.Errorf("Validation should still work after cache invalidation: %v", err)
	}
}


// TestAccessKeyExportEncryption tests Property 11: Hub Access Key Export Encryption
// For any Hub access key included in system export, the key_value field SHALL remain
// encrypted using the same encryption as database storage.
// **Validates: Requirements 10.1, 10.2**
func TestAccessKeyExportEncryption(t *testing.T) {
	svc, db := setupTestService(t)
	ctx := context.Background()

	// Create test keys
	testKeys := []struct {
		name          string
		allowedModels []string
	}{
		{"export-test-key-1", []string{}},
		{"export-test-key-2", []string{"gpt-4", "claude-3"}},
		{"export-test-key-3", []string{"llama-2"}},
	}

	originalKeys := make(map[string]string) // name -> original key value

	for _, tc := range testKeys {
		dto, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          tc.name,
			AllowedModels: tc.allowedModels,
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("Failed to create test key %s: %v", tc.name, err)
		}
		originalKeys[dto.Name] = originalKey
	}

	// Export keys
	exports, err := svc.ExportAccessKeys(ctx)
	if err != nil {
		t.Fatalf("Failed to export access keys: %v", err)
	}

	if len(exports) != len(testKeys) {
		t.Errorf("Expected %d exported keys, got %d", len(testKeys), len(exports))
	}

	for _, export := range exports {
		originalKey, exists := originalKeys[export.Name]
		if !exists {
			t.Errorf("Exported key %s not found in original keys", export.Name)
			continue
		}

		// Verify: Exported key_value should NOT equal the original key (should be encrypted)
		if export.KeyValue == originalKey {
			t.Errorf("Exported key_value for %s equals original key (not encrypted)", export.Name)
		}

		// Verify: Original key should not appear in the exported value
		if strings.Contains(export.KeyValue, originalKey) {
			t.Errorf("Original key appears in exported value for %s", export.Name)
		}

		// Verify: Exported key_value should match what's stored in database
		var storedKey HubAccessKey
		if err := db.Where("name = ?", export.Name).First(&storedKey).Error; err != nil {
			t.Errorf("Failed to fetch stored key %s: %v", export.Name, err)
			continue
		}

		if export.KeyValue != storedKey.KeyValue {
			t.Errorf("Exported key_value for %s doesn't match database value", export.Name)
		}
	}
}

// TestAccessKeyImportValidation tests Property 12: Hub Access Key Import Validation
// For any Hub access key being imported, the system SHALL validate decryption before
// import and skip keys with decryption errors.
// **Validates: Requirements 10.3, 10.4**
func TestAccessKeyImportValidation(t *testing.T) {
	svc, db := setupTestService(t)
	ctx := context.Background()

	// Create and export a key
	_, originalKey, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "import-test-key",
		AllowedModels: []string{"gpt-4"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create test key: %v", err)
	}

	// Export the key
	exports, err := svc.ExportAccessKeys(ctx)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	if len(exports) == 0 {
		t.Fatal("No keys exported")
	}

	// Delete the original key
	var key HubAccessKey
	if err := db.Where("name = ?", "import-test-key").First(&key).Error; err != nil {
		t.Fatalf("Failed to find key: %v", err)
	}
	if err := svc.DeleteAccessKey(ctx, key.ID); err != nil {
		t.Fatalf("Failed to delete key: %v", err)
	}

	// Test 1: Import with valid encrypted key (same encryption key)
	t.Run("ValidEncryptedKeyImported", func(t *testing.T) {
		tx := db.Begin()
		defer tx.Rollback()

		imported, skipped, err := svc.ImportAccessKeys(ctx, tx, exports)
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		if imported != 1 {
			t.Errorf("Expected 1 imported, got %d", imported)
		}
		if skipped != 0 {
			t.Errorf("Expected 0 skipped, got %d", skipped)
		}

		// Verify the imported key can be validated with original key value
		tx.Commit()
		_, err = svc.ValidateAccessKey(ctx, originalKey)
		if err != nil {
			t.Errorf("Imported key should be valid: %v", err)
		}
	})

	// Test 2: Import with invalid encrypted key (corrupted)
	t.Run("InvalidEncryptedKeySkipped", func(t *testing.T) {
		// Clean up first
		db.Where("name LIKE ?", "import-test-key%").Delete(&HubAccessKey{})

		invalidExports := []HubAccessKeyExportInfo{
			{
				Name:          "invalid-key-1",
				KeyValue:      "not-a-valid-encrypted-value",
				AllowedModels: []string{},
				Enabled:       true,
			},
			{
				Name:          "invalid-key-2",
				KeyValue:      "abc123", // Too short to be valid
				AllowedModels: []string{},
				Enabled:       true,
			},
		}

		tx := db.Begin()
		defer tx.Rollback()

		imported, skipped, err := svc.ImportAccessKeys(ctx, tx, invalidExports)
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		if imported != 0 {
			t.Errorf("Expected 0 imported for invalid keys, got %d", imported)
		}
		if skipped != 2 {
			t.Errorf("Expected 2 skipped for invalid keys, got %d", skipped)
		}
	})

	// Test 3: Import with empty key value
	t.Run("EmptyKeyValueSkipped", func(t *testing.T) {
		emptyExports := []HubAccessKeyExportInfo{
			{
				Name:          "empty-key",
				KeyValue:      "",
				AllowedModels: []string{},
				Enabled:       true,
			},
		}

		tx := db.Begin()
		defer tx.Rollback()

		imported, skipped, err := svc.ImportAccessKeys(ctx, tx, emptyExports)
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		if imported != 0 {
			t.Errorf("Expected 0 imported for empty key, got %d", imported)
		}
		if skipped != 1 {
			t.Errorf("Expected 1 skipped for empty key, got %d", skipped)
		}
	})
}

// TestAccessKeyImportUniqueNames tests that import generates unique names for conflicts
// **Validates: Requirements 10.5**
func TestAccessKeyImportUniqueNames(t *testing.T) {
	svc, db := setupTestService(t)
	ctx := context.Background()

	// Create an existing key with a specific name
	_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "conflict-name",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create existing key: %v", err)
	}

	// Create a new key with a different key value to export
	_, newKeyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "to-export-key",
		AllowedModels: []string{"gpt-4"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create export key: %v", err)
	}

	// Export the second key
	exports, err := svc.ExportAccessKeys(ctx)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Find the exported key and change its name to conflict
	var exportToImport []HubAccessKeyExportInfo
	for _, e := range exports {
		if e.Name == "to-export-key" {
			e.Name = "conflict-name" // Create name conflict
			exportToImport = append(exportToImport, e)
			break
		}
	}

	if len(exportToImport) == 0 {
		t.Fatal("No export found to import")
	}

	// Delete the original "to-export-key" so we can import it with a new name
	var toDelete HubAccessKey
	if err := db.Where("name = ?", "to-export-key").First(&toDelete).Error; err != nil {
		t.Fatalf("Failed to find to-export-key: %v", err)
	}
	if err := svc.DeleteAccessKey(ctx, toDelete.ID); err != nil {
		t.Fatalf("Failed to delete to-export-key: %v", err)
	}

	// Import with name conflict - should generate unique name
	tx := db.Begin()
	imported, skipped, err := svc.ImportAccessKeys(ctx, tx, exportToImport)
	tx.Commit()

	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported != 1 {
		t.Errorf("Expected 1 imported, got %d", imported)
	}
	if skipped != 0 {
		t.Errorf("Expected 0 skipped, got %d", skipped)
	}

	// Verify a new key was created with a different name
	var keys []HubAccessKey
	if err := db.Where("name LIKE ?", "conflict-name%").Find(&keys).Error; err != nil {
		t.Fatalf("Failed to find keys: %v", err)
	}

	if len(keys) != 2 {
		t.Errorf("Expected 2 keys with conflict-name prefix, got %d", len(keys))
	}

	// Verify one has the original name and one has a suffix
	hasOriginal := false
	hasSuffix := false
	for _, k := range keys {
		if k.Name == "conflict-name" {
			hasOriginal = true
		} else if strings.HasPrefix(k.Name, "conflict-name") && len(k.Name) > len("conflict-name") {
			hasSuffix = true
		}
	}

	if !hasOriginal {
		t.Error("Original conflict-name key not found")
	}
	if !hasSuffix {
		t.Error("Renamed key with suffix not found")
	}

	// Verify the imported key can be validated with the original key value
	_, err = svc.ValidateAccessKey(ctx, newKeyValue)
	if err != nil {
		t.Errorf("Imported key should be valid: %v", err)
	}
}

// TestAccessKeyImportDuplicateKeyValue tests that duplicate key values are skipped
func TestAccessKeyImportDuplicateKeyValue(t *testing.T) {
	svc, db := setupTestService(t)
	ctx := context.Background()

	// Create a key
	_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "original-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Failed to create key: %v", err)
	}

	// Export the key
	exports, err := svc.ExportAccessKeys(ctx)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Try to import the same key with a different name
	for i := range exports {
		exports[i].Name = "different-name"
	}

	tx := db.Begin()
	imported, skipped, err := svc.ImportAccessKeys(ctx, tx, exports)
	tx.Commit()

	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Should be skipped because key value (hash) already exists
	if imported != 0 {
		t.Errorf("Expected 0 imported for duplicate key value, got %d", imported)
	}
	if skipped != 1 {
		t.Errorf("Expected 1 skipped for duplicate key value, got %d", skipped)
	}
}
