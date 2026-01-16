// Package services provides business logic services for the application.
package services

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Re-export store.ErrNotFound for local use
var errNotFound = store.ErrNotFound

const (
	// DefaultPersistenceInterval is the default interval for persisting metrics to database.
	DefaultPersistenceInterval = 1 * time.Minute
	// DefaultRolloverInterval is the default interval for rolling over time window statistics.
	DefaultRolloverInterval = 24 * time.Hour
	// SoftDeleteRetentionDays is how long soft-deleted records are kept before permanent deletion.
	SoftDeleteRetentionDays = 180
)

// DynamicWeightPersistence handles persistence of dynamic weight metrics to database.
// It maintains a dirty set of keys that need to be synced to database.
type DynamicWeightPersistence struct {
	db       *gorm.DB
	manager  *DynamicWeightManager
	interval time.Duration
	stopChan chan struct{}
	wg       sync.WaitGroup

	// dirtyKeys tracks keys that have been modified since last sync
	dirtyMu   sync.Mutex
	dirtyKeys map[string]struct{}

	// lastRollover tracks when the last rollover was performed
	lastRollover time.Time
	// lastCleanup tracks when the last cleanup was performed
	lastCleanup time.Time
}

// NewDynamicWeightPersistence creates a new persistence service.
func NewDynamicWeightPersistence(db *gorm.DB, manager *DynamicWeightManager) *DynamicWeightPersistence {
	p := &DynamicWeightPersistence{
		db:           db,
		manager:      manager,
		interval:     DefaultPersistenceInterval,
		stopChan:     make(chan struct{}),
		dirtyKeys:    make(map[string]struct{}),
		lastRollover: time.Now(),
		lastCleanup:  time.Now(),
	}
	// Set dirty callback on manager to enable automatic dirty tracking
	manager.SetDirtyCallback(p.MarkDirtyByKey)
	return p
}

// Start begins the periodic persistence routine.
func (p *DynamicWeightPersistence) Start() {
	p.wg.Add(1)
	go p.runLoop()
	logrus.Info("Dynamic weight persistence service started")
}

// Stop gracefully stops the persistence service and performs final sync.
func (p *DynamicWeightPersistence) Stop(ctx context.Context) {
	close(p.stopChan)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.syncDirtyKeys()
		logrus.Info("Dynamic weight persistence service stopped")
	case <-ctx.Done():
		logrus.Warn("Dynamic weight persistence service stop timed out")
	}
}

// runLoop runs the periodic persistence routine.
func (p *DynamicWeightPersistence) runLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.syncDirtyKeys()
			p.checkAndRunMaintenance()
		case <-p.stopChan:
			return
		}
	}
}

// checkAndRunMaintenance checks if maintenance tasks need to run and executes them.
// Rollover runs daily, cleanup runs weekly.
func (p *DynamicWeightPersistence) checkAndRunMaintenance() {
	now := time.Now()

	// Run rollover daily
	if now.Sub(p.lastRollover) >= DefaultRolloverInterval {
		p.RolloverTimeWindows()
		p.lastRollover = now
	}

	// Run cleanup weekly (7 days)
	if now.Sub(p.lastCleanup) >= 7*24*time.Hour {
		if count, err := p.CleanupExpiredMetrics(); err != nil {
			logrus.WithError(err).Warn("Failed to cleanup expired metrics")
		} else if count > 0 {
			logrus.WithField("count", count).Info("Cleaned up expired dynamic weight metrics")
		}
		p.lastCleanup = now
	}
}

