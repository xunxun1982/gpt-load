package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestListProxyPoolReturnsPaginatedResponse(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ProxyPoolItem{}))
	require.NoError(t, db.Create(&[]models.ProxyPoolItem{
		{Name: "proxy-a", URL: "http://127.0.0.1:7001"},
		{Name: "proxy-b", URL: "http://127.0.0.1:7002"},
		{Name: "proxy-c", URL: "http://127.0.0.1:7003"},
	}).Error)

	server := &Server{ProxyPoolService: services.NewProxyPoolService(db)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/proxy-pool?page=1&page_size=2", nil)

	server.ListProxyPool(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Code int `json:"code"`
		Data struct {
			Items      []models.ProxyPoolItem `json:"items"`
			Pagination struct {
				Page       int   `json:"page"`
				PageSize   int   `json:"page_size"`
				TotalItems int64 `json:"total_items"`
				TotalPages int   `json:"total_pages"`
				HasMore    bool  `json:"has_more"`
			} `json:"pagination"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Len(t, body.Data.Items, 2)
	require.Equal(t, "proxy-a", body.Data.Items[0].Name)
	require.Equal(t, "proxy-b", body.Data.Items[1].Name)
	require.Equal(t, 1, body.Data.Pagination.Page)
	require.Equal(t, 2, body.Data.Pagination.PageSize)
	require.Equal(t, int64(-1), body.Data.Pagination.TotalItems)
	require.Equal(t, -1, body.Data.Pagination.TotalPages)
	require.True(t, body.Data.Pagination.HasMore)
}

func TestListProxyPoolSelectionOptionsIncludesManualProxiesOnly(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ProxyPoolItem{}))
	require.NoError(t, db.Create(&models.ProxyPoolItem{Name: "manual", URL: "http://manual.example.com:8080"}).Error)

	server := &Server{ProxyPoolService: services.NewProxyPoolService(db)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/proxy-pool/selection-options", nil)

	server.ListProxyPoolSelectionOptions(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Code int `json:"code"`
		Data []struct {
			Type  string `json:"type"`
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Len(t, body.Data, 1)
	require.Equal(t, "manual", body.Data[0].Type)
	require.Equal(t, utils.BuildProxyPoolItemRef(1), body.Data[0].Value)
}

func TestListProxyPoolSelectionOptionsSanitizesManualProxyCredentials(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ProxyPoolItem{}))
	svc := services.NewProxyPoolService(db)
	item, err := svc.Create(context.Background(), services.ProxyPoolInput{
		Name: "secret proxy",
		URL:  "http://user:pass@manual.example.com:8080",
	})
	require.NoError(t, err)

	server := &Server{ProxyPoolService: svc}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/proxy-pool/selection-options", nil)

	server.ListProxyPoolSelectionOptions(c)

	require.Equal(t, http.StatusOK, w.Code)
	bodyText := w.Body.String()
	require.NotContains(t, bodyText, "user:pass")
	require.Contains(t, bodyText, utils.BuildProxyPoolItemRef(item.ID))
	require.Contains(t, bodyText, "http://manual.example.com:8080")
}

func TestListGatewayProxyOptions(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	server := &Server{ProxyPoolService: services.NewProxyPoolService(db)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/proxy-pool/gateway-options", nil)

	server.ListGatewayProxyOptions(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Code int `json:"code"`
		Data []struct {
			Type        string `json:"type"`
			Label       string `json:"label"`
			Value       string `json:"value"`
			CandidateID string `json:"candidate_id"`
			URL         string `json:"url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Len(t, body.Data, 5)
	require.Equal(t, "gateway", body.Data[0].Type)
	require.Equal(t, "betterclaude", body.Data[0].Value)
	require.Equal(t, "betterclaude-default", body.Data[0].CandidateID)
	require.Equal(t, "https://betterclau.de", body.Data[0].URL)
}
