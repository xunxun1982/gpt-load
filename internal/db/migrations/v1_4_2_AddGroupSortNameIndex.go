package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_4_2_AddGroupSortNameIndex adds composite index on groups(sort, name) for ORDER BY queries.
// This optimizes: SELECT * FROM groups ORDER BY sort ASC, name ASC
func V1_4_2_AddGroupSortNameIndex(db *gorm.DB) error {
	logrus.Info("Running migration v1.4.2: Adding idx_groups_sort_name index")
	return createIndexIfNotExists(db, "groups", "idx_groups_sort_name", "sort, name")
}
