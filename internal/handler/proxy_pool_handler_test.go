package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/services"

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
