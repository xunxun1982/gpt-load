package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_GetEnvironmentInfo(t *testing.T) {
	server := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/environment", nil)

	server.GetEnvironmentInfo(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Contains(t, response, "code")
	assert.Contains(t, response, "data")
	data := response["data"].(map[string]interface{})
	assert.Contains(t, data, "debug_mode")
}

func TestServer_UpdateSettings(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    map[string]interface{}
		expectedStatus int
		expectError    bool
	}{
		{
			name:           "empty settings",
			requestBody:    map[string]interface{}{},
			expectedStatus: http.StatusOK,
			expectError:    false,
		},
		{
			name:           "invalid JSON",
			requestBody:    nil,
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := setupTestServer(t)

			var body []byte
			var err error
			if tt.requestBody != nil {
				body, err = json.Marshal(tt.requestBody)
				require.NoError(t, err)
			} else {
				body = []byte("invalid json")
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			server.UpdateSettings(c)

			if tt.expectError {
				assert.NotEqual(t, http.StatusOK, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestServer_GetSettings(t *testing.T) {
	server := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/settings", nil)

	server.GetSettings(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Contains(t, response, "code")
	assert.Contains(t, response, "data")

	// Data should be an array of categorized settings
	data, ok := response["data"].([]interface{})
	assert.True(t, ok)
	assert.NotEmpty(t, data)

	// Check structure of first category
	if len(data) > 0 {
		category := data[0].(map[string]interface{})
		assert.Contains(t, category, "category_name")
		assert.Contains(t, category, "settings")
	}
}

func TestServer_GetSettingsExcludesProxyPoolSettings(t *testing.T) {
	server := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/settings", nil)

	server.GetSettings(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	keys := collectSettingKeys(t, response)
	assert.NotContains(t, keys, "proxy_pool_test_target_url")
	assert.NotContains(t, keys, "proxy_pool_test_timeout_seconds")
	assert.NotContains(t, keys, "proxy_pool_auto_test_interval_minutes")
	assert.NotContains(t, keys, "gateway_proxy_test_timeout_seconds")
	assert.NotContains(t, keys, "gateway_proxy_auto_test_interval_minutes")
}

func TestServer_GetProxyPoolSettings(t *testing.T) {
	server := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/settings/proxy-pool", nil)

	server.GetProxyPoolSettings(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	data, ok := response["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "https://www.gstatic.com/generate_204", data["proxy_pool_test_target_url"])
	assert.EqualValues(t, 10, data["proxy_pool_test_timeout_seconds"])
	assert.EqualValues(t, 60, data["proxy_pool_auto_test_interval_minutes"])
	assert.EqualValues(t, 10, data["gateway_proxy_test_timeout_seconds"])
	assert.EqualValues(t, 60, data["gateway_proxy_auto_test_interval_minutes"])
}

func collectSettingKeys(t *testing.T, response map[string]interface{}) map[string]struct{} {
	t.Helper()

	data, ok := response["data"].([]interface{})
	require.True(t, ok)

	keys := make(map[string]struct{})
	for _, rawCategory := range data {
		category, ok := rawCategory.(map[string]interface{})
		require.True(t, ok)

		settings, ok := category["settings"].([]interface{})
		require.True(t, ok)
		for _, rawSetting := range settings {
			setting, ok := rawSetting.(map[string]interface{})
			require.True(t, ok)
			key, ok := setting["key"].(string)
			require.True(t, ok)
			keys[key] = struct{}{}
		}
	}
	return keys
}