// LoadFromDatabase loads all metrics from database into the store.
// Called on startup to restore metrics after restart.
// Only loads non-deleted records.
func (p *DynamicWeightPersistence) LoadFromDatabase() error {
	var dbMetrics []models.DynamicWeightMetric
	// Only load non-deleted records
	if err := p.db.Where("deleted_at IS NULL").Find(&dbMetrics).Error; err != nil {
		return err
	}

	loaded := 0
	for _, dbm := range dbMetrics {
		metrics := dbMetricToMemory(&dbm)

		var key string
		switch dbm.MetricType {
		case models.MetricTypeSubGroup:
			key = SubGroupMetricsKey(dbm.GroupID, dbm.SubGroupID)
		case models.MetricTypeModelRedirect:
			key = ModelRedirectMetricsKey(dbm.GroupID, dbm.SourceModel, dbm.TargetIndex)
		default:
			continue
		}

		if err := p.manager.SetMetrics(key, metrics); err != nil {
			logrus.WithError(err).WithField("key", key).Debug("Failed to load metric into store")
			continue
		}
		loaded++
	}

	if loaded > 0 {
		logrus.WithField("count", loaded).Info("Dynamic weight metrics loaded from database")
	}

	return nil
}

// dbMetricToMemory converts database model to in-memory metrics.
func dbMetricToMemory(dbm *models.DynamicWeightMetric) *DynamicWeightMetrics {
	metrics := &DynamicWeightMetrics{
		ConsecutiveFailures: dbm.ConsecutiveFailures,
		Requests7d:          dbm.Requests7d,
		Successes7d:         dbm.Successes7d,
		Requests14d:         dbm.Requests14d,
		Successes14d:        dbm.Successes14d,
		Requests30d:         dbm.Requests30d,
		Successes30d:        dbm.Successes30d,
		Requests90d:         dbm.Requests90d,
		Successes90d:        dbm.Successes90d,
		Requests180d:        dbm.Requests180d,
		Successes180d:       dbm.Successes180d,
		UpdatedAt:           dbm.UpdatedAt,
	}
	if dbm.LastFailureAt != nil {
		metrics.LastFailureAt = *dbm.LastFailureAt
	}
	if dbm.LastSuccessAt != nil {
		metrics.LastSuccessAt = *dbm.LastSuccessAt
	}
	if dbm.LastRolloverAt != nil {
		metrics.LastRolloverAt = *dbm.LastRolloverAt
	}
	return metrics
}

// MarkDirtyByKey marks a metrics key as dirty (needs sync).
// This is used as a callback from DynamicWeightManager.
func (p *DynamicWeightPersistence) MarkDirtyByKey(key string) {
	p.dirtyMu.Lock()
	p.dirtyKeys[key] = struct{}{}
	p.dirtyMu.Unlock()
}

// syncDirtyKeys syncs all dirty keys to database.
// Failed keys are re-queued for retry on the next sync cycle.
func (p *DynamicWeightPersistence) syncDirtyKeys() {
	p.dirtyMu.Lock()
	if len(p.dirtyKeys) == 0 {
		p.dirtyMu.Unlock()
		return
	}
	// Copy and clear dirty keys
	keys := make([]string, 0, len(p.dirtyKeys))
	for k := range p.dirtyKeys {
		keys = append(keys, k)
	}
	p.dirtyKeys = make(map[string]struct{})
	p.dirtyMu.Unlock()

	kvStore := p.manager.GetStore()
	var toUpsert []models.DynamicWeightMetric
	var upsertKeys []string // Track keys that will be upserted
	var failedKeys []string

	for _, key := range keys {
		data, err := kvStore.Get(key)
		if err != nil {
			// Re-queue for retry on transient errors (not ErrNotFound)
			if err != errNotFound {
				failedKeys = append(failedKeys, key)
			}
			continue
		}

		var metrics DynamicWeightMetrics
		if err := json.Unmarshal(data, &metrics); err != nil {
			// JSON unmarshal errors are not transient, skip without retry
			continue
		}

		dbm := p.keyToDBMetric(key, &metrics)
		if dbm != nil {
			toUpsert = append(toUpsert, *dbm)
			upsertKeys = append(upsertKeys, key)
		}
	}

	if len(toUpsert) > 0 {
		if err := p.batchUpsert(toUpsert); err != nil {
			// Re-queue all attempted keys on upsert failure
			failedKeys = append(failedKeys, upsertKeys...)
		}
	}

	// Re-queue failed keys for retry on next sync cycle
	if len(failedKeys) > 0 {
		p.dirtyMu.Lock()
		for _, k := range failedKeys {
			p.dirtyKeys[k] = struct{}{}
		}
		p.dirtyMu.Unlock()
	}
}

