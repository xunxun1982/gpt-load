package services

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// hourlyStatKey is the composite key for hourly statistics aggregation.
type hourlyStatKey struct {
	Time    time.Time
	GroupID uint
}

// hourlyStatCounts holds success and failure counts for aggregation.
type hourlyStatCounts struct {
	Success int64
	Failure int64
}

const (
	RequestLogCachePrefix    = "request_log:"
	PendingLogKeysSet        = "pending_log_keys"
	DefaultLogFlushBatchSize = 200
)

// RequestLogService is responsible for managing request logs.
type RequestLogService struct {
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	stopChan        chan struct{}
	wg              sync.WaitGroup
	ticker          *time.Ticker
}

// NewRequestLogService creates a new RequestLogService instance
func NewRequestLogService(db *gorm.DB, store store.Store, sm *config.SystemSettingsManager) *RequestLogService {
	return &RequestLogService{
		db:              db,
		store:           store,
		settingsManager: sm,
		stopChan:        make(chan struct{}),
	}
}

// Start initializes the service and starts the periodic flush routine
func (s *RequestLogService) Start() {
	s.wg.Add(1)
	go s.runLoop()
}

func (s *RequestLogService) runLoop() {
	defer s.wg.Done()

	// Initial flush on start
	s.flush()

	interval := time.Duration(s.settingsManager.GetSettings().RequestLogWriteIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Minute
	}
	s.ticker = time.NewTicker(interval)
	defer s.ticker.Stop()

	for {
		select {
		case <-s.ticker.C:
			newInterval := time.Duration(s.settingsManager.GetSettings().RequestLogWriteIntervalMinutes) * time.Minute
			if newInterval <= 0 {
				newInterval = time.Minute
			}
			if newInterval != interval {
				s.ticker.Reset(newInterval)
				interval = newInterval
				logrus.Debugf("Request log write interval updated to: %v", interval)
			}
			s.flush()
		case <-s.stopChan:
			return
		}
	}
}

// Stop gracefully stops the RequestLogService
func (s *RequestLogService) Stop(ctx context.Context) {
	close(s.stopChan)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.flush()
		logrus.Info("RequestLogService stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("RequestLogService stop timed out.")
	}
}

// Record logs a request to the database and cache
func (s *RequestLogService) Record(log *models.RequestLog) error {
	log.ID = uuid.NewString()
	log.Timestamp = time.Now()

	if s.settingsManager.GetSettings().RequestLogWriteIntervalMinutes == 0 {
		return s.writeLogsToDB([]*models.RequestLog{log})
	}

	cacheKey := RequestLogCachePrefix + log.ID

	logBytes, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("failed to marshal request log: %w", err)
	}

	ttl := time.Duration(s.settingsManager.GetSettings().RequestLogWriteIntervalMinutes*5) * time.Minute
	if err := s.store.Set(cacheKey, logBytes, ttl); err != nil {
		return err
	}

	return s.store.SAdd(PendingLogKeysSet, cacheKey)
}

// RecordError is a convenience method to record error logs with minimal parameters.
// It creates a RequestLog entry for failed requests (e.g., auth failures, early errors).
// Note: groupID=0 indicates "no group context" (e.g., when group lookup fails before group is known).
// Note: Input validation for statusCode/duration is intentionally omitted because all callers
// are internal code using valid http.StatusXXX constants and time.Since() which cannot be negative.
func (s *RequestLogService) RecordError(groupID uint, groupName, sourceIP, requestPath, errorMsg string, statusCode int, duration int64) {
	logEntry := &models.RequestLog{
		GroupID:      groupID,
		GroupName:    groupName,
		IsSuccess:    false,
		SourceIP:     sourceIP,
		StatusCode:   statusCode,
		RequestPath:  requestPath,
		Duration:     duration,
		RequestType:  models.RequestTypeFinal,
		IsStream:     false,
		ErrorMessage: errorMsg,
	}

	if err := s.Record(logEntry); err != nil {
		logrus.Errorf("Failed to record error log: %v", err)
	}
}

