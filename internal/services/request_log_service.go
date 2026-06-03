package services

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"
	"strings"
	"sync"
	"sync/atomic"
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

type modelTokenStatKey struct {
	Time          time.Time
	GroupID       uint
	ParentGroupID uint
	Model         string
}

type modelTokenStatCounts struct {
	RequestCount          int64
	InputTokens           int64
	OutputTokens          int64
	TotalTokens           int64
	CacheReadTokens       int64
	CacheWriteTokens      int64
	ThinkingTokens        int64
	EstimatedTokens       int64
	EstimatedRequestCount int64
}

type apiKeyStatsKey struct {
	GroupID uint
	KeyHash string
}

type apiKeyStats struct {
	Count      int64
	LastUsedAt time.Time
}

const (
	RequestLogCachePrefix    = "request_log:"
	PendingLogKeysSet        = "pending_log_keys"
	DefaultLogFlushBatchSize = 200
	// MaxPendingLogs is the maximum number of logs that can be pending in memory
	// If this limit is reached, new logs will be dropped to prevent memory exhaustion
	// Set to 10000 to handle ~10MB of log data (assuming ~1KB per log)
	MaxPendingLogs = 10000
)

// RequestLogService is responsible for managing request logs.
type RequestLogService struct {
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	stopChan        chan struct{}
	wg              sync.WaitGroup
	ticker          *time.Ticker
	droppedLogs     int64 // Counter for dropped logs due to memory pressure
	pendingCount    int64 // Approximate count of pending logs (updated on flush)
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
	// Initialize pendingCount from persistent store to maintain accuracy across restarts
	// This prevents MaxPendingLogs checks from being bypassed after restart
	if card, err := s.store.SCard(PendingLogKeysSet); err != nil {
		logrus.WithError(err).Warn("Failed to get pending log count from store, starting with 0")
	} else {
		atomic.StoreInt64(&s.pendingCount, card)
		if card > 0 {
			logrus.Infof("Initialized pending log count: %d logs from previous session", card)
		}
	}

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

	// Emergency flush ticker - runs every 30 seconds to check for memory pressure
	// This provides a safety net if regular flush is delayed or failing
	emergencyTicker := time.NewTicker(30 * time.Second)
	defer emergencyTicker.Stop()

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
		case <-emergencyTicker.C:
			// Check if we're under memory pressure and force flush if needed
			// Use atomic counter for fast check without store query
			pendingCount := atomic.LoadInt64(&s.pendingCount)
			if pendingCount > MaxPendingLogs/2 {
				logrus.Warnf("Emergency flush triggered: %d pending logs (threshold: %d)", pendingCount, MaxPendingLogs/2)
				s.flush()
			}
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

// Record logs a request to the database and cache.
// Uses pooled JSON encoder for efficient memory allocation in high-frequency scenarios.
// Implements backpressure: drops logs if pending count exceeds MaxPendingLogs to prevent memory exhaustion.
func (s *RequestLogService) Record(log *models.RequestLog) error {
	log.ID = uuid.NewString()
	log.Timestamp = time.Now()

	settings := s.settingsManager.GetSettings()
	if settings.RequestLogWriteIntervalMinutes == 0 {
		return s.writeLogsToDB([]*models.RequestLog{log})
	}

	// Fast path: check approximate pending count using atomic counter
	// This avoids expensive LLen call on every request
	approxPending := atomic.LoadInt64(&s.pendingCount)
	if approxPending >= MaxPendingLogs {
		dropped := atomic.AddInt64(&s.droppedLogs, 1)
		if dropped%100 == 1 { // Log every 100 drops to avoid log spam
			logrus.Warnf("Dropping request log due to memory pressure (approx pending: %d, dropped total: %d)", approxPending, dropped)
		}
		return nil
	}

	cacheKey := RequestLogCachePrefix + log.ID

	// Use pooled JSON encoder to reduce memory allocations
	logBytes, err := utils.MarshalJSON(log)
	if err != nil {
		return fmt.Errorf("failed to marshal request log: %w", err)
	}

	// Reduce TTL from 5x to 3x flush interval to free memory faster
	// This is safe because flush runs every interval, so 3x provides adequate buffer
	ttl := time.Duration(settings.RequestLogWriteIntervalMinutes*3) * time.Minute
	if err := s.store.Set(cacheKey, logBytes, ttl); err != nil {
		return err
	}

	// Add to pending set; cleanup orphaned cache entry if SAdd fails
	// to prevent memory leaks from untracked entries
	if err := s.store.SAdd(PendingLogKeysSet, cacheKey); err != nil {
		// Best-effort cleanup: delete the orphaned cache entry
		if delErr := s.store.Del(cacheKey); delErr != nil {
			logrus.WithError(delErr).Warnf("Failed to cleanup orphaned log cache key %s", cacheKey)
		}
		return err
	}

	// Increment approximate counter
	atomic.AddInt64(&s.pendingCount, 1)
	return nil
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
		missingCount := 0 // Track missing keys for pendingCount adjustment
		for _, key := range keys {
			logBytes, err := s.store.Get(key)
			if err != nil {
				if err == store.ErrNotFound {
					logrus.Warnf("Log key %s found in set but not in store, skipping.", key)
					missingCount++
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
			// Decrement pendingCount regardless of Del success since keys are already popped from set
			// This prevents counter drift when Del fails but keys are already removed from tracking set
			if len(badKeys) > 0 {
				if err := s.store.Del(badKeys...); err != nil {
					logrus.WithError(err).Error("Failed to delete corrupted log bodies from store")
				}
				// Decrement regardless of Del success since keys are already popped from set
				atomic.AddInt64(&s.pendingCount, -int64(len(badKeys)))
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
			// Decrement pendingCount for missing keys to prevent counter drift
			if missingCount > 0 {
				atomic.AddInt64(&s.pendingCount, -int64(missingCount))
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
			// Decrement pendingCount regardless of Del success since keys are already popped from set
			// This prevents counter drift when Del fails but keys are already removed from tracking set
			if len(badKeys) > 0 {
				if delErr := s.store.Del(badKeys...); delErr != nil {
					logrus.WithError(delErr).Error("Failed to delete corrupted log bodies from store")
				}
				// Decrement regardless of Del success since keys are already popped from set
				atomic.AddInt64(&s.pendingCount, -int64(len(badKeys)))
			}
			// Decrement pendingCount for missing keys to prevent counter drift
			if missingCount > 0 {
				atomic.AddInt64(&s.pendingCount, -int64(missingCount))
			}
			return
		}

		// Decrement pendingCount regardless of Del success since keys are already popped from set
		// and logs are written to DB. Orphaned cache entries will TTL out.
		// This prevents counter drift when Del fails but keys are already removed from tracking set.
		if len(processedKeys) > 0 {
			if err := s.store.Del(processedKeys...); err != nil {
				logrus.Errorf("Failed to delete flushed log bodies from store: %v", err)
			}
			atomic.AddInt64(&s.pendingCount, -int64(len(processedKeys)))
		}
		if len(badKeys) > 0 {
			if err := s.store.Del(badKeys...); err != nil {
				logrus.WithError(err).Error("Failed to delete corrupted log bodies from store")
			}
			atomic.AddInt64(&s.pendingCount, -int64(len(badKeys)))
		}
		// Decrement for missing keys (they were never in cache, so safe to decrement)
		if missingCount > 0 {
			atomic.AddInt64(&s.pendingCount, -int64(missingCount))
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

		keyStats := make(map[apiKeyStatsKey]apiKeyStats, len(logs)/2)
		for _, log := range logs {
			if log.IsSuccess && log.GroupID > 0 && log.KeyHash != "" {
				key := apiKeyStatsKey{GroupID: log.GroupID, KeyHash: log.KeyHash}
				stats := keyStats[key]
				stats.Count++
				if log.Timestamp.After(stats.LastUsedAt) {
					stats.LastUsedAt = log.Timestamp
				}
				keyStats[key] = stats
			}
		}

		if len(keyStats) > 0 {
			if err := updateAPIKeyStats(tx, keyStats); err != nil {
				return err
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

		modelTokenStats := aggregateModelTokenStats(logs)
		if len(modelTokenStats) > 0 {
			if err := s.batchUpsertModelTokenStats(tx, modelTokenStats); err != nil {
				return err
			}
		}

		return nil
	})
}

func aggregateModelTokenStats(logs []*models.RequestLog) map[modelTokenStatKey]modelTokenStatCounts {
	stats := make(map[modelTokenStatKey]modelTokenStatCounts, len(logs)/4)
	for _, log := range logs {
		if log.RequestType != models.RequestTypeFinal || log.GroupID == 0 || log.TotalTokens <= 0 {
			continue
		}
		hourlyTime := log.Timestamp.Truncate(time.Hour)
		key := modelTokenStatKey{
			Time:          hourlyTime,
			GroupID:       log.GroupID,
			ParentGroupID: log.ParentGroupID,
			Model:         normalizeTokenUsageModel(log.Model),
		}
		counts := stats[key]
		counts.RequestCount++
		counts.InputTokens += log.InputTokens
		counts.OutputTokens += log.OutputTokens
		counts.TotalTokens += log.TotalTokens
		counts.CacheReadTokens += log.CacheReadTokens
		counts.CacheWriteTokens += log.CacheWriteTokens
		counts.ThinkingTokens += log.ThinkingTokens
		if log.TokenUsageSource == models.TokenUsageSourceEstimated {
			counts.EstimatedTokens += log.TotalTokens
			counts.EstimatedRequestCount++
		}
		stats[key] = counts
	}
	return stats
}

func normalizeTokenUsageModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

func updateAPIKeyStats(tx *gorm.DB, keyStats map[apiKeyStatsKey]apiKeyStats) error {
	if len(keyStats) == 0 {
		return nil
	}

	countCase := utils.GetStringBuilder()
	defer utils.PutStringBuilder(countCase)
	lastUsedCase := utils.GetStringBuilder()
	defer utils.PutStringBuilder(lastUsedCase)

	countArgs := make([]any, 0, len(keyStats)*3)
	lastUsedArgs := make([]any, 0, len(keyStats)*4)
	whereParts := make([]string, 0, len(keyStats))
	whereArgs := make([]any, 0, len(keyStats)*2)

	countCase.WriteString("CASE ")
	lastUsedCase.WriteString("CASE ")

	for key, stats := range keyStats {
		countCase.WriteString("WHEN group_id = ? AND key_hash = ? THEN request_count + ? ")
		countArgs = append(countArgs, key.GroupID, key.KeyHash, stats.Count)

		lastUsedCase.WriteString("WHEN group_id = ? AND key_hash = ? THEN CASE WHEN last_used_at IS NULL OR last_used_at < ? THEN ? ELSE last_used_at END ")
		lastUsedArgs = append(lastUsedArgs, key.GroupID, key.KeyHash, stats.LastUsedAt, stats.LastUsedAt)

		whereParts = append(whereParts, "(group_id = ? AND key_hash = ?)")
		whereArgs = append(whereArgs, key.GroupID, key.KeyHash)
	}

	countCase.WriteString("ELSE request_count END")
	lastUsedCase.WriteString("ELSE last_used_at END")

	if err := tx.Model(&models.APIKey{}).Where(strings.Join(whereParts, " OR "), whereArgs...).
		Updates(map[string]any{
			"request_count": gorm.Expr(countCase.String(), countArgs...),
			"last_used_at":  gorm.Expr(lastUsedCase.String(), lastUsedArgs...),
		}).Error; err != nil {
		return fmt.Errorf("failed to batch update api_key stats: %w", err)
	}
	return nil
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
	// Note: GORM's postgres driver (gorm.io/driver/postgres) returns "postgres" from Dialector.Name(),
	// not "pgx", even though it uses pgx internally. Verified in GORM source code.
	dialect := tx.Dialector.Name()
	switch dialect {
	case "postgres":
		return s.batchUpsertHourlyStatsPostgres(tx, stats)
	case "mysql":
		return s.batchUpsertHourlyStatsMySQL(tx, stats)
	case "sqlite":
		return s.batchUpsertHourlyStatsSQLite(tx, stats)
	default:
		// Unknown dialect, fall back to SQLite implementation with warning
		logrus.Warnf("Unknown database dialect '%s', falling back to SQLite upsert strategy", dialect)
		return s.batchUpsertHourlyStatsSQLite(tx, stats)
	}
}

// batchUpsertHourlyStatsPostgres performs batch upsert for PostgreSQL.
// Uses GORM's OnConflict clause which generates efficient ON CONFLICT DO UPDATE.
func (s *RequestLogService) batchUpsertHourlyStatsPostgres(tx *gorm.DB, stats []models.GroupHourlyStat) error {
	// PostgreSQL supports batch upsert with ON CONFLICT
	// Process in batches to avoid parameter limit (65535)

	for i := 0; i < len(stats); i += HourlyStatsBatchSize {
		end := i + HourlyStatsBatchSize
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
// AI Review Note: MySQL 8.0.20+ deprecated VALUES() in ON DUPLICATE KEY UPDATE.
// Decision: Keep using VALUES() because:
// 1. GORM's clause.OnConflict doesn't support the new row alias syntax (AS new_row)
// 2. VALUES() still works in MySQL 8.0.20+ (deprecated != removed)
// 3. Using raw SQL would lose GORM's type safety and batch processing benefits
// 4. When GORM adds support for row alias syntax, we can update this code
// Reference: https://dev.mysql.com/doc/refman/8.0/en/insert-on-duplicate.html
func (s *RequestLogService) batchUpsertHourlyStatsMySQL(tx *gorm.DB, stats []models.GroupHourlyStat) error {
	// MySQL supports batch upsert with ON DUPLICATE KEY UPDATE
	// Process in batches to stay within max_allowed_packet

	for i := 0; i < len(stats); i += HourlyStatsBatchSize {
		end := i + HourlyStatsBatchSize
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

	for i := 0; i < len(stats); i += HourlyStatsBatchSizeSQLite {
		end := i + HourlyStatsBatchSizeSQLite
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]

		// SQLite supports ON CONFLICT since version 3.24.0 (2018)
		// Use CreateInBatches for consistency with other database implementations
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"success_count": gorm.Expr("group_hourly_stats.success_count + excluded.success_count"),
				"failure_count": gorm.Expr("group_hourly_stats.failure_count + excluded.failure_count"),
				"updated_at":    gorm.Expr("excluded.updated_at"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert hourly stats (sqlite): %w", err)
		}
	}
	return nil
}

// batchUpsertModelTokenStats performs batch upsert for model token statistics.
func (s *RequestLogService) batchUpsertModelTokenStats(tx *gorm.DB, modelStats map[modelTokenStatKey]modelTokenStatCounts) error {
	if len(modelStats) == 0 {
		return nil
	}

	now := time.Now()
	stats := make([]models.ModelTokenHourlyStat, 0, len(modelStats))
	for key, counts := range modelStats {
		stats = append(stats, models.ModelTokenHourlyStat{
			Time:                  key.Time,
			GroupID:               key.GroupID,
			ParentGroupID:         key.ParentGroupID,
			Model:                 key.Model,
			RequestCount:          counts.RequestCount,
			InputTokens:           counts.InputTokens,
			OutputTokens:          counts.OutputTokens,
			TotalTokens:           counts.TotalTokens,
			CacheReadTokens:       counts.CacheReadTokens,
			CacheWriteTokens:      counts.CacheWriteTokens,
			ThinkingTokens:        counts.ThinkingTokens,
			EstimatedTokens:       counts.EstimatedTokens,
			EstimatedRequestCount: counts.EstimatedRequestCount,
			CreatedAt:             now,
			UpdatedAt:             now,
		})
	}

	dialect := tx.Dialector.Name()
	switch dialect {
	case "postgres":
		return s.batchUpsertModelTokenStatsPostgres(tx, stats)
	case "mysql":
		return s.batchUpsertModelTokenStatsMySQL(tx, stats)
	case "sqlite":
		return s.batchUpsertModelTokenStatsSQLite(tx, stats)
	default:
		logrus.WithField("dialect", dialect).Warn("Unknown database dialect for token stats, falling back to SQLite strategy")
		return s.batchUpsertModelTokenStatsSQLite(tx, stats)
	}
}

func (s *RequestLogService) batchUpsertModelTokenStatsPostgres(tx *gorm.DB, stats []models.ModelTokenHourlyStat) error {
	for i := 0; i < len(stats); i += HourlyStatsBatchSize {
		end := i + HourlyStatsBatchSize
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}, {Name: "parent_group_id"}, {Name: "model"}},
			DoUpdates: clause.Assignments(map[string]any{
				"request_count":           gorm.Expr("model_token_hourly_stats.request_count + EXCLUDED.request_count"),
				"input_tokens":            gorm.Expr("model_token_hourly_stats.input_tokens + EXCLUDED.input_tokens"),
				"output_tokens":           gorm.Expr("model_token_hourly_stats.output_tokens + EXCLUDED.output_tokens"),
				"total_tokens":            gorm.Expr("model_token_hourly_stats.total_tokens + EXCLUDED.total_tokens"),
				"cache_read_tokens":       gorm.Expr("model_token_hourly_stats.cache_read_tokens + EXCLUDED.cache_read_tokens"),
				"cache_write_tokens":      gorm.Expr("model_token_hourly_stats.cache_write_tokens + EXCLUDED.cache_write_tokens"),
				"thinking_tokens":         gorm.Expr("model_token_hourly_stats.thinking_tokens + EXCLUDED.thinking_tokens"),
				"estimated_tokens":        gorm.Expr("model_token_hourly_stats.estimated_tokens + EXCLUDED.estimated_tokens"),
				"estimated_request_count": gorm.Expr("model_token_hourly_stats.estimated_request_count + EXCLUDED.estimated_request_count"),
				"updated_at":              gorm.Expr("EXCLUDED.updated_at"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert model token stats (postgres): %w", err)
		}
	}
	return nil
}

func (s *RequestLogService) batchUpsertModelTokenStatsMySQL(tx *gorm.DB, stats []models.ModelTokenHourlyStat) error {
	for i := 0; i < len(stats); i += HourlyStatsBatchSize {
		end := i + HourlyStatsBatchSize
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}, {Name: "parent_group_id"}, {Name: "model"}},
			DoUpdates: clause.Assignments(map[string]any{
				"request_count":           gorm.Expr("request_count + VALUES(request_count)"),
				"input_tokens":            gorm.Expr("input_tokens + VALUES(input_tokens)"),
				"output_tokens":           gorm.Expr("output_tokens + VALUES(output_tokens)"),
				"total_tokens":            gorm.Expr("total_tokens + VALUES(total_tokens)"),
				"cache_read_tokens":       gorm.Expr("cache_read_tokens + VALUES(cache_read_tokens)"),
				"cache_write_tokens":      gorm.Expr("cache_write_tokens + VALUES(cache_write_tokens)"),
				"thinking_tokens":         gorm.Expr("thinking_tokens + VALUES(thinking_tokens)"),
				"estimated_tokens":        gorm.Expr("estimated_tokens + VALUES(estimated_tokens)"),
				"estimated_request_count": gorm.Expr("estimated_request_count + VALUES(estimated_request_count)"),
				"updated_at":              gorm.Expr("VALUES(updated_at)"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert model token stats (mysql): %w", err)
		}
	}
	return nil
}

func (s *RequestLogService) batchUpsertModelTokenStatsSQLite(tx *gorm.DB, stats []models.ModelTokenHourlyStat) error {
	for i := 0; i < len(stats); i += HourlyStatsBatchSizeSQLite {
		end := i + HourlyStatsBatchSizeSQLite
		if end > len(stats) {
			end = len(stats)
		}
		batch := stats[i:end]
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "time"}, {Name: "group_id"}, {Name: "parent_group_id"}, {Name: "model"}},
			DoUpdates: clause.Assignments(map[string]any{
				"request_count":           gorm.Expr("model_token_hourly_stats.request_count + excluded.request_count"),
				"input_tokens":            gorm.Expr("model_token_hourly_stats.input_tokens + excluded.input_tokens"),
				"output_tokens":           gorm.Expr("model_token_hourly_stats.output_tokens + excluded.output_tokens"),
				"total_tokens":            gorm.Expr("model_token_hourly_stats.total_tokens + excluded.total_tokens"),
				"cache_read_tokens":       gorm.Expr("model_token_hourly_stats.cache_read_tokens + excluded.cache_read_tokens"),
				"cache_write_tokens":      gorm.Expr("model_token_hourly_stats.cache_write_tokens + excluded.cache_write_tokens"),
				"thinking_tokens":         gorm.Expr("model_token_hourly_stats.thinking_tokens + excluded.thinking_tokens"),
				"estimated_tokens":        gorm.Expr("model_token_hourly_stats.estimated_tokens + excluded.estimated_tokens"),
				"estimated_request_count": gorm.Expr("model_token_hourly_stats.estimated_request_count + excluded.estimated_request_count"),
				"updated_at":              gorm.Expr("excluded.updated_at"),
			}),
		}).CreateInBatches(batch, len(batch)).Error; err != nil {
			return fmt.Errorf("failed to batch upsert model token stats (sqlite): %w", err)
		}
	}
	return nil
}
