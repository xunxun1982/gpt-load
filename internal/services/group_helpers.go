package services

import (
	"context"
	"errors"
	"strings"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"

	"gorm.io/gorm"
)

// GroupListOrderClause defines the standard ORDER BY clause for group list queries.
// Groups are sorted by sort field ascending, then by name ascending for stable ordering
// when sort values are equal (alphabetical order by group name).
const GroupListOrderClause = "sort ASC, name ASC"

// isTransientDBError checks if the error is a transient database error that can be retried
// or handled gracefully by returning cached data.
func isTransientDBError(err error) bool {
	if err == nil {
		return false
	}
	// Context timeout or cancellation
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// SQLite database locked error
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "database table is locked") ||
		strings.Contains(errStr, "busy") {
		return true
	}
	return false
}

// FindGroupByID finds a group by ID and returns it, or an error if not found.
func FindGroupByID(ctx context.Context, db *gorm.DB, groupID uint) (*models.Group, error) {
	var group models.Group
	// Use primary key lookup without ORDER BY to avoid slow paths under heavy load
	if err := db.WithContext(ctx).Where("id = ?", groupID).Limit(1).Find(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	if group.ID == 0 {
		return nil, NewI18nError(app_errors.ErrResourceNotFound, "group.not_found", nil)
	}
	return &group, nil
}

// FindGroupByIDWithType finds a group by ID and validates its type.
// Returns the group if found and type matches, or an error otherwise.
func FindGroupByIDWithType(ctx context.Context, db *gorm.DB, groupID uint, expectedType string) (*models.Group, error) {
	group, err := FindGroupByID(ctx, db, groupID)
	if err != nil {
		return nil, err
	}

	if group.GroupType != expectedType {
		var messageID string
		if expectedType == "aggregate" {
			messageID = "group.not_aggregate"
		} else {
			// Use existing i18n key for standard group validation
			messageID = "validation.invalid_group_type"
		}
		return nil, NewI18nError(app_errors.ErrBadRequest, messageID, nil)
	}

	return group, nil
}

// FindAggregateGroupByID is a convenience function to find and validate an aggregate group.
func FindAggregateGroupByID(ctx context.Context, db *gorm.DB, groupID uint) (*models.Group, error) {
	return FindGroupByIDWithType(ctx, db, groupID, "aggregate")
}
