package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestHealth_Success tests successful health check
func TestHealth_Success(t *testing.T) {
	t.Parallel()

	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer mockDB.Close()

	// First ping: GORM automatically pings during gorm.Open() initialization
	// Second ping: Health() handler calls PingContext() to verify database connectivity
	mock.ExpectPing()
	mock.ExpectPing()

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      mockDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	require.NoError(t, err)

	server := &Server{DB: gormDB}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)
	c.Set("serverStartTime", time.Now().Add(-5*time.Minute))

	server.Health(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "healthy", response["status"])
	assert.Equal(t, "ok", response["database"])
	assert.Contains(t, response, "timestamp")
	assert.Contains(t, response, "uptime")

	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestHealth_DatabaseUnavailable tests health check when database is unavailable
func TestHealth_DatabaseUnavailable(t *testing.T) {
	t.Parallel()

	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer mockDB.Close()

	// First ping: GORM automatically pings during gorm.Open() initialization (succeeds)
	// Second ping: Health() handler calls PingContext() to verify connectivity (fails)
	mock.ExpectPing()
	mock.ExpectPing().WillReturnError(sql.ErrConnDone)

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      mockDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	require.NoError(t, err)

	server := &Server{DB: gormDB}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)
	c.Set("serverStartTime", time.Now().Add(-5*time.Minute))

	server.Health(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "unhealthy", response["status"])
	assert.Equal(t, "unavailable", response["database"])

	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestHealth_NoDatabase tests health check when database is nil
func TestHealth_NoDatabase(t *testing.T) {
	t.Parallel()

	server := &Server{DB: nil}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)
	c.Set("serverStartTime", time.Now().Add(-5*time.Minute))

	server.Health(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "healthy", response["status"])
	assert.Equal(t, "ok", response["database"])
}

// TestHealth_UptimeCalculation tests uptime calculation
func TestHealth_UptimeCalculation(t *testing.T) {
	t.Parallel()

	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer mockDB.Close()

	// First ping: GORM automatically pings during gorm.Open() initialization
	// Second ping: Health() handler calls PingContext() to verify database connectivity
	mock.ExpectPing()
	mock.ExpectPing()

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      mockDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	require.NoError(t, err)

	server := &Server{DB: gormDB}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)

	startTime := time.Now().Add(-1 * time.Hour)
	c.Set("serverStartTime", startTime)

	server.Health(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	uptime, ok := response["uptime"].(string)
	require.True(t, ok)
	assert.Contains(t, uptime, "h")

	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestHealth_NoStartTime tests health check without start time
func TestHealth_NoStartTime(t *testing.T) {
	t.Parallel()

	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer mockDB.Close()

	// First ping: GORM automatically pings during gorm.Open() initialization
	// Second ping: Health() handler calls PingContext() to verify database connectivity
	mock.ExpectPing()
	mock.ExpectPing()

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      mockDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	require.NoError(t, err)

	server := &Server{DB: gormDB}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)

	server.Health(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "unknown", response["uptime"])

	assert.NoError(t, mock.ExpectationsWereMet())
}