// keyToDBMetric converts a store key and metrics to database model.
func (p *DynamicWeightPersistence) keyToDBMetric(key string, metrics *DynamicWeightMetrics) *models.DynamicWeightMetric {
	dbm := &models.DynamicWeightMetric{
		ConsecutiveFailures: metrics.ConsecutiveFailures,
		Requests7d:          metrics.Requests7d,
		Successes7d:         metrics.Successes7d,
		Requests14d:         metrics.Requests14d,
		Successes14d:        metrics.Successes14d,
		Requests30d:         metrics.Requests30d,
		Successes30d:        metrics.Successes30d,
		Requests90d:         metrics.Requests90d,
		Successes90d:        metrics.Successes90d,
		Requests180d:        metrics.Requests180d,
		Successes180d:       metrics.Successes180d,
		UpdatedAt:           metrics.UpdatedAt,
	}
	if !metrics.LastFailureAt.IsZero() {
		t := metrics.LastFailureAt
		dbm.LastFailureAt = &t
	}
	if !metrics.LastSuccessAt.IsZero() {
		t := metrics.LastSuccessAt
		dbm.LastSuccessAt = &t
	}
	if !metrics.LastRolloverAt.IsZero() {
		t := metrics.LastRolloverAt
		dbm.LastRolloverAt = &t
	}

	// Parse key to determine type and IDs
	if len(key) > 6 && key[:6] == "dw:sg:" {
		// Sub-group key: dw:sg:{aggregateGroupID}:{subGroupID}
		aggID, subID, ok := parseSubGroupKeyParts(key[6:])
		if ok {
			dbm.MetricType = models.MetricTypeSubGroup
			dbm.GroupID = aggID
			dbm.SubGroupID = subID
			return dbm
		}
	} else if len(key) > 6 && key[:6] == "dw:mr:" {
		// Model redirect key: dw:mr:{groupID}:{encodedModel}:{targetIndex}
		groupID, sourceModel, targetIndex, ok := parseModelRedirectKeyParts(key[6:])
		if ok {
			dbm.MetricType = models.MetricTypeModelRedirect
			dbm.GroupID = groupID
			dbm.SourceModel = sourceModel
			dbm.TargetIndex = targetIndex
			return dbm
		}
	}

	return nil
}

// parseSubGroupKeyParts parses "aggID:subID" format.
func parseSubGroupKeyParts(s string) (aggID, subID uint, ok bool) {
	var colonIdx int
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			colonIdx = i
			break
		}
	}
	if colonIdx == 0 || colonIdx >= len(s)-1 {
		return 0, 0, false
	}

	aggID = parseUintSimple(s[:colonIdx])
	subID = parseUintSimple(s[colonIdx+1:])
	return aggID, subID, true
}

// parseModelRedirectKeyParts parses "{groupID}:{encodedModel}:{targetIndex}".
func parseModelRedirectKeyParts(s string) (groupID uint, sourceModel string, targetIndex int, ok bool) {
	// Find first colon
	idx1 := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			idx1 = i
			break
		}
	}
	if idx1 <= 0 || idx1 >= len(s)-1 {
		return 0, "", 0, false
	}
	groupID = parseUintSimple(s[:idx1])

	// Find last colon
	idx2 := -1
	for i := len(s) - 1; i > idx1; i-- {
		if s[i] == ':' {
			idx2 = i
			break
		}
	}
	if idx2 <= idx1 || idx2 >= len(s)-1 {
		return 0, "", 0, false
	}

	targetIndex = int(parseUintSimple(s[idx2+1:]))

	// Decode source model (URL encoded)
	encodedModel := s[idx1+1 : idx2]
	if decoded, err := url.PathUnescape(encodedModel); err == nil {
		sourceModel = decoded
	} else {
		sourceModel = encodedModel
	}

	return groupID, sourceModel, targetIndex, true
}