// flush data from cache to database
func (s *RequestLogService) flush() {
	if s.settingsManager.GetSettings().RequestLogWriteIntervalMinutes == 0 {
		logrus.Debug("Sync mode enabled, skipping scheduled log flush.")
		return
	}

	logrus.Debug("Master starting to flush request logs...")

	for {
		keys, err := s.store.SPopN(PendingLogKeysSet, DefaultLogFlushBatchSize)
		if err != nil {
			logrus.Errorf("Failed to pop pending log keys from store: %v", err)
			return
		}

		if len(keys) == 0 {
			return
		}

		logrus.Debugf("Popped %d request logs to flush.", len(keys))

		logs := make([]*models.RequestLog, 0, len(keys))
		processedKeys := make([]string, 0, len(keys))
		retryKeys := make([]string, 0, len(keys)/10)
		badKeys := make([]string, 0, len(keys)/50)
		for _, key := range keys {
			logBytes, err := s.store.Get(key)
			if err != nil {
				if err == store.ErrNotFound {
					logrus.Warnf("Log key %s found in set but not in store, skipping.", key)
				} else {
					logrus.Warnf("Failed to get log for key %s: %v", key, err)
					retryKeys = append(retryKeys, key)
				}
				continue
			}
			var log models.RequestLog
			if err := json.Unmarshal(logBytes, &log); err != nil {
				logrus.Warnf("Failed to unmarshal log for key %s: %v", key, err)
				badKeys = append(badKeys, key)
				continue
			}
			logs = append(logs, &log)
			processedKeys = append(processedKeys, key)
		}

		if len(logs) == 0 {
			if len(badKeys) > 0 {
				if err := s.store.Del(badKeys...); err != nil {
					logrus.WithError(err).Error("Failed to delete corrupted log bodies from store")
				}
			}
			if len(retryKeys) > 0 {
				args := make([]any, len(retryKeys))
				for i, k := range retryKeys {
					args[i] = k
				}
				if saddErr := s.store.SAdd(PendingLogKeysSet, args...); saddErr != nil {
					logrus.Errorf("CRITICAL: Failed to re-add unread log keys to set: %v", saddErr)
				}
			}
			continue
		}

		err = s.writeLogsToDB(logs)

		if err != nil {
			logrus.Errorf("Failed to flush request logs batch, will retry next time. Error: %v", err)
			// No pre-allocation needed: append() handles capacity internally, and this error path
			// prioritizes readability over micro-optimization.
			keysToRetry := append(processedKeys, retryKeys...)
			if len(keysToRetry) > 0 {
				args := make([]any, len(keysToRetry))
				for i, k := range keysToRetry {
					args[i] = k
				}
				if saddErr := s.store.SAdd(PendingLogKeysSet, args...); saddErr != nil {
					logrus.Errorf("CRITICAL: Failed to re-add failed log keys to set: %v", saddErr)
				}
			}
			if len(badKeys) > 0 {
				if delErr := s.store.Del(badKeys...); delErr != nil {
					logrus.WithError(delErr).Error("Failed to delete corrupted log bodies from store")
				}
			}
			return
		}

		if len(processedKeys) > 0 {
			if err := s.store.Del(processedKeys...); err != nil {
				logrus.Errorf("Failed to delete flushed log bodies from store: %v", err)
			}
		}
		if len(badKeys) > 0 {
			if err := s.store.Del(badKeys...); err != nil {
				logrus.WithError(err).Error("Failed to delete corrupted log bodies from store")
			}
		}
		if len(retryKeys) > 0 {
			args := make([]any, len(retryKeys))
			for i, k := range retryKeys {
				args[i] = k
			}
			if saddErr := s.store.SAdd(PendingLogKeysSet, args...); saddErr != nil {
				logrus.Errorf("CRITICAL: Failed to re-add unread log keys to set: %v", saddErr)
			}
		}
		logrus.Infof("Successfully flushed %d request logs.", len(logs))
	}
}

// writeLogsToDB writes a batch of request logs to the database
func (s *RequestLogService) writeLogsToDB(logs []*models.RequestLog) error {
	if len(logs) == 0 {
		return nil
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(logs, len(logs)).Error; err != nil {
			return fmt.Errorf("failed to batch insert request logs: %w", err)
		}

		keyStats := make(map[string]int64, len(logs)/2)
		for _, log := range logs {
			if log.IsSuccess && log.KeyHash != "" {
				keyStats[log.KeyHash]++
			}
		}

		if len(keyStats) > 0 {
			var caseStmt strings.Builder
			keyHashes := make([]string, 0, len(keyStats))
			// Pre-allocate capacity: ~50 bytes per CASE clause
			caseStmt.Grow(len(keyStats) * 50)
			caseStmt.WriteString("CASE key_hash ")
			for keyHash, count := range keyStats {
				caseStmt.WriteString("WHEN '")
				caseStmt.WriteString(keyHash)
				caseStmt.WriteString("' THEN request_count + ")
				caseStmt.WriteString(strconv.FormatInt(count, 10))
				caseStmt.WriteString(" ")
				keyHashes = append(keyHashes, keyHash)
			}
			caseStmt.WriteString("END")

			if err := tx.Model(&models.APIKey{}).Where("key_hash IN ?", keyHashes).
				Updates(map[string]any{
					"request_count": gorm.Expr(caseStmt.String()),
					"last_used_at":  time.Now(),
				}).Error; err != nil {
				return fmt.Errorf("failed to batch update api_key stats: %w", err)
			}
		}

		// Update statistics table using batch upsert
		hourlyStats := make(map[hourlyStatKey]hourlyStatCounts, len(logs)/10)
		for _, log := range logs {
			if log.RequestType == models.RequestTypeRetry {
				continue
			}
			hourlyTime := log.Timestamp.Truncate(time.Hour)
			key := hourlyStatKey{Time: hourlyTime, GroupID: log.GroupID}

			counts := hourlyStats[key]
			if log.IsSuccess {
				counts.Success++
			} else {
				counts.Failure++
			}
			hourlyStats[key] = counts

			if log.ParentGroupID > 0 {
				parentKey := hourlyStatKey{Time: hourlyTime, GroupID: log.ParentGroupID}

				parentCounts := hourlyStats[parentKey]
				if log.IsSuccess {
					parentCounts.Success++
				} else {
					parentCounts.Failure++
				}
				hourlyStats[parentKey] = parentCounts
			}
		}

		if len(hourlyStats) > 0 {
			if err := s.batchUpsertHourlyStats(tx, hourlyStats); err != nil {
				return err
			}
		}

		return nil
	})
}

