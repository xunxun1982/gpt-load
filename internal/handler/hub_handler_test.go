package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gpt-load/internal/centralizedmgmt"
	"gpt-load/internal/encryption"
	"gpt-load/internal/i18n"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var i18nOnce sync.Once

// initTestI18n initializes i18n for tests (only once)
func initTestI18n() error {
	var initErr error
	i18nOnce.Do(func() {
		initErr = i18n.Init()
	})
	return initErr
}

// setupHubHandlerTestDB creates an in-memory SQLite database for testing
func setupHubHandlerTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}

	// Auto-migrate the required models
	if err := db.AutoMigrate(
		&models.Group{},
		&models.GroupSubGroup{},
		&centralizedmgmt.HubAccessKey{},
	); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return db
}

// createHubTestGroup creates a test group in the database
func createHubTestGroup(t *testing.T, db *gorm.DB, name string, groupType string, channelType string, sort int, enabled bool, testModel string) *models.Group {
	group := &models.Group{
		Name:        name,
		GroupType:   groupType,
		ChannelType: channelType,
		Sort:        sort,
		Enabled:     true,
		TestModel:   testModel,
		Upstreams:   datatypes.JSON("[]"),
	}

	if err := db.Create(group).Error; err != nil {
		t.Fatalf("failed to create test group: %v", err)
	}

	if !enabled {
		if err := db.Model(group).Update("enabled", false).Error; err != nil {
			t.Fatalf("failed to disable test group: %v", err)
		}
		group.Enabled = false
	}

	return group
}

// createHubTestGroupWithRedirects creates a test group with V2 model redirect rules
func createHubTestGroupWithRedirects(t *testing.T, db *gorm.DB, name string, sort int, enabled bool, testModel string, redirects map[string]*models.ModelRedirectRuleV2) *models.Group {
	var redirectsJSON []byte
	if redirects != nil {
		var err error
		redirectsJSON, err = json.Marshal(redirects)
		if err != nil {
			t.Fatalf("failed to marshal redirects: %v", err)
		}
	}

	group := &models.Group{
		Name:                 name,
		GroupType:            "standard",
		ChannelType:          "openai",
		Sort:                 sort,
		Enabled:              true,
		TestModel:            testModel,
		Upstreams:            datatypes.JSON("[]"),
		ModelRedirectRulesV2: redirectsJSON,
	}

	if err := db.Create(group).Error; err != nil {
		t.Fatalf("failed to create test group: %v", err)
	}

	if !enabled {
		if err := db.Model(group).Update("enabled", false).Error; err != nil {
			t.Fatalf("failed to disable test group: %v", err)
		}
		group.Enabled = false
	}

	return group
}

// setupHubHandlerServices creates the services needed for HubHandler testing
func setupHubHandlerServices(t *testing.T, db *gorm.DB) (*centralizedmgmt.HubService, *centralizedmgmt.HubAccessKeyService) {
	mockStore := store.NewMemoryStore()
	dynamicWeightManager := services.NewDynamicWeightManager(mockStore)
	encryptionSvc, err := encryption.NewService("test-encryption-key-32bytes!!")
	if err != nil {
		t.Fatalf("failed to create encryption service: %v", err)
	}

	hubService := centralizedmgmt.NewHubService(db, nil, dynamicWeightManager)
	accessKeyService := centralizedmgmt.NewHubAccessKeyService(db, encryptionSvc)

	return hubService, accessKeyService
}