// parseUintSimple parses a string to uint.
// Non-digit characters are silently ignored for defensive parsing of store keys.
// Returns 0 for empty or invalid input. This is acceptable since invalid keys
// will be filtered out by the caller's validation logic.
func parseUintSimple(s string) uint {
	var n uint
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + uint(s[i]-'0')
		}
	}
	return n
}

// batchUpsert performs batch upsert of metrics to database.
// Returns error if the upsert fails, allowing caller to handle retry logic.
func (p *DynamicWeightPersistence) batchUpsert(metrics []models.DynamicWeightMetric) error {
	if len(metrics) == 0 {
		return nil
	}

	// Detect database type and use appropriate upsert strategy
	dialect := p.db.Dialector.Name()

	var err error
	switch dialect {
	case "sqlite":
		err = p.batchUpsertSQLite(metrics)
	default:
		// PostgreSQL and MySQL support excluded.column syntax
		err = p.batchUpsertDefault(metrics)
	}

	if err != nil {
		logrus.WithError(err).Warn("Failed to batch upsert dynamic weight metrics")
		return err
	}
	logrus.WithField("count", len(metrics)).Debug("Dynamic weight metrics synced to database")
	return nil
}

// batchUpsertDefault performs batch upsert for PostgreSQL and MySQL.
// Uses GORM's AssignmentColumns which generates excluded.column syntax.
func (p *DynamicWeightPersistence) batchUpsertDefault(metrics []models.DynamicWeightMetric) error {
	return p.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "metric_type"},
			{Name: "group_id"},
			{Name: "sub_group_id"},
			{Name: "source_model"},
			{Name: "target_index"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"consecutive_failures",
			"last_failure_at",
			"last_success_at",
			"requests_7d",
			"successes_7d",
			"requests_14d",
			"successes_14d",
			"requests_30d",
			"successes_30d",
			"requests_90d",
			"successes_90d",
			"requests_180d",
			"successes_180d",
			"last_rollover_at",
			"updated_at",
		}),
	}).CreateInBatches(metrics, 100).Error
}

// batchUpsertSQLite performs batch upsert for SQLite.
// SQLite requires explicit column assignments without the excluded. prefix for older versions.
// Uses smaller batch size for better performance with SQLite's single-writer model.
func (p *DynamicWeightPersistence) batchUpsertSQLite(metrics []models.DynamicWeightMetric) error {
	// SQLite performs better with smaller batches
	batchSize := 50

	for i := 0; i < len(metrics); i += batchSize {
		end := i + batchSize
		if end > len(metrics) {
			end = len(metrics)
		}
		batch := metrics[i:end]

		// For SQLite, use UpdateAll which generates simpler UPDATE SET column = value syntax
		// This avoids the excluded.column syntax that requires SQLite 3.24.0+
		err := p.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "metric_type"},
				{Name: "group_id"},
				{Name: "sub_group_id"},
				{Name: "source_model"},
				{Name: "target_index"},
			},
			UpdateAll: true,
		}).Create(&batch).Error

		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteSubGroupMetrics soft-deletes sub-group metrics from database.
// The data is preserved for potential restoration when sub-group is re-added.
// Use CleanupExpiredMetrics to permanently delete old soft-deleted records.
func (p *DynamicWeightPersistence) DeleteSubGroupMetrics(aggregateGroupID, subGroupID uint) error {
	now := time.Now()
	return p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND sub_group_id = ? AND deleted_at IS NULL",
			models.MetricTypeSubGroup, aggregateGroupID, subGroupID).
		Update("deleted_at", now).Error
}

