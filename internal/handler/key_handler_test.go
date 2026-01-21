package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestValidateGroupIDFromQuery(t *testing.T) {
	tests := []struct {
		name        string
		queryParam  string
		expectValid bool
		expectedID  uint
	}{
		{
			name:        "valid group ID",
			queryParam:  "group_id=123",
			expectValid: true,
			expectedID:  123,
		},
		{
			name:        "missing group ID",
			queryParam:  "",
			expectValid: false,
			expectedID:  0,
		},
		{
			name:        "invalid format",
			queryParam:  "group_id=abc",
			expectValid: false,
			expectedID:  0,
		},
		{
			name:        "zero group ID",
			queryParam:  "group_id=0",
			expectValid: false,
			expectedID:  0,
		},
		{
			name:        "negative group ID",
			queryParam:  "group_id=-1",
			expectValid: false,
			expectedID:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/test?"+tt.queryParam, nil)

			groupID, valid := validateGroupIDFromQuery(c)

			assert.Equal(t, tt.expectValid, valid)
			if tt.expectValid {
				assert.Equal(t, tt.expectedID, groupID)
			}
		})
	}
}

func TestValidateKeysText(t *testing.T) {
	tests := []struct {
		name        string
		keysText    string
		expectValid bool
	}{
		{
			name:        "valid keys text",
			keysText:    "sk-test123\nsk-test456",
			expectValid: true,
		},
		{
			name:        "empty string",
			keysText:    "",
			expectValid: false,
		},
		{
			name:        "whitespace only",
			keysText:    "   \n\t  ",
			expectValid: false,
		},
		{
			name:        "single key",
			keysText:    "sk-test123",
			expectValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			valid := validateKeysText(c, tt.keysText)

			assert.Equal(t, tt.expectValid, valid)
		})
	}
}

func TestKeyTextRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request KeyTextRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: KeyTextRequest{
				GroupID:  1,
				KeysText: "sk-test123",
			},
			wantErr: false,
		},
		{
			name: "missing group ID",
			request: KeyTextRequest{
				GroupID:  0,
				KeysText: "sk-test123",
			},
			wantErr: true,
		},
		{
			name: "missing keys text",
			request: KeyTextRequest{
				GroupID:  1,
				KeysText: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req KeyTextRequest
			err := c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.request.GroupID, req.GroupID)
				assert.Equal(t, tt.request.KeysText, req.KeysText)
			}
		})
	}
}

func TestGroupIDRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request GroupIDRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: GroupIDRequest{
				GroupID: 1,
			},
			wantErr: false,
		},
		{
			name: "missing group ID",
			request: GroupIDRequest{
				GroupID: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req GroupIDRequest
			err := c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.request.GroupID, req.GroupID)
			}
		})
	}
}

func TestValidateGroupKeysRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request ValidateGroupKeysRequest
		wantErr bool
	}{
		{
			name: "valid request with status",
			request: ValidateGroupKeysRequest{
				GroupID: 1,
				Status:  "active",
			},
			wantErr: false,
		},
		{
			name: "valid request without status",
			request: ValidateGroupKeysRequest{
				GroupID: 1,
			},
			wantErr: false,
		},
		{
			name: "missing group ID",
			request: ValidateGroupKeysRequest{
				GroupID: 0,
				Status:  "active",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req ValidateGroupKeysRequest
			err := c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.request.GroupID, req.GroupID)
				assert.Equal(t, tt.request.Status, req.Status)
			}
		})
	}
}