// TestExtractModelFromRequest tests model extraction from different request formats
// **Validates: Requirements 9.1, 9.7** - Request Body Passthrough
func TestExtractModelFromRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		method        string
		path          string
		body          map[string]any
		expectedModel string
		expectError   bool
	}{
		{
			name:   "OpenAI format",
			method: http.MethodPost,
			path:   "/hub/v1/chat/completions",
			body: map[string]any{
				"model":    "gpt-4",
				"messages": []map[string]string{{"role": "user", "content": "hello"}},
			},
			expectedModel: "gpt-4",
			expectError:   false,
		},
		{
			name:   "Claude format",
			method: http.MethodPost,
			path:   "/hub/v1/messages",
			body: map[string]any{
				"model":      "claude-3-opus",
				"max_tokens": 1024,
				"messages":   []map[string]string{{"role": "user", "content": "hello"}},
			},
			expectedModel: "claude-3-opus",
			expectError:   false,
		},
		{
			name:   "Codex format",
			method: http.MethodPost,
			path:   "/hub/v1/responses",
			body: map[string]any{
				"model": "codex-mini",
				"input": "test input",
			},
			expectedModel: "codex-mini",
			expectError:   false,
		},
		{
			name:          "GET request - no model needed",
			method:        http.MethodGet,
			path:          "/hub/v1/models",
			body:          nil,
			expectedModel: "",
			expectError:   false,
		},
		{
			name:          "Empty body",
			method:        http.MethodPost,
			path:          "/hub/v1/chat/completions",
			body:          nil,
			expectedModel: "",
			expectError:   false,
		},
		{
			name:   "Missing model field",
			method: http.MethodPost,
			path:   "/hub/v1/chat/completions",
			body: map[string]any{
				"messages": []map[string]string{{"role": "user", "content": "hello"}},
			},
			expectedModel: "",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create handler
			handler := &HubHandler{}

			// Create request
			var bodyBytes []byte
			if tt.body != nil {
				var err error
				bodyBytes, err = json.Marshal(tt.body)
				if err != nil {
					t.Fatalf("failed to marshal body: %v", err)
				}
			}

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			// Extract model
			model, err := handler.extractModelFromRequest(c)

			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if model != tt.expectedModel {
				t.Errorf("expected model %q, got %q", tt.expectedModel, model)
			}
		})
	}
}

// TestRewriteHubPath tests path rewriting from hub to proxy format
// **Validates: Requirements 9.1** - Request routing
func TestRewriteHubPath(t *testing.T) {
	handler := &HubHandler{}

	tests := []struct {
		name         string
		inputPath    string
		groupName    string
		expectedPath string
	}{
		{
			name:         "OpenAI chat completions",
			inputPath:    "/hub/v1/chat/completions",
			groupName:    "test-group",
			expectedPath: "/proxy/test-group/v1/chat/completions",
		},
		{
			name:         "Claude messages",
			inputPath:    "/hub/v1/messages",
			groupName:    "claude-group",
			expectedPath: "/proxy/claude-group/v1/messages",
		},
		{
			name:         "Codex responses",
			inputPath:    "/hub/v1/responses",
			groupName:    "codex-group",
			expectedPath: "/proxy/codex-group/v1/responses",
		},
		{
			name:         "Models endpoint",
			inputPath:    "/hub/v1/models",
			groupName:    "any-group",
			expectedPath: "/proxy/any-group/v1/models",
		},
		{
			name:         "Gemini format",
			inputPath:    "/hub/v1/models/gemini-pro:generateContent",
			groupName:    "gemini-group",
			expectedPath: "/proxy/gemini-group/v1/models/gemini-pro:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.rewriteHubPath(tt.inputPath, tt.groupName)
			if result != tt.expectedPath {
				t.Errorf("expected %q, got %q", tt.expectedPath, result)
			}
		})
	}
}

// TestHubErrorResponse tests error response formatting
func TestHubErrorResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &HubHandler{}

	tests := []struct {
		name           string
		status         int
		code           string
		message        string
		expectedType   string
	}{
		{
			name:         "Unauthorized error",
			status:       http.StatusUnauthorized,
			code:         "hub_key_invalid",
			message:      "Invalid access key",
			expectedType: "authentication_error",
		},
		{
			name:         "Forbidden error",
			status:       http.StatusForbidden,
			code:         "hub_model_not_allowed",
			message:      "Model not allowed",
			expectedType: "authentication_error",
		},
		{
			name:         "Not found error",
			status:       http.StatusNotFound,
			code:         "hub_model_not_found",
			message:      "Model not found",
			expectedType: "not_found_error",
		},
		{
			name:         "Bad request error",
			status:       http.StatusBadRequest,
			code:         "hub_invalid_request",
			message:      "Invalid request",
			expectedType: "invalid_request_error",
		},
		{
			name:         "Server error",
			status:       http.StatusInternalServerError,
			code:         "hub_internal_error",
			message:      "Internal error",
			expectedType: "server_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			handler.returnHubError(c, tt.status, tt.code, tt.message)

			if w.Code != tt.status {
				t.Errorf("expected status %d, got %d", tt.status, w.Code)
			}

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			errorObj, ok := resp["error"].(map[string]any)
			if !ok {
				t.Fatal("response should contain error object")
			}

			if errorObj["code"] != tt.code {
				t.Errorf("expected code %q, got %q", tt.code, errorObj["code"])
			}
			if errorObj["message"] != tt.message {
				t.Errorf("expected message %q, got %q", tt.message, errorObj["message"])
			}
			if errorObj["type"] != tt.expectedType {
				t.Errorf("expected type %q, got %q", tt.expectedType, errorObj["type"])
			}
		})
	}
}

