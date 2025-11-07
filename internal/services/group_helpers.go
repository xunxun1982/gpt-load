package services

import (
	"context"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"

	"gorm.io/gorm"
)

// FindGroupByID finds a group by ID and returns it, or an error if not found.
func FindGroupByID(ctx context.Context, db *gorm.DB, groupID uint) (*models.Group, error) {
	var group models.Group
	if err := db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewI18nError(app_errors.ErrResourceNotFound, "group.not_found", nil)
		}
		return nil, app_errors.ParseDBError(err)
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


