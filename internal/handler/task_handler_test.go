package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestGetTaskStatus_NilService(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{TaskService: nil}
	router := gin.New()
	router.GET("/task/status", server.GetTaskStatus)

	req := httptest.NewRequest(http.MethodGet, "/task/status", nil)
	w := httptest.NewRecorder()

	// Will panic if TaskService is nil
	assert.Panics(t, func() {
		router.ServeHTTP(w, req)
	})
}
