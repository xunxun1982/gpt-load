package services

import (
	"encoding/csv"
	"fmt"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"io"
	"strconv"
	"strings"
	"time"

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

	baseQuery := s.DB.Model(&models.RequestLog{}).
		Select("id", "key_hash", "key_value", "group_name", "status_code", "timestamp").
		Scopes(s.logFiltersScope(c)).
		Where("key_hash IS NOT NULL AND key_hash != ''")

	// Pick the latest record for each key_hash.
	// We intentionally avoid window functions to reduce sort overhead on large datasets.
	// Note: request_logs.id is a UUID string, so MAX(id) is NOT a reliable proxy for "latest".
	// Instead, we use MAX(timestamp) as the latest marker, then use MAX(id) only as a deterministic tie-breaker
	// when multiple rows share the same timestamp.
	err := s.DB.Raw(`
		WITH filtered_logs AS (
			SELECT * FROM (?) AS base
		),
		latest_ts AS (
			SELECT key_hash, MAX(timestamp) AS max_ts
			FROM filtered_logs
			GROUP BY key_hash
		),
		latest_id AS (
			SELECT fl.key_hash, MAX(fl.id) AS max_id
			FROM filtered_logs fl
			INNER JOIN latest_ts lt ON fl.key_hash = lt.key_hash AND fl.timestamp = lt.max_ts
			GROUP BY fl.key_hash
		)
		SELECT
			fl.key_value,
			fl.group_name,
			fl.status_code
		FROM filtered_logs fl
		INNER JOIN latest_ts lt ON fl.key_hash = lt.key_hash AND fl.timestamp = lt.max_ts
		INNER JOIN latest_id li ON fl.key_hash = li.key_hash AND fl.id = li.max_id
		ORDER BY fl.key_hash
	`, baseQuery).Scan(&results).Error

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
