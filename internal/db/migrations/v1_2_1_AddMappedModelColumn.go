package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_2_1_AddMappedModelColumn adds mapped_model column to request_logs table
func V1_2_1_AddMappedModelColumn(db *gorm.DB) error {
	logrus.Info("Running migration v1.2.1: Add mapped_model column to request_logs")

	// Check if column already exists
	if db.Migrator().HasColumn(&RequestLogV121{}, "mapped_model") {
		logrus.Info("Column mapped_model already exists, skipping migration v1.2.1")
		return nil
	}

	// Add the column
	if err := db.Migrator().AddColumn(&RequestLogV121{}, "mapped_model"); err != nil {
		logrus.WithError(err).Error("Failed to add mapped_model column")
		return err
	}

	logrus.Info("Successfully added mapped_model column to request_logs table")
	return nil
}

// RequestLogV121 is a minimal struct for migration purposes
type RequestLogV121 struct {
	MappedModel string `gorm:"type:varchar(255)"`
}

// TableName specifies the table name for GORM
func (RequestLogV121) TableName() string {
	return "request_logs"
}
