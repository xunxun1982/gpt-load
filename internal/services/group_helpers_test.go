package services

import (
	"context"
	"errors"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupListOrderClause(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "sort ASC, name ASC", GroupListOrderClause)
	assert.Contains(t, GroupListOrderClause, "sort ASC")
	assert.Contains(t, GroupListOrderClause, "name ASC")
}

func TestIsTransientDBError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "context canceled",
			err:      context.Canceled,
			expected: true,
		},
		{
			name:     "context deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "database locked error",
			err:      errors.New("database is locked"),
			expected: true,
		},
		{
			name:     "regular error",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTransientDBError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindGroupByID(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ctx := context.Background()

	// Create a test group
	testGroup := &models.Group{
		Name:        "test-group",
		DisplayName: "Test Group",
		ChannelType: "openai",
	}
	testGroup.Upstreams = []byte(`["https://api.openai.com"]`)
	err := db.Create(testGroup).Error
	require.NoError(t, err)

	tests := []struct {
		name      string
		groupID   uint
		expectErr bool
	}{
		{
			name:      "existing group",
			groupID:   testGroup.ID,
			expectErr: false,
		},
		{
			name:      "non-existing group",
			groupID:   9999,
			expectErr: true,
		},
		{
			name:      "zero ID",
			groupID:   0,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, err := FindGroupByID(ctx, db, tt.groupID)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, group)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, group)
				assert.Equal(t, tt.groupID, group.ID)
			}
		})
	}
}

func TestFindGroupByIDWithType(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ctx := context.Background()

	// Create test groups
	standardGroup := &models.Group{
		Name:        "standard-group",
		DisplayName: "Standard Group",
		ChannelType: "openai",
		GroupType:   "standard",
	}
	standardGroup.Upstreams = []byte(`["https://api.openai.com"]`)
	err := db.Create(standardGroup).Error
	require.NoError(t, err)

	aggregateGroup := &models.Group{
		Name:        "aggregate-group",
		DisplayName: "Aggregate Group",
		ChannelType: "openai",
		GroupType:   "aggregate",
	}
	aggregateGroup.Upstreams = []byte(`["https://api.openai.com"]`)
	err = db.Create(aggregateGroup).Error
	require.NoError(t, err)

	tests := []struct {
		name         string
		groupID      uint
		expectedType string
		expectErr    bool
	}{
		{
			name:         "standard group with correct type",
			groupID:      standardGroup.ID,
			expectedType: "standard",
			expectErr:    false,
		},
		{
			name:         "aggregate group with correct type",
			groupID:      aggregateGroup.ID,
			expectedType: "aggregate",
			expectErr:    false,
		},
		{
			name:         "standard group with wrong type",
			groupID:      standardGroup.ID,
			expectedType: "aggregate",
			expectErr:    true,
		},
		{
			name:         "aggregate group with wrong type",
			groupID:      aggregateGroup.ID,
			expectedType: "standard",
			expectErr:    true,
		},
		{
			name:         "non-existing group",
			groupID:      9999,
			expectedType: "standard",
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, err := FindGroupByIDWithType(ctx, db, tt.groupID, tt.expectedType)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, group)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, group)
				assert.Equal(t, tt.groupID, group.ID)
				assert.Equal(t, tt.expectedType, group.GroupType)
			}
		})
	}
}

func TestFindAggregateGroupByID(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ctx := context.Background()

	// Create test groups
	aggregateGroup := &models.Group{
		Name:        "aggregate-group",
		DisplayName: "Aggregate Group",
		ChannelType: "openai",
		GroupType:   "aggregate",
	}
	aggregateGroup.Upstreams = []byte(`["https://api.openai.com"]`)
	err := db.Create(aggregateGroup).Error
	require.NoError(t, err)

	standardGroup := &models.Group{
		Name:        "standard-group",
		DisplayName: "Standard Group",
		ChannelType: "openai",
		GroupType:   "standard",
	}
	standardGroup.Upstreams = []byte(`["https://api.openai.com"]`)
	err = db.Create(standardGroup).Error
	require.NoError(t, err)

	tests := []struct {
		name      string
		groupID   uint
		expectErr bool
	}{
		{
			name:      "valid aggregate group",
			groupID:   aggregateGroup.ID,
			expectErr: false,
		},
		{
			name:      "standard group (should fail)",
			groupID:   standardGroup.ID,
			expectErr: true,
		},
		{
			name:      "non-existing group",
			groupID:   9999,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, err := FindAggregateGroupByID(ctx, db, tt.groupID)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, group)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, group)
				assert.Equal(t, "aggregate", group.GroupType)
			}
		})
	}
}

func TestIsTransientDBError_Integration(t *testing.T) {
	t.Parallel()

	// Test that it correctly delegates to utils.IsTransientDBError
	testErr := errors.New("database is locked")
	result := isTransientDBError(testErr)
	expected := utils.IsTransientDBError(testErr)

	assert.Equal(t, expected, result)
}

// Benchmark tests
func BenchmarkFindGroupByID(b *testing.B) {
	b.ReportAllocs()

	db := setupTestDB(b)
	ctx := context.Background()

	testGroup := &models.Group{
		Name:        "bench-group",
		DisplayName: "Benchmark Group",
		ChannelType: "openai",
	}
	if err := db.Create(testGroup).Error; err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = FindGroupByID(ctx, db, testGroup.ID)
	}
}

func BenchmarkFindGroupByIDWithType(b *testing.B) {
	b.ReportAllocs()

	db := setupTestDB(b)
	ctx := context.Background()

	testGroup := &models.Group{
		Name:        "bench-group",
		DisplayName: "Benchmark Group",
		ChannelType: "openai",
		GroupType:   "standard",
	}
	if err := db.Create(testGroup).Error; err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = FindGroupByIDWithType(ctx, db, testGroup.ID, "standard")
	}
}
