package db

import (
	"fmt"

	"gorm.io/gorm"
)

const proxyPoolNameUniqueIndex = "idx_proxy_pool_items_name_unique"
const proxyPoolNameUniqueIndexMigrationVersion = "v1.27.0_proxy_pool_name_unique_index"
const proxyPoolNameMaxLength = 255

type proxyPoolNameUniqueIndexModel struct {
	Name string `gorm:"column:name;uniqueIndex:idx_proxy_pool_items_name_unique"`
}

func (proxyPoolNameUniqueIndexModel) TableName() string {
	return "proxy_pool_items"
}

type proxyPoolNameUniqueIndexRow struct {
	ID   uint
	Name string
}

// V1_27_0_AddProxyPoolNameUniqueIndex makes proxy pool names unique after cleaning legacy duplicates.
func V1_27_0_AddProxyPoolNameUniqueIndex(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable("proxy_pool_items") {
		return nil
	}
	if err := ensureDataMigrationsTable(db); err != nil {
		return err
	}
	ran, err := hasDataMigrationRun(db, proxyPoolNameUniqueIndexMigrationVersion)
	if err != nil {
		return err
	}
	if ran {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		acquired, err := acquireDataMigrationMarker(tx, proxyPoolNameUniqueIndexMigrationVersion)
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}

		txMigrator := tx.Migrator()
		if !txMigrator.HasTable("proxy_pool_items") || txMigrator.HasIndex("proxy_pool_items", proxyPoolNameUniqueIndex) {
			return nil
		}
		if err := renameDuplicateProxyPoolNames(tx); err != nil {
			return err
		}
		return txMigrator.CreateIndex(&proxyPoolNameUniqueIndexModel{}, proxyPoolNameUniqueIndex)
	})
}

func renameDuplicateProxyPoolNames(db *gorm.DB) error {
	duplicateNames := make([]string, 0)
	if err := db.Table("proxy_pool_items").
		Select("name").
		Group("name").
		Having("COUNT(*) > 1").
		Order("name ASC").
		Pluck("name", &duplicateNames).Error; err != nil {
		return err
	}

	for _, name := range duplicateNames {
		rows := make([]proxyPoolNameUniqueIndexRow, 0)
		if err := db.Table("proxy_pool_items").
			Select("id", "name").
			Where("name = ?", name).
			Order("id ASC").
			Find(&rows).Error; err != nil {
			return err
		}

		nextSuffix := 2
		for i := 1; i < len(rows); i++ {
			newName, updatedSuffix, err := nextAvailableProxyPoolName(db, name, nextSuffix)
			if err != nil {
				return err
			}
			nextSuffix = updatedSuffix
			if err := db.Table("proxy_pool_items").Where("id = ?", rows[i].ID).Update("name", newName).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

func nextAvailableProxyPoolName(db *gorm.DB, base string, suffix int) (string, int, error) {
	if suffix < 2 {
		suffix = 2
	}
	for {
		suffixText := fmt.Sprintf("-%d", suffix)
		candidate := truncateProxyPoolNameBase(base, len([]rune(suffixText))) + suffixText
		suffix++
		var count int64
		if err := db.Table("proxy_pool_items").Where("name = ?", candidate).Count(&count).Error; err != nil {
			return "", suffix, err
		}
		if count == 0 {
			return candidate, suffix, nil
		}
	}
}

func truncateProxyPoolNameBase(base string, suffixLength int) string {
	runes := []rune(base)
	limit := proxyPoolNameMaxLength - suffixLength
	if limit < 1 {
		limit = 1
	}
	if len(runes) <= limit {
		return base
	}
	return string(runes[:limit])
}
