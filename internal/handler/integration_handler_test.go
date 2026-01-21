package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestServer_GetIntegrationInfo(t *testing.T) {
	tests := []struct {
		name           string
		queryKey       string
		path           string
		expectedStatus int
		expectError    bool
	}{
		{
			name:           "missing key",
			queryKey:       "",
			path:           "/integration",
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := setupTestServer(t)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tt.path+"?key="+tt.queryKey, nil)

			server.GetIntegrationInfo(c)

			if tt.expectError {
				assert.NotEqual(t, http.StatusOK, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestIntegrationGroupInfo_Structure(t *testing.T) {
	info := IntegrationGroupInfo{
		Name:        "test-group",
		DisplayName: "Test Group",
		ChannelType: "openai",
		Path:        "/proxy/test-group/v1/chat/completions",
	}

	assert.Equal(t, "test-group", info.Name)
	assert.Equal(t, "Test Group", info.DisplayName)
	assert.Equal(t, "openai", info.ChannelType)
	assert.NotEmpty(t, info.Path)
}

func TestIntegrationInfoResponse_Structure(t *testing.T) {
	response := IntegrationInfoResponse{
		Code:    200,
		Message: "success",
		Data: []IntegrationGroupInfo{
			{
				Name:        "group1",
				DisplayName: "Group 1",
				ChannelType: "openai",
				Path:        "/proxy/group1/v1/chat/completions",
			},
		},
	}

	assert.Equal(t, 200, response.Code)
	assert.Equal(t, "success", response.Message)
	assert.Len(t, response.Data, 1)
	assert.Equal(t, "group1", response.Data[0].Name)
}
