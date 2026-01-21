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

func TestBindGroupToSite_InvalidGroupID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/groups/:id/bind", server.BindGroupToSite)

	req := httptest.NewRequest(http.MethodPost, "/groups/invalid/bind", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBindGroupToSite_MissingSiteID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/groups/:id/bind", server.BindGroupToSite)

	reqBody := BindGroupToSiteRequest{SiteID: 0}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/groups/1/bind", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUnbindGroupFromSite_InvalidGroupID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.DELETE("/groups/:id/unbind", server.UnbindGroupFromSite)

	req := httptest.NewRequest(http.MethodDelete, "/groups/invalid/unbind", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetBoundSiteInfo_InvalidGroupID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.GET("/groups/:id/bound-site", server.GetBoundSiteInfo)

	req := httptest.NewRequest(http.MethodGet, "/groups/invalid/bound-site", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUnbindSiteFromGroup_InvalidSiteID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.DELETE("/sites/:id/unbind", server.UnbindSiteFromGroup)

	req := httptest.NewRequest(http.MethodDelete, "/sites/invalid/unbind", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetBoundGroupInfo_InvalidSiteID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.GET("/sites/:id/bound-groups", server.GetBoundGroupInfo)

	req := httptest.NewRequest(http.MethodGet, "/sites/invalid/bound-groups", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
