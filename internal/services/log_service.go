package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const likeEscapeChar = "!"

// ExportableLogKey defines the structure for the data to be exported to CSV.
type ExportableLogKey struct {
	KeyValue   string `gorm:"column:key_value"`
	GroupName  string `gorm:"column:group_name"`
	StatusCode int    `gorm:"column:status_code"`
}

// LogService provides services related to request logs.
type LogService struct {
	DB            *gorm.DB
	EncryptionSvc encryption.Service
}

// NewLogService creates a new LogService.
func NewLogService(db *gorm.DB, encryptionSvc encryption.Service) *LogService {
	return &LogService{
		DB:            db,
		EncryptionSvc: encryptionSvc,
	}
}

// escapeLike escapes special characters in LIKE pattern.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, likeEscapeChar, likeEscapeChar+likeEscapeChar)
	s = strings.ReplaceAll(s, "%", likeEscapeChar+"%")
	s = strings.ReplaceAll(s, "_", likeEscapeChar+"_")
	return s
}

// logFiltersScope returns a GORM scope function that applies filters from the Gin context.
func (s *LogService) logFiltersScope(c *gin.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if parentGroupName := c.Query("parent_group_name"); parentGroupName != "" {
			db = db.Where("parent_group_name LIKE ? ESCAPE '!'", "%"+escapeLike(parentGroupName)+"%")
		}
		if groupName := c.Query("group_name"); groupName != "" {
			db = db.Where("group_name LIKE ? ESCAPE '!'", "%"+escapeLike(groupName)+"%")
		}
		if keyValue := c.Query("key_value"); keyValue != "" {
			keyHash := s.EncryptionSvc.Hash(keyValue)
			db = db.Where("key_hash = ?", keyHash)
		}
		if model := c.Query("model"); model != "" {
			db = db.Where("model LIKE ? ESCAPE '!'", "%"+escapeLike(model)+"%")
		}
		if isSuccessStr := c.Query("is_success"); isSuccessStr != "" {
			if isSuccess, err := strconv.ParseBool(isSuccessStr); err == nil {
				db = db.Where("is_success = ?", isSuccess)
			}
		}
		if requestType := c.Query("request_type"); requestType != "" {
			db = db.Where("request_type = ?", requestType)
		}
		if statusCodeStr := c.Query("status_code"); statusCodeStr != "" {
			if statusCode, err := strconv.Atoi(statusCodeStr); err == nil {
				db = db.Where("status_code = ?", statusCode)
			}
		}
		if sourceIP := c.Query("source_ip"); sourceIP != "" {
			db = db.Where("source_ip = ?", sourceIP)
		}
		if errorContains := c.Query("error_contains"); errorContains != "" {
			db = db.Where("error_message LIKE ? ESCAPE '!'", "%"+escapeLike(errorContains)+"%")
		}
		if startTimeStr := c.Query("start_time"); startTimeStr != "" {
			if startTime, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
				db = db.Where("timestamp >= ?", startTime)
			}
		}
		if endTimeStr := c.Query("end_time"); endTimeStr != "" {
			if endTime, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
				db = db.Where("timestamp <= ?", endTime)
			}
		}
		return db
	}
}

// GetLogsQuery returns a GORM query for fetching logs with filters.
func (s *LogService) GetLogsQuery(c *gin.Context) *gorm.DB {
	return s.DB.Model(&models.RequestLog{}).Scopes(s.logFiltersScope(c))
}

