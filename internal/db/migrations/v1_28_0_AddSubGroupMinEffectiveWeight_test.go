package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyGroupSubGroupV1280 struct {
	ID         uint `gorm:"primaryKey;autoIncrement"`
	GroupID    uint
	SubGroupID uint
	Weight     int
}

func (legacyGroupSubGroupV1280) TableName() string {
	return "group_sub_groups"
}

func TestV1_28_0_AddSubGroupMinEffectiveWeight(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyGroupSubGroupV1280{}))
	require.NoError(t, db.Exec(
		"INSERT INTO group_sub_groups (group_id, sub_group_id, weight) VALUES (?, ?, ?)",
		1, 2, 100,
	).Error)
	require.False(t, db.Migrator().HasColumn(&legacyGroupSubGroupV1280{}, "min_effective_weight"))

	require.NoError(t, V1_28_0_AddSubGroupMinEffectiveWeight(db))
	require.True(t, db.Migrator().HasColumn(&legacyGroupSubGroupV1280{}, "min_effective_weight"))

	var minEffectiveWeight int
	require.NoError(t, db.Table("group_sub_groups").
		Select("min_effective_weight").
		Where("group_id = ? AND sub_group_id = ?", 1, 2).
		Scan(&minEffectiveWeight).Error)
	require.Equal(t, 1, minEffectiveWeight)

	require.NoError(t, V1_28_0_AddSubGroupMinEffectiveWeight(db))
}