// batchUpsertHourlyStats performs batch upsert for hourly statistics.
// Uses database-specific optimizations for best performance.
func (s *RequestLogService) batchUpsertHourlyStats(tx *gorm.DB, hourlyStats map[hourlyStatKey]hourlyStatCounts) error {
	if len(hourlyStats) == 0 {
		return nil
	}

	// Prepare batch data
	now := time.Now()
	stats := make([]models.GroupHourlyStat, 0, len(hourlyStats))
	for key, counts := range hourlyStats {
		stats = append(stats, models.GroupHourlyStat{
			Time:         key.Time,
			GroupID:      key.GroupID,
			SuccessCount: counts.Success,
			FailureCount: counts.Failure,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	// Detect database type and use appropriate batch upsert strategy
	dialect := tx.Dialector.Name()
	switch dialect {
	case "postgres", "pgx":
		return s.batchUpsertHourlyStatsPostgres(tx, stats)
	case "mysql":
		return s.batchUpsertHourlyStatsMySQL(tx, stats)
	default: // sqlite, sqlite3
		return s.batchUpsertHourlyStatsSQLite(tx, stats)
	}
}

// batchUpsertHourlyStatsPostgres performs batch upsert for PostgreSQL.
// Uses GORM's OnConflict clause which generates efficient ON CONFLICT DO UPDATE.
func (s *RequestLogService) batchUpsertHourlyStatsPostgres(tx *gorm.DB, stats []models.GroupHourlyStat) error {
	// PostgreSQL supports batch upsert with ON CONFLICT
	// Process in batches to avoid parameter limit (65535)
	const batchSize = 500

	for i := 0; i < len(stats); i += batchSize {
		end := i + batchSize
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]

		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"success_count": gorm.Expr("group_hourly_stats.success_count + EXCLUDED.success_count"),
				"failure_count": gorm.Expr("group_hourly_stats.failure_count + EXCLUDED.failure_count"),
				"updated_at":    gorm.Expr("EXCLUDED.updated_at"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert hourly stats (postgres): %w", err)
		}
	}
	return nil
}

// batchUpsertHourlyStatsMySQL performs batch upsert for MySQL.
// Uses GORM's OnConflict clause which generates ON DUPLICATE KEY UPDATE.
//
// AI Review Note: MySQL 8.0.20+ deprecated the VALUES() function in ON DUPLICATE KEY UPDATE.
// The new syntax uses row aliases (e.g., INSERT ... VALUES (...) AS new ON DUPLICATE KEY UPDATE col = new.col).
// However, GORM's clause.OnConflict does not natively support this new alias syntax.
// Migrating would require raw SQL or custom clause builders, adding significant complexity.
// The VALUES() function, while deprecated, still works in MySQL 8.x and is unlikely to be removed soon.
// We intentionally keep the current implementation for simplicity and GORM compatibility.
// If MySQL removes VALUES() in a future version, this will need to be updated to use raw SQL.
func (s *RequestLogService) batchUpsertHourlyStatsMySQL(tx *gorm.DB, stats []models.GroupHourlyStat) error {
	// MySQL supports batch upsert with ON DUPLICATE KEY UPDATE
	// Process in batches to stay within max_allowed_packet
	const batchSize = 500

	for i := 0; i < len(stats); i += batchSize {
		end := i + batchSize
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]

		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"success_count": gorm.Expr("success_count + VALUES(success_count)"),
				"failure_count": gorm.Expr("failure_count + VALUES(failure_count)"),
				"updated_at":    gorm.Expr("VALUES(updated_at)"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert hourly stats (mysql): %w", err)
		}
	}
	return nil
}

// batchUpsertHourlyStatsSQLite performs batch upsert for SQLite.
// SQLite has limited batch capabilities, so we use smaller batches with GORM's OnConflict.
func (s *RequestLogService) batchUpsertHourlyStatsSQLite(tx *gorm.DB, stats []models.GroupHourlyStat) error {
	// SQLite performs better with smaller batches due to its single-writer model
	const batchSize = 50

	for i := 0; i < len(stats); i += batchSize {
		end := i + batchSize
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]

		// SQLite supports ON CONFLICT since version 3.24.0 (2018)
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"success_count": gorm.Expr("group_hourly_stats.success_count + excluded.success_count"),
				"failure_count": gorm.Expr("group_hourly_stats.failure_count + excluded.failure_count"),
				"updated_at":    gorm.Expr("excluded.updated_at"),
			}),
		}).Create(&batch).Error; err != nil {
			return fmt.Errorf("failed to batch upsert hourly stats (sqlite): %w", err)
		}
	}
	return nil
}
