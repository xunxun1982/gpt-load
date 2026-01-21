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

func TestCreateManagedSiteRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request CreateManagedSiteRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: CreateManagedSiteRequest{
				Name:     "Test Site",
				BaseURL:  "https://example.com",
				SiteType: "api",
				Enabled:  true,
			},
			wantErr: false,
		},
		{
			name: "with check-in enabled",
			request: CreateManagedSiteRequest{
				Name:             "Test Site",
				BaseURL:          "https://example.com",
				SiteType:         "api",
				CheckInAvailable: true,
				CheckInEnabled:   true,
				CheckInPageURL:   "https://example.com/checkin",
			},
			wantErr: false,
		},
		{
			name: "with proxy",
			request: CreateManagedSiteRequest{
				Name:     "Test Site",
				BaseURL:  "https://example.com",
				SiteType: "api",
				UseProxy: true,
				ProxyURL: "http://proxy.example.com:8080",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/sites", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req CreateManagedSiteRequest
			err = c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.request.Name, req.Name)
				assert.Equal(t, tt.request.BaseURL, req.BaseURL)
			}
		})
	}
}

func TestUpdateManagedSiteRequest_Validation(t *testing.T) {
	name := "Updated Site"
	enabled := false
	sort := 10

	tests := []struct {
		name    string
		request UpdateManagedSiteRequest
		wantErr bool
	}{
		{
			name: "update name only",
			request: UpdateManagedSiteRequest{
				Name: &name,
			},
			wantErr: false,
		},
		{
			name: "update enabled status",
			request: UpdateManagedSiteRequest{
				Enabled: &enabled,
			},
			wantErr: false,
		},
		{
			name: "update sort order",
			request: UpdateManagedSiteRequest{
				Sort: &sort,
			},
			wantErr: false,
		},
		{
			name:    "empty update",
			request: UpdateManagedSiteRequest{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/sites/1", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req UpdateManagedSiteRequest
			err = c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.request.Name != nil {
					assert.Equal(t, *tt.request.Name, *req.Name)
				}
			}
		})
	}
}
