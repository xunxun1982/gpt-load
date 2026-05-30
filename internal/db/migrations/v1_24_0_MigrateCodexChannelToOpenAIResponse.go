package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_24_0_MigrateCodexChannelToOpenAIResponse migrates legacy codex groups to openai-response.
func V1_24_0_MigrateCodexChannelToOpenAIResponse(db *gorm.DB) error {
	if !db.Migrator().HasTable("groups") {
		logrus.Info("Table groups does not exist, skipping codex channel migration")
		return nil
	}

	var count int64
	if err := db.Table("groups").Where("channel_type = ?", "codex").Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		logrus.Info("No legacy codex groups need migration")
		return nil
	}

	result := db.Table("groups").
		Where("channel_type = ?", "codex").
		Update("channel_type", "openai-response")
	if result.Error != nil {
		return result.Error
	}

	logrus.WithField("count", result.RowsAffected).Info("Migrated legacy codex groups to openai-response")
	return nil
}
