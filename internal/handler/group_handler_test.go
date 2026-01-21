package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCreateGroup_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("POST", "/api/groups", bytes.NewBufferString("invalid json"))
	c.Request.Header.Set("Content-Type", "application/json")

	s.CreateGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateGroup_InvalidID(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "invalid"}}
	c.Request = httptest.NewRequest("PUT", "/api/groups/invalid", nil)

	s.UpdateGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateGroup_NegativeID(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "-1"}}
	c.Request = httptest.NewRequest("PUT", "/api/groups/-1", nil)

	s.UpdateGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateGroup_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest("PUT", "/api/groups/1", bytes.NewBufferString("{invalid}"))
	c.Request.Header.Set("Content-Type", "application/json")

	s.UpdateGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteGroup_InvalidID(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	c.Request = httptest.NewRequest("DELETE", "/api/groups/abc", nil)

	s.DeleteGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetGroupStats_InvalidID(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "invalid"}}
	c.Request = httptest.NewRequest("GET", "/api/groups/invalid/stats", nil)

	s.GetGroupStats(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCopyGroup_InvalidID(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	c.Request = httptest.NewRequest("POST", "/api/groups/abc/copy", nil)

	s.CopyGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCopyGroup_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := setupTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest("POST", "/api/groups/1/copy", bytes.NewBufferString("invalid"))
	c.Request.Header.Set("Content-Type", "application/json")

	s.CopyGroup(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
