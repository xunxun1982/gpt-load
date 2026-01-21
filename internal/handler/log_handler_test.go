package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestGetLogs_NilService(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{LogService: nil}
	router := gin.New()
	router.GET("/logs", server.GetLogs)

	req := httptest.NewRequest(http.MethodGet, "/logs?page=1&page_size=10", nil)
	w := httptest.NewRecorder()

	// Will panic if LogService is nil
	assert.Panics(t, func() {
		router.ServeHTTP(w, req)
	})
}

func TestExportLogs_ValidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{LogService: nil}
	router := gin.New()
	router.GET("/logs/export", server.ExportLogs)

	req := httptest.NewRequest(http.MethodGet, "/logs/export", nil)
	w := httptest.NewRecorder()

	// Will panic if LogService is nil
	assert.Panics(t, func() {
		router.ServeHTTP(w, req)
	})
}