// TestHandleListModels tests the model list endpoint
// **Validates: Requirements 9.3** - Group Configuration Application
func TestHandleListModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupHubHandlerTestDB(t)
	ctx := context.Background()

	// Create test groups with V2 redirect rules
	redirects1 := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {Targets: []models.ModelRedirectTarget{{Model: "gpt-4-turbo", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-1", 1, true, "gpt-4", redirects1)

	redirects2 := map[string]*models.ModelRedirectRuleV2{
		"gpt-3.5-turbo": {Targets: []models.ModelRedirectTarget{{Model: "gpt-3.5-turbo-0125", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-2", 2, true, "gpt-3.5-turbo", redirects2)

	hubService, accessKeyService := setupHubHandlerServices(t, db)

	// Create an access key with all models allowed
	params := centralizedmgmt.CreateAccessKeyParams{
		Name:          "test-key",
		AllowedModels: []string{}, // Empty means all models
		Enabled:       true,
	}
	_, keyValue, err := accessKeyService.CreateAccessKey(ctx, params)
	if err != nil {
		t.Fatalf("failed to create access key: %v", err)
	}

	// Validate the key to get the access key object
	accessKey, err := accessKeyService.ValidateAccessKey(ctx, keyValue)
	if err != nil {
		t.Fatalf("failed to validate access key: %v", err)
	}

	handler := NewHubHandler(hubService, accessKeyService, nil, nil)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/hub/v1/models", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("hub_access_key", accessKey)

	// Call handler
	handler.HandleListModels(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["object"] != "list" {
		t.Errorf("expected object 'list', got %v", resp["object"])
	}

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatal("response should contain data array")
	}

	// Should have at least 2 models
	if len(data) < 2 {
		t.Errorf("expected at least 2 models, got %d", len(data))
	}

	// Verify model format
	for _, item := range data {
		model, ok := item.(map[string]any)
		if !ok {
			t.Error("each model should be an object")
			continue
		}
		if model["object"] != "model" {
			t.Errorf("model object should be 'model', got %v", model["object"])
		}
		if model["id"] == nil || model["id"] == "" {
			t.Error("model should have an id")
		}
	}
}

// TestHandleListModelsWithRestrictedKey tests model filtering by access key
func TestHandleListModelsWithRestrictedKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupHubHandlerTestDB(t)
	ctx := context.Background()

	// Create test groups with V2 redirect rules
	redirects1 := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {Targets: []models.ModelRedirectTarget{{Model: "gpt-4-turbo", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-1", 1, true, "gpt-4", redirects1)

	redirects2 := map[string]*models.ModelRedirectRuleV2{
		"gpt-3.5-turbo": {Targets: []models.ModelRedirectTarget{{Model: "gpt-3.5-turbo-0125", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-2", 2, true, "gpt-3.5-turbo", redirects2)

	redirects3 := map[string]*models.ModelRedirectRuleV2{
		"claude-3": {Targets: []models.ModelRedirectTarget{{Model: "claude-3-opus", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-3", 3, true, "claude-3", redirects3)

	hubService, accessKeyService := setupHubHandlerServices(t, db)

	// Create an access key with restricted models
	params := centralizedmgmt.CreateAccessKeyParams{
		Name:          "restricted-key",
		AllowedModels: []string{"gpt-4"}, // Only gpt-4 allowed
		Enabled:       true,
	}
	_, keyValue, err := accessKeyService.CreateAccessKey(ctx, params)
	if err != nil {
		t.Fatalf("failed to create access key: %v", err)
	}

	accessKey, err := accessKeyService.ValidateAccessKey(ctx, keyValue)
	if err != nil {
		t.Fatalf("failed to validate access key: %v", err)
	}

	handler := NewHubHandler(hubService, accessKeyService, nil, nil)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/hub/v1/models", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("hub_access_key", accessKey)

	// Call handler
	handler.HandleListModels(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatal("response should contain data array")
	}

	// Should only have 1 model (gpt-4)
	if len(data) != 1 {
		t.Errorf("expected 1 model, got %d", len(data))
	}

	if len(data) > 0 {
		model := data[0].(map[string]any)
		if model["id"] != "gpt-4" {
			t.Errorf("expected model 'gpt-4', got %v", model["id"])
		}
	}
}

// TestHandleGetModelPool tests the admin model pool endpoint
func TestHandleGetModelPool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Initialize i18n for tests
	if err := initTestI18n(); err != nil {
		t.Fatalf("failed to initialize i18n: %v", err)
	}

	db := setupHubHandlerTestDB(t)

	// Create test groups with V2 redirect rules
	redirects1 := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {Targets: []models.ModelRedirectTarget{{Model: "gpt-4-turbo", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-1", 1, true, "gpt-4", redirects1)

	redirects2 := map[string]*models.ModelRedirectRuleV2{
		"gpt-3.5-turbo": {Targets: []models.ModelRedirectTarget{{Model: "gpt-3.5-turbo-0125", Weight: 100}}},
	}
	createHubTestGroupWithRedirects(t, db, "group-2", 2, true, "gpt-3.5-turbo", redirects2)

	hubService, accessKeyService := setupHubHandlerServices(t, db)
	handler := NewHubHandler(hubService, accessKeyService, nil, nil)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/hub/admin/model-pool", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// Call handler
	handler.HandleGetModelPool(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Check response structure
	if resp["code"] != float64(0) {
		t.Errorf("expected code 0, got %v", resp["code"])
	}

	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatal("response should contain data object")
	}

	models, ok := data["models"].([]any)
	if !ok {
		t.Fatal("response data should contain models array")
	}

	// Should have at least 2 models (from V2 redirect rules)
	if len(models) < 2 {
		t.Errorf("expected at least 2 models in pool, got %d", len(models))
	}
}

// TestAccessKeyCRUD tests access key CRUD operations through handler
func TestAccessKeyCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Initialize i18n for tests
	if err := initTestI18n(); err != nil {
		t.Fatalf("failed to initialize i18n: %v", err)
	}

	db := setupHubHandlerTestDB(t)

	hubService, accessKeyService := setupHubHandlerServices(t, db)
	handler := NewHubHandler(hubService, accessKeyService, nil, nil)

	// Test Create
	t.Run("Create", func(t *testing.T) {
		body := map[string]any{
			"name":           "test-key",
			"allowed_models": []string{"gpt-4", "gpt-3.5-turbo"},
			"enabled":        true,
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/hub/admin/access-keys", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		handler.HandleCreateAccessKey(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		data, ok := resp["data"].(map[string]any)
		if !ok {
			t.Fatal("response should contain data object")
		}

		// Should return the key value on creation
		if data["key_value"] == nil || data["key_value"] == "" {
			t.Error("should return key_value on creation")
		}
	})

	// Test List
	t.Run("List", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hub/admin/access-keys", nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		handler.HandleListAccessKeys(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		data, ok := resp["data"].(map[string]any)
		if !ok {
			t.Fatal("response should contain data object")
		}

		accessKeys, ok := data["access_keys"].([]any)
		if !ok {
			t.Fatal("response data should contain access_keys array")
		}

		if len(accessKeys) < 1 {
			t.Error("should have at least 1 access key")
		}
	})

	// Test Update
	t.Run("Update", func(t *testing.T) {
		newName := "updated-key"
		body := map[string]any{
			"name":    newName,
			"enabled": false,
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPut, "/hub/admin/access-keys/1", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		c.Params = gin.Params{{Key: "id", Value: "1"}}

		handler.HandleUpdateAccessKey(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/hub/admin/access-keys/1", nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		c.Params = gin.Params{{Key: "id", Value: "1"}}

		handler.HandleDeleteAccessKey(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	})
}
