package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_6_1_AddRequestLogsTimestampIndex adds a dedicated index on request_logs.timestamp
// for faster DELETE operations during log cleanup.
// The existing composite indexes (idx_request_logs_group_timestamp, idx_request_logs_success_timestamp)
// are not efficient for DELETE WHERE timestamp < ? queries because timestamp is not the leading column.
func V1_6_1_AddRequestLogsTimestampIndex(db *gorm.DB) error {
	logrus.Info("Running migration v1.6.1: Adding timestamp index to request_logs for faster cleanup")

	indexName := "idx_request_logs_timestamp"
	if err := createIndexIfNotExists(db, "request_logs", indexName, "timestamp"); err != nil {
		logrus.WithError(err).Warnf("Failed to create %s index, continuing anyway", indexName)
	}

	return nil
}