// DeleteModelRedirectMetrics soft-deletes model redirect metrics from database.
func (p *DynamicWeightPersistence) DeleteModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) error {
	now := time.Now()
	return p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND source_model = ? AND target_index = ? AND deleted_at IS NULL",
			models.MetricTypeModelRedirect, groupID, sourceModel, targetIndex).
		Update("deleted_at", now).Error
}

// RestoreSubGroupMetrics restores soft-deleted sub-group metrics.
// Called when a sub-group is re-added to an aggregate group.
// Returns true if a record was restored, false if no deleted record exists.
func (p *DynamicWeightPersistence) RestoreSubGroupMetrics(aggregateGroupID, subGroupID uint) (bool, error) {
	result := p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND sub_group_id = ? AND deleted_at IS NOT NULL",
			models.MetricTypeSubGroup, aggregateGroupID, subGroupID).
		Update("deleted_at", nil)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		// Reload the restored metrics into memory
		var dbm models.DynamicWeightMetric
		if err := p.db.Where("metric_type = ? AND group_id = ? AND sub_group_id = ?",
			models.MetricTypeSubGroup, aggregateGroupID, subGroupID).First(&dbm).Error; err == nil {
			key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
			metrics := dbMetricToMemory(&dbm)
			if err := p.manager.SetMetrics(key, metrics); err != nil {
				logrus.WithError(err).WithField("key", key).Debug("Failed to restore metrics to store")
			}
		}
		return true, nil
	}
	return false, nil
}

// RestoreModelRedirectMetrics restores soft-deleted model redirect metrics.
func (p *DynamicWeightPersistence) RestoreModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) (bool, error) {
	result := p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND source_model = ? AND target_index = ? AND deleted_at IS NOT NULL",
			models.MetricTypeModelRedirect, groupID, sourceModel, targetIndex).
		Update("deleted_at", nil)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		var dbm models.DynamicWeightMetric
		if err := p.db.Where("metric_type = ? AND group_id = ? AND source_model = ? AND target_index = ?",
			models.MetricTypeModelRedirect, groupID, sourceModel, targetIndex).First(&dbm).Error; err == nil {
			key := ModelRedirectMetricsKey(groupID, sourceModel, targetIndex)
			metrics := dbMetricToMemory(&dbm)
			if err := p.manager.SetMetrics(key, metrics); err != nil {
				logrus.WithError(err).WithField("key", key).Debug("Failed to restore metrics to store")
			}
		}
		return true, nil
	}
	return false, nil
}

// CleanupExpiredMetrics permanently deletes soft-deleted records older than retention period.
// This should be called periodically (e.g., daily) to clean up old data.
func (p *DynamicWeightPersistence) CleanupExpiredMetrics() (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -SoftDeleteRetentionDays)
	result := p.db.Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
		Delete(&models.DynamicWeightMetric{})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > 0 {
		logrus.WithField("count", result.RowsAffected).Info("Cleaned up expired dynamic weight metrics")
	}
	return result.RowsAffected, nil
}

// DeleteAllSubGroupMetricsForGroup soft-deletes all sub-group metrics for an aggregate group.
// Called when an aggregate group is deleted.
func (p *DynamicWeightPersistence) DeleteAllSubGroupMetricsForGroup(aggregateGroupID uint) error {
	now := time.Now()
	return p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND deleted_at IS NULL",
			models.MetricTypeSubGroup, aggregateGroupID).
		Update("deleted_at", now).Error
}

// DeleteAllModelRedirectMetricsForGroup soft-deletes all model redirect metrics for a group.
// Called when a group is deleted.
func (p *DynamicWeightPersistence) DeleteAllModelRedirectMetricsForGroup(groupID uint) error {
	now := time.Now()
	return p.db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND deleted_at IS NULL",
			models.MetricTypeModelRedirect, groupID).
		Update("deleted_at", now).Error
}

