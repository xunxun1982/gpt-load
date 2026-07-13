package handler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDashboardRPMStatsUsesReadDBWhenWritePoolIsBusy(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "dashboard-read-contention.db") +
		"?_busy_timeout=100&_journal_mode=WAL&_synchronous=NORMAL"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger:      logger.Default.LogMode(logger.Silent),
			PrepareStmt: false,
		})
		require.NoError(t, err)
		return db
	}

	writeDB := open()
	readDB := open()
	writeSQLDB, err := writeDB.DB()
	require.NoError(t, err)
	writeSQLDB.SetMaxOpenConns(1)
	writeSQLDB.SetMaxIdleConns(1)
	readSQLDB, err := readDB.DB()
	require.NoError(t, err)
	readSQLDB.SetMaxOpenConns(2)
	readSQLDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = readSQLDB.Close()
		_ = writeSQLDB.Close()
	})

	require.NoError(t, writeDB.AutoMigrate(&models.RequestLog{}))
	now := time.Now()
	require.NoError(t, writeDB.Create(&models.RequestLog{
		ID:          "dashboard-read-pool",
		Timestamp:   now.Add(-5 * time.Minute),
		RequestType: models.RequestTypeFinal,
	}).Error)

	conn, err := writeSQLDB.Conn(context.Background())
	require.NoError(t, err)
	released := make(chan struct{})
	go func() {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_ = conn.Close()
		close(released)
	}()

	server := &Server{DB: writeDB, readDB: readDB}
	startedAt := time.Now()
	stats, err := server.getRPMStats(now)
	elapsed := time.Since(startedAt)
	<-released

	require.NoError(t, err)
	require.Equal(t, 0.1, stats.Value)
	require.Less(t, elapsed, 200*time.Millisecond)
}
