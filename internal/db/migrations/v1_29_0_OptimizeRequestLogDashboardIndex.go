package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const requestLogTypeTimestampIndex = "idx_request_logs_type_timestamp"

// V1_29_0_OptimizeRequestLogDashboardIndex adds the covering filter index used by RPM statistics.
func V1_29_0_OptimizeRequestLogDashboardIndex(db *gorm.DB) error {
	logrus.Info("Running migration v1.29.0: Optimizing request log dashboard index")

	return createIndexIfNotExists(
		db,
		"request_logs",
		requestLogTypeTimestampIndex,
		"request_type, timestamp",
	)
}