// RolloverTimeWindows performs daily rollover of time window statistics.
// This should be called once per day to shift data between time windows.
// Data older than 180 days is discarded. Only processes non-deleted records.
//
// NOTE: Current implementation loads all metrics into memory. For deployments
// with very large numbers of groups (10000+), consider implementing batched
// processing. In typical deployments, the number of metrics is bounded by
// (aggregate_groups * sub_groups) + (groups * model_redirects), which is
// usually manageable in memory.
func (p *DynamicWeightPersistence) RolloverTimeWindows() {
	var dbMetrics []models.DynamicWeightMetric
	// Only process non-deleted records
	if err := p.db.Where("deleted_at IS NULL").Find(&dbMetrics).Error; err != nil {
		logrus.WithError(err).Warn("Failed to fetch metrics for rollover")
		return
	}

	now := time.Now()
	var toUpdate []models.DynamicWeightMetric

	for _, dbm := range dbMetrics {
		// Check if rollover is needed (more than 24 hours since last rollover)
		if dbm.LastRolloverAt != nil && now.Sub(*dbm.LastRolloverAt) < DefaultRolloverInterval {
			continue
		}

		// Calculate days since last rollover
		daysSinceRollover := 1
		if dbm.LastRolloverAt != nil {
			daysSinceRollover = int(now.Sub(*dbm.LastRolloverAt).Hours() / 24)
			if daysSinceRollover < 1 {
				continue
			}
		}

		// Apply decay to each time window
		dbm.Requests7d = applyDecay(dbm.Requests7d, 7, daysSinceRollover)
		dbm.Successes7d = applyDecay(dbm.Successes7d, 7, daysSinceRollover)
		dbm.Requests14d = applyDecay(dbm.Requests14d, 14, daysSinceRollover)
		dbm.Successes14d = applyDecay(dbm.Successes14d, 14, daysSinceRollover)
		dbm.Requests30d = applyDecay(dbm.Requests30d, 30, daysSinceRollover)
		dbm.Successes30d = applyDecay(dbm.Successes30d, 30, daysSinceRollover)
		dbm.Requests90d = applyDecay(dbm.Requests90d, 90, daysSinceRollover)
		dbm.Successes90d = applyDecay(dbm.Successes90d, 90, daysSinceRollover)
		dbm.Requests180d = applyDecay(dbm.Requests180d, 180, daysSinceRollover)
		dbm.Successes180d = applyDecay(dbm.Successes180d, 180, daysSinceRollover)

		dbm.LastRolloverAt = &now
		toUpdate = append(toUpdate, dbm)
	}

	if len(toUpdate) > 0 {
		if err := p.batchUpsert(toUpdate); err != nil {
			// Skip in-memory update on DB failure to avoid inconsistency
			logrus.WithError(err).WithField("count", len(toUpdate)).Warn("Failed to persist rolled over metrics")
			return
		}
		logrus.WithField("count", len(toUpdate)).Info("Dynamic weight metrics rolled over")

		// Also update in-memory store
		for _, dbm := range toUpdate {
			var key string
			switch dbm.MetricType {
			case models.MetricTypeSubGroup:
				key = SubGroupMetricsKey(dbm.GroupID, dbm.SubGroupID)
			case models.MetricTypeModelRedirect:
				key = ModelRedirectMetricsKey(dbm.GroupID, dbm.SourceModel, dbm.TargetIndex)
			default:
				continue
			}
			metrics := dbMetricToMemory(&dbm)
			if err := p.manager.SetMetrics(key, metrics); err != nil {
				logrus.WithError(err).WithField("key", key).Debug("Failed to update store after rollover")
			}
		}
	}
}

// applyDecay applies decay to a count based on window size and days passed.
func applyDecay(count int64, windowDays int, daysPassed int) int64 {
	if count <= 0 || daysPassed <= 0 {
		return count
	}

	// Calculate how much data should be removed
	// If daysPassed >= windowDays, all data is expired
	if daysPassed >= windowDays {
		return 0
	}

	// Proportional decay: remove (daysPassed/windowDays) of the data
	remaining := float64(count) * (1.0 - float64(daysPassed)/float64(windowDays))
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}
