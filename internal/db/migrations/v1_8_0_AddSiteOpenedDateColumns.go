package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ManagedSiteV180 is a minimal struct for migration purposes
type ManagedSiteV180 struct {
	LastSiteOpenedDate        string `gorm:"column:last_site_opened_date;type:char(10);not null;default:''"`
	LastCheckinPageOpenedDate string `gorm:"column:last_checkin_page_opened_date;type:char(10);not null;default:''"`
}

// TableName specifies the table name for GORM migration
func (ManagedSiteV180) TableName() string {
	return "managed_sites"
}

// V1_8_0_AddSiteOpenedDateColumns adds columns to track when user clicked
// "Open Site" or "Open Check-in Page" buttons.
// These columns store dates in Beijing time (UTC+8) with 05:00 reset.
func V1_8_0_AddSiteOpenedDateColumns(db *gorm.DB) error {
	logrus.Info("Running migration v1.8.0: Add site opened date columns")

	migrator := db.Migrator()

	// Add last_site_opened_date column if not exists
	if !migrator.HasColumn(&ManagedSiteV180{}, "last_site_opened_date") {
		if err := migrator.AddColumn(&ManagedSiteV180{}, "last_site_opened_date"); err != nil {
			logrus.WithError(err).Error("Failed to add last_site_opened_date column")
			return err
		}
		logrus.Info("Added last_site_opened_date column to managed_sites")
	} else {
		logrus.Info("Column last_site_opened_date already exists, skipping")
	}

	// Add last_checkin_page_opened_date column if not exists
	if !migrator.HasColumn(&ManagedSiteV180{}, "last_checkin_page_opened_date") {
		if err := migrator.AddColumn(&ManagedSiteV180{}, "last_checkin_page_opened_date"); err != nil {
			logrus.WithError(err).Error("Failed to add last_checkin_page_opened_date column")
			return err
		}
		logrus.Info("Added last_checkin_page_opened_date column to managed_sites")
	} else {
		logrus.Info("Column last_checkin_page_opened_date already exists, skipping")
	}

	logrus.Info("Migration v1.8.0 completed successfully")
	return nil
}
