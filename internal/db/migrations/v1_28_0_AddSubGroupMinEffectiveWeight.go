package db

import (
	"gpt-load/internal/models"

	"gorm.io/gorm"
)

// V1_28_0_AddSubGroupMinEffectiveWeight adds the relation-level floor for dynamic weights.
func V1_28_0_AddSubGroupMinEffectiveWeight(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasColumn(&models.GroupSubGroup{}, "min_effective_weight") {
		if err := migrator.AddColumn(&models.GroupSubGroup{}, "min_effective_weight"); err != nil {
			return err
		}
	}
	return nil
}
