package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_14_0_AddBypassMethodColumn adds bypass_method column to managed_sites table.
// This column stores the WAF/Cloudflare bypass method for each site.
// Supported values: "" (none), "stealth" (TLS fingerprint spoofing)
func V1_14_0_AddBypassMethodColumn(db *gorm.DB) error {
	migrator := db.Migrator()

	// Check if managed_sites table exists
	if !migrator.HasTable("managed_sites") {
		logrus.Info("Table managed_sites does not exist, skipping bypass_method column migration")
		return nil
	}

	// Add bypass_method column if not exists
	if !migrator.HasColumn(&managedSiteBypassMethod{}, "bypass_method") {
		if err := migrator.AddColumn(&managedSiteBypassMethod{}, "bypass_method"); err != nil {
			logrus.WithError(err).Error("Failed to add bypass_method column to managed_sites")
			return err
		}
		logrus.Info("Added bypass_method column to managed_sites table")
	} else {
		logrus.Info("Column bypass_method already exists in managed_sites, skipping")
	}

	return nil
}

// managedSiteBypassMethod is a minimal struct for migration purposes
type managedSiteBypassMethod struct {
	ID           uint   `gorm:"primaryKey"`
	BypassMethod string `gorm:"column:bypass_method;type:varchar(32);not null;default:''"`
}

func (managedSiteBypassMethod) TableName() string {
	return "managed_sites"
}