// StreamLogKeysToCSV fetches unique keys from logs based on filters and streams them as a CSV.
// Optimized: Uses ROW_NUMBER() window function to reduce table scans from 3 to 1.
// Database version requirements:
// - PostgreSQL 8.4+ (window functions supported since 2009)
// - MySQL 8.0+ (window functions added in 2018)
// - SQLite 3.25+ (window functions added in September 2018)
// Note: Runtime version validation is not performed because:
// 1. These versions are 5+ years old and widely deployed
// 2. GORM driver initialization would fail earlier with incompatible versions
// 3. Adding version checks would introduce unnecessary complexity and dependencies
func (s *LogService) StreamLogKeysToCSV(c *gin.Context, writer io.Writer) error {
	// Create a CSV writer
	csvWriter := csv.NewWriter(writer)
	defer csvWriter.Flush()

	// Write CSV header
	header := []string{"key_value", "group_name", "status_code"}
	if err := csvWriter.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	var results []ExportableLogKey

	// Build base query with filters
	baseQuery := s.DB.Model(&models.RequestLog{}).
		Select("id", "key_hash", "key_value", "group_name", "status_code", "timestamp").
		Scopes(s.logFiltersScope(c)).
		Where("key_hash IS NOT NULL AND key_hash != ''")

	// Detect database dialect and use optimized query strategy
	dialect := s.DB.Dialector.Name()

	// Execute optimized query using ROW_NUMBER() window function.
	// AI Review Note: Suggested merging the identical SQL branches into a single query.
	// Decision: Keep separate branches for future database-specific optimizations.
	// Currently all branches use the same SQL, but this structure allows easy customization
	// if a specific database requires different syntax or optimization in the future.
	// The performance cost of the switch statement is negligible compared to DB query time.
	var err error
	switch dialect {
	case "postgres", "pgx":
		// PostgreSQL: Use ROW_NUMBER() window function for optimal performance
		// This reduces 3 table scans to 1 scan with a single sort operation
		err = s.DB.Raw(`
			SELECT key_value, group_name, status_code FROM (
				SELECT
					key_hash, key_value, group_name, status_code,
					ROW_NUMBER() OVER (PARTITION BY key_hash ORDER BY timestamp DESC, id DESC) AS rn
				FROM (?) AS base
			) AS ranked WHERE rn = 1
			ORDER BY key_hash
		`, baseQuery).Scan(&results).Error

	case "mysql":
		// MySQL 8.0+: Use ROW_NUMBER() window function
		// Note: MySQL 5.7 does not support window functions, but we target newer versions
		err = s.DB.Raw(`
			SELECT key_value, group_name, status_code FROM (
				SELECT
					key_hash, key_value, group_name, status_code,
					ROW_NUMBER() OVER (PARTITION BY key_hash ORDER BY timestamp DESC, id DESC) AS rn
				FROM (?) AS base
			) AS ranked WHERE rn = 1
			ORDER BY key_hash
		`, baseQuery).Scan(&results).Error

	default:
		// SQLite 3.25+: Use ROW_NUMBER() window function (supported since September 2018)
		// All modern SQLite versions support window functions.
		// Note: This default branch also handles unknown dialects. We intentionally do not
		// log warnings for unknown dialects because SQLite dialect names vary ("sqlite", "sqlite3")
		// and incompatible databases will fail with clear SQL syntax errors at execution time.
		err = s.DB.Raw(`
			SELECT key_value, group_name, status_code FROM (
				SELECT
					key_hash, key_value, group_name, status_code,
					ROW_NUMBER() OVER (PARTITION BY key_hash ORDER BY timestamp DESC, id DESC) AS rn
				FROM (?) AS base
			) AS ranked WHERE rn = 1
			ORDER BY key_hash
		`, baseQuery).Scan(&results).Error
	}

	if err != nil {
		return fmt.Errorf("failed to fetch log keys: %w", err)
	}

	// Decrypt and write CSV data
	for _, record := range results {
		// Decrypt key for CSV export
		decryptedKey := record.KeyValue
		if record.KeyValue != "" {
			if decrypted, err := s.EncryptionSvc.Decrypt(record.KeyValue); err != nil {
				logrus.WithError(err).WithField("key_value", record.KeyValue).Error("Failed to decrypt key for CSV export")
				decryptedKey = "failed-to-decrypt"
			} else {
				decryptedKey = decrypted
			}
		}

		csvRecord := []string{
			decryptedKey,
			record.GroupName,
			strconv.Itoa(record.StatusCode),
		}
		if err := csvWriter.Write(csvRecord); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}
	}

	return nil
}
