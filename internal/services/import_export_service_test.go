package services

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestExportSystemIncludesStandardChildGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	parent := models.Group{
		Name:        "parent-group",
		DisplayName: "Parent Group",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[{"url":"https://parent.example.com","weight":1}]`),
	}
	require.NoError(t, db.Create(&parent).Error)

	childGroups := []models.Group{
		{
			Name:               "child-group-a",
			DisplayName:        "Child Group A",
			Description:        "Child A description",
			GroupType:          "standard",
			ChannelType:        "openai",
			Enabled:            true,
			ParentGroupID:      &parent.ID,
			Sort:               20,
			Upstreams:          datatypes.JSON(`[{"url":"https://child-a.example.com","weight":1}]`),
			TestModel:          "gpt-4o-mini",
			Config:             datatypes.JSONMap{"base_url": "https://child-a.example.com"},
			ModelRedirectRules: datatypes.JSONMap{"gpt-4": "gpt-4o"},
		},
		{
			Name:          "child-group-b",
			DisplayName:   "Child Group B",
			Description:   "Child B description",
			GroupType:     "standard",
			ChannelType:   "openai",
			Enabled:       false,
			ParentGroupID: &parent.ID,
			Sort:          10,
			Upstreams:     datatypes.JSON(`[{"url":"https://child-b.example.com","weight":1}]`),
			TestModel:     "gpt-4.1-mini",
			Config:        datatypes.JSONMap{"base_url": "https://child-b.example.com"},
		},
		{
			Name:                 "child-group-c",
			DisplayName:          "Child Group C",
			Description:          "Child C description",
			GroupType:            "standard",
			ChannelType:          "openai",
			Enabled:              true,
			ParentGroupID:        &parent.ID,
			Sort:                 30,
			Upstreams:            datatypes.JSON(`[{"url":"https://child-c.example.com","weight":1}]`),
			TestModel:            "gpt-4.1",
			ModelRedirectRulesV2: datatypes.JSON(`{"source":{"targets":[{"model":"target","weight":100}]}}`),
		},
	}
	for i := range childGroups {
		enabled := childGroups[i].Enabled
		require.NoError(t, db.Create(&childGroups[i]).Error)
		if !enabled {
			require.NoError(t, db.Model(&childGroups[i]).Update("enabled", false).Error)
			childGroups[i].Enabled = false
		}
		require.NoError(t, db.Create(&models.APIKey{
			GroupID:  childGroups[i].ID,
			KeyValue: "encrypted-key-" + childGroups[i].Name,
			KeyHash:  "hash-" + childGroups[i].Name,
			Status:   models.KeyStatusActive,
		}).Error)
	}

	exported, err := service.ExportSystem()
	require.NoError(t, err)
	require.Len(t, exported.Groups, 1)
	require.Len(t, exported.Groups[0].ChildGroups, 3)

	byName := make(map[string]ChildGroupExport, len(exported.Groups[0].ChildGroups))
	for _, childExport := range exported.Groups[0].ChildGroups {
		byName[childExport.Name] = childExport
	}
	require.Contains(t, byName, "child-group-a")
	require.Contains(t, byName, "child-group-b")
	require.Contains(t, byName, "child-group-c")

	childA := byName["child-group-a"]
	assert.Equal(t, "Child Group A", childA.DisplayName)
	assert.Equal(t, "gpt-4o-mini", childA.TestModel)
	assert.JSONEq(t, `{"base_url":"https://child-a.example.com"}`, string(childA.Config))
	assert.JSONEq(t, `{"gpt-4":"gpt-4o"}`, string(childA.ModelRedirectRules))
	require.Len(t, childA.Keys, 1)
	assert.Equal(t, "encrypted-key-child-group-a", childA.Keys[0].KeyValue)
	assert.Equal(t, models.KeyStatusActive, childA.Keys[0].Status)

	childB := byName["child-group-b"]
	assert.False(t, childB.Enabled)
	assert.Equal(t, 10, childB.Sort)

	childC := byName["child-group-c"]
	assert.JSONEq(t, `{"source":{"targets":[{"model":"target","weight":100}]}}`, string(childC.ModelRedirectRulesV2))

	groupExport, err := service.ExportGroup(parent.ID)
	require.NoError(t, err)
	require.Len(t, groupExport.ChildGroups, 3)
}

func TestImportSystemRestoresStandardChildGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))

	encryptionSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	service := NewImportExportService(db, NewBulkImportService(db), encryptionSvc)

	parentKey, err := encryptionSvc.Encrypt("parent-secret-key")
	require.NoError(t, err)
	childKeyA, err := encryptionSvc.Encrypt("child-a-secret-key")
	require.NoError(t, err)
	childKeyB, err := encryptionSvc.Encrypt("child-b-secret-key")
	require.NoError(t, err)
	childKeyC, err := encryptionSvc.Encrypt("child-c-secret-key")
	require.NoError(t, err)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "import-parent",
					DisplayName: "Import Parent",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					TestModel:   "gpt-4o-mini",
					Upstreams:   datatypes.JSON(`[{"url":"https://parent.example.com","weight":1}]`),
				},
				Keys: []KeyExportInfo{
					{KeyValue: parentKey, Status: models.KeyStatusInvalid},
				},
				ChildGroups: []ChildGroupExport{
					{
						Name:               "import-child-a",
						DisplayName:        "Import Child A",
						Description:        "child A description",
						Enabled:            true,
						Sort:               7,
						TestModel:          "child-a-test-model",
						Config:             json.RawMessage(`{"base_url":"https://child-a.example.com"}`),
						ModelRedirectRules: json.RawMessage(`{"gpt-4":"gpt-4o"}`),
						Keys: []KeyExportInfo{
							{KeyValue: childKeyA, Status: models.KeyStatusInvalid},
						},
					},
					{
						Name:        "import-child-b",
						DisplayName: "Import Child B",
						Description: "child B description",
						Enabled:     false,
						Sort:        8,
						TestModel:   "child-b-test-model",
						Config:      json.RawMessage(`{"base_url":"https://child-b.example.com"}`),
						Keys: []KeyExportInfo{
							{KeyValue: childKeyB, Status: models.KeyStatusActive},
						},
					},
					{
						Name:                 "import-child-c",
						DisplayName:          "Import Child C",
						Description:          "child C description",
						Enabled:              true,
						Sort:                 9,
						TestModel:            "child-c-test-model",
						ModelRedirectRulesV2: json.RawMessage(`{"source":{"targets":[{"model":"target","weight":100}]}}`),
						Keys: []KeyExportInfo{
							{KeyValue: childKeyC, Status: models.KeyStatusInvalid},
						},
					},
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var parent models.Group
	require.NoError(t, db.Where("name = ?", "import-parent").First(&parent).Error)
	assert.Nil(t, parent.ParentGroupID)

	var children []models.Group
	require.NoError(t, db.Where("parent_group_id = ?", parent.ID).Find(&children).Error)
	require.Len(t, children, 3)

	childrenByName := make(map[string]models.Group, len(children))
	for _, child := range children {
		childrenByName[child.Name] = child
	}
	require.Contains(t, childrenByName, "import-child-a")
	require.Contains(t, childrenByName, "import-child-b")
	require.Contains(t, childrenByName, "import-child-c")

	childA := childrenByName["import-child-a"]
	assert.Equal(t, "openai", childA.ChannelType)
	assert.Equal(t, "child-a-test-model", childA.TestModel)
	assert.JSONEq(t, `{"base_url":"https://child-a.example.com"}`, jsonString(t, childA.Config))
	assert.JSONEq(t, `{"gpt-4":"gpt-4o"}`, jsonString(t, childA.ModelRedirectRules))
	assert.JSONEq(t, `[{"url":"`+expectedProxyURL(parent.Name)+`","weight":1}]`, string(childA.Upstreams))

	childB := childrenByName["import-child-b"]
	assert.False(t, childB.Enabled)
	assert.Equal(t, 8, childB.Sort)

	childC := childrenByName["import-child-c"]
	assert.JSONEq(t, `{"source":{"targets":[{"model":"target","weight":100}]}}`, string(childC.ModelRedirectRulesV2))

	var parentKeys []models.APIKey
	require.NoError(t, db.Where("group_id = ?", parent.ID).Find(&parentKeys).Error)
	require.Len(t, parentKeys, 1)
	assert.Equal(t, models.KeyStatusActive, parentKeys[0].Status)
	decryptedParentKey, err := encryptionSvc.Decrypt(parentKeys[0].KeyValue)
	require.NoError(t, err)
	assert.Equal(t, "parent-secret-key", decryptedParentKey)

	expectedChildKeys := map[string]string{
		"import-child-a": "child-a-secret-key",
		"import-child-b": "child-b-secret-key",
		"import-child-c": "child-c-secret-key",
	}
	for childName, expectedKey := range expectedChildKeys {
		child := childrenByName[childName]
		var childKeys []models.APIKey
		require.NoError(t, db.Where("group_id = ?", child.ID).Find(&childKeys).Error)
		require.Len(t, childKeys, 1)
		assert.Equal(t, models.KeyStatusActive, childKeys[0].Status)
		decryptedChildKey, err := encryptionSvc.Decrypt(childKeys[0].KeyValue)
		require.NoError(t, err)
		assert.Equal(t, expectedKey, decryptedChildKey)
	}
}

func TestImportSystemRestoresAggregateSubGroupsAfterReferencedGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "aggregate-first",
					GroupType:   "aggregate",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[]`),
				},
				SubGroups: []SubGroupInfo{
					{GroupName: "standard-later-a", Weight: 3},
					{GroupName: "standard-later-b", Weight: 7},
				},
			},
			{
				Group: models.Group{
					Name:        "standard-later-a",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://a.example.com","weight":1}]`),
				},
			},
			{
				Group: models.Group{
					Name:        "standard-later-b",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://b.example.com","weight":1}]`),
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var aggregate models.Group
	require.NoError(t, db.Where("name = ?", "aggregate-first").First(&aggregate).Error)

	var relations []models.GroupSubGroup
	require.NoError(t, db.Where("group_id = ?", aggregate.ID).Find(&relations).Error)
	require.Len(t, relations, 2)

	weightsBySubGroupName := make(map[string]int, len(relations))
	for _, relation := range relations {
		var subGroup models.Group
		require.NoError(t, db.First(&subGroup, relation.SubGroupID).Error)
		weightsBySubGroupName[subGroup.Name] = relation.Weight
	}
	assert.Equal(t, 3, weightsBySubGroupName["standard-later-a"])
	assert.Equal(t, 7, weightsBySubGroupName["standard-later-b"])
}

func TestExportImportSystemRestoresDynamicWeightsByGroupName(t *testing.T) {
	t.Parallel()

	sourceDB := setupTestDB(t)
	require.NoError(t, sourceDB.AutoMigrate(&models.SystemSetting{}))
	sourceService := NewImportExportService(sourceDB, nil, nil)

	aggregate := models.Group{
		Name:        "dw-aggregate",
		GroupType:   "aggregate",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[]`),
	}
	require.NoError(t, sourceDB.Create(&aggregate).Error)

	standard := models.Group{
		Name:        "dw-standard",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[{"url":"https://standard.example.com","weight":1}]`),
	}
	require.NoError(t, sourceDB.Create(&standard).Error)

	subGroup := models.Group{
		Name:        "dw-sub",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[{"url":"https://sub.example.com","weight":1}]`),
	}
	require.NoError(t, sourceDB.Create(&subGroup).Error)
	require.NoError(t, sourceDB.Create(&models.GroupSubGroup{
		GroupID:    aggregate.ID,
		SubGroupID: subGroup.ID,
		Weight:     9,
	}).Error)

	lastFailureAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	lastSuccessAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	lastRateLimitAt := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	lastRolloverAt := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	deletedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	sourceMetrics := []models.DynamicWeightMetric{
		{
			MetricType:            models.MetricTypeGroup,
			GroupID:               standard.ID,
			ConsecutiveFailures:   2,
			LastFailureAt:         &lastFailureAt,
			LastSuccessAt:         &lastSuccessAt,
			ConsecutiveRateLimits: 1,
			LastRateLimitAt:       &lastRateLimitAt,
			Requests7d:            10,
			Successes7d:           8,
			RateLimits7d:          1,
			Requests14d:           15,
			Successes14d:          12,
			RateLimits14d:         2,
			Requests30d:           30,
			Successes30d:          25,
			RateLimits30d:         3,
			Requests90d:           90,
			Successes90d:          80,
			RateLimits90d:         4,
			Requests180d:          180,
			Successes180d:         160,
			RateLimits180d:        5,
			LastRolloverAt:        &lastRolloverAt,
		},
		{
			MetricType:          models.MetricTypeSubGroup,
			GroupID:             aggregate.ID,
			SubGroupID:          subGroup.ID,
			ConsecutiveFailures: 1,
			Requests7d:          20,
			Successes7d:         19,
			RateLimits7d:        1,
			Requests14d:         25,
			Successes14d:        23,
			RateLimits14d:       2,
			Requests30d:         40,
			Successes30d:        35,
			RateLimits30d:       3,
		},
		{
			MetricType:          models.MetricTypeModelRedirect,
			GroupID:             standard.ID,
			SourceModel:         "source-model",
			TargetModel:         "target-model",
			ConsecutiveFailures: 3,
			Requests7d:          5,
			Successes7d:         2,
			DeletedAt:           &deletedAt,
		},
	}
	require.NoError(t, sourceDB.Create(&sourceMetrics).Error)

	exported, err := sourceService.ExportSystem()
	require.NoError(t, err)
	require.Len(t, exported.DynamicWeights, 3)

	targetDB := setupTestDB(t)
	require.NoError(t, targetDB.AutoMigrate(&models.SystemSetting{}))
	targetService := NewImportExportService(targetDB, nil, nil)

	require.NoError(t, targetService.ImportSystem(targetDB, exported))

	var importedGroups []models.Group
	require.NoError(t, targetDB.Find(&importedGroups).Error)
	groupByName := make(map[string]models.Group, len(importedGroups))
	for _, group := range importedGroups {
		groupByName[group.Name] = group
	}

	var restored []models.DynamicWeightMetric
	require.NoError(t, targetDB.Order("metric_type ASC, group_id ASC, sub_group_id ASC, source_model ASC").Find(&restored).Error)
	require.Len(t, restored, 3)

	restoredByType := make(map[models.MetricType]models.DynamicWeightMetric, len(restored))
	for _, metric := range restored {
		restoredByType[metric.MetricType] = metric
	}

	groupMetric := restoredByType[models.MetricTypeGroup]
	assert.Equal(t, groupByName["dw-standard"].ID, groupMetric.GroupID)
	assert.Equal(t, int64(10), groupMetric.Requests7d)
	assert.Equal(t, int64(160), groupMetric.Successes180d)
	assert.Equal(t, int64(1), groupMetric.ConsecutiveRateLimits)
	assert.Equal(t, int64(5), groupMetric.RateLimits180d)
	require.NotNil(t, groupMetric.LastFailureAt)
	assert.True(t, groupMetric.LastFailureAt.Equal(lastFailureAt))
	require.NotNil(t, groupMetric.LastRateLimitAt)
	assert.True(t, groupMetric.LastRateLimitAt.Equal(lastRateLimitAt))

	subGroupMetric := restoredByType[models.MetricTypeSubGroup]
	assert.Equal(t, groupByName["dw-aggregate"].ID, subGroupMetric.GroupID)
	assert.Equal(t, groupByName["dw-sub"].ID, subGroupMetric.SubGroupID)
	assert.Equal(t, int64(19), subGroupMetric.Successes7d)
	assert.Equal(t, int64(3), subGroupMetric.RateLimits30d)

	modelMetric := restoredByType[models.MetricTypeModelRedirect]
	assert.Equal(t, groupByName["dw-standard"].ID, modelMetric.GroupID)
	assert.Equal(t, "source-model", modelMetric.SourceModel)
	assert.Equal(t, "target-model", modelMetric.TargetModel)
	require.NotNil(t, modelMetric.DeletedAt)
	assert.True(t, modelMetric.DeletedAt.Equal(deletedAt))

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	require.NoError(t, LoadDynamicWeightMetricsFromDatabase(targetDB, manager))

	hydratedGroupMetric, err := manager.GetGroupMetrics(groupMetric.GroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), hydratedGroupMetric.Requests7d)
	assert.Equal(t, int64(160), hydratedGroupMetric.Successes180d)
}

func TestImportSystemSkipsMissingDynamicWeightsForOldExports(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "old-export-group",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://old.example.com","weight":1}]`),
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var count int64
	require.NoError(t, db.Model(&models.DynamicWeightMetric{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestImportSystemRestoresDynamicWeightsForRenamedChildGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	existingChild := models.Group{
		Name:        "child-conflict",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[{"url":"https://existing.example.com","weight":1}]`),
	}
	require.NoError(t, db.Create(&existingChild).Error)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "parent-with-child-metric",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://parent.example.com","weight":1}]`),
				},
				ChildGroups: []ChildGroupExport{
					{
						Name:      "child-conflict",
						Enabled:   true,
						ProxyKeys: "proxy-key",
						Sort:      1,
					},
				},
			},
			{
				Group: models.Group{
					Name:        "aggregate-for-child-metric",
					GroupType:   "aggregate",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[]`),
				},
				SubGroups: []SubGroupInfo{
					{GroupName: "child-conflict", Weight: 5},
				},
			},
		},
		DynamicWeights: []DynamicWeightMetricExportInfo{
			{
				MetricType:   models.MetricTypeSubGroup,
				GroupName:    "aggregate-for-child-metric",
				SubGroupName: "child-conflict",
				Requests7d:   11,
				Successes7d:  10,
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var importedChild models.Group
	require.NoError(t, db.Where("name <> ? AND parent_group_id IS NOT NULL", existingChild.Name).First(&importedChild).Error)

	var metric models.DynamicWeightMetric
	require.NoError(t, db.Where("metric_type = ?", models.MetricTypeSubGroup).First(&metric).Error)
	assert.Equal(t, importedChild.ID, metric.SubGroupID)
	assert.Equal(t, int64(11), metric.Requests7d)
	assert.Equal(t, int64(10), metric.Successes7d)
}

func TestImportSystemInfersLegacyChildGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "legacy-parent",
					DisplayName: "Legacy Parent",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "parent-proxy",
					Upstreams:   datatypes.JSON(`[{"url":"https://legacy.example.com/v1","weight":1}]`),
				},
			},
			{
				Group: models.Group{
					Name:        "legacy-parent_fovt",
					DisplayName: "Legacy Parent Fovt",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "child-proxy",
					Sort:        20,
					Upstreams:   datatypes.JSON(`[{"url":"https://legacy.example.com/v1","weight":1}]`),
				},
			},
			{
				Group: models.Group{
					Name:        "legacy-default_default",
					DisplayName: "Legacy Default 默认",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "default-proxy",
					Upstreams:   datatypes.JSON(`[{"url":"https://default.example.com/v1","weight":1}]`),
				},
			},
			{
				Group: models.Group{
					Name:        "legacy-default_alt",
					DisplayName: "Legacy Default Alt",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "alt-proxy",
					Sort:        30,
					Upstreams:   datatypes.JSON(`[{"url":"https://default.example.com/v1","weight":1}]`),
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var parent models.Group
	require.NoError(t, db.Where("name = ?", "legacy-parent").First(&parent).Error)
	var prefixChild models.Group
	require.NoError(t, db.Where("name = ?", "legacy-parent_fovt").First(&prefixChild).Error)
	require.NotNil(t, prefixChild.ParentGroupID)
	assert.Equal(t, parent.ID, *prefixChild.ParentGroupID)
	assert.Equal(t, "child-proxy", prefixChild.ProxyKeys)

	var defaultParent models.Group
	require.NoError(t, db.Where("name = ?", "legacy-default_default").First(&defaultParent).Error)
	var defaultChild models.Group
	require.NoError(t, db.Where("name = ?", "legacy-default_alt").First(&defaultChild).Error)
	require.NotNil(t, defaultChild.ParentGroupID)
	assert.Equal(t, defaultParent.ID, *defaultChild.ParentGroupID)
}

func TestImportSystemDoesNotInferUnrelatedSameUpstreamGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	service := NewImportExportService(db, nil, nil)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "openrouter",
					DisplayName: "OpenRouter",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "openrouter-proxy",
					Upstreams:   datatypes.JSON(`[{"url":"https://api-proxy.example.com/v1","weight":1}]`),
				},
			},
			{
				Group: models.Group{
					Name:        "openai",
					DisplayName: "OpenAI",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					ProxyKeys:   "openai-proxy",
					Upstreams:   datatypes.JSON(`[{"url":"https://api-proxy.example.com/v1","weight":1}]`),
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var groups []models.Group
	require.NoError(t, db.Order("name ASC").Find(&groups).Error)
	require.Len(t, groups, 2)
	for _, group := range groups {
		assert.Nil(t, group.ParentGroupID)
	}
}

func TestImportSystemRollsBackFailedGroupImport(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}))
	encryptionSvc, err := encryption.NewService("test-encryption-key-long-enough")
	require.NoError(t, err)
	service := NewImportExportService(db, nil, encryptionSvc)

	importData := &SystemExportData{
		Version: "2.0",
		Groups: []GroupExportData{
			{
				Group: models.Group{
					Name:        "bad-partial-group",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://bad.example.com","weight":1}]`),
				},
				Keys: []KeyExportInfo{{KeyValue: "not-encrypted", Status: "active"}},
			},
			{
				Group: models.Group{
					Name:        "good-group-after-failure",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					Upstreams:   datatypes.JSON(`[{"url":"https://good.example.com","weight":1}]`),
				},
			},
		},
	}

	require.NoError(t, service.ImportSystem(db, importData))

	var badCount int64
	require.NoError(t, db.Model(&models.Group{}).Where("name = ?", "bad-partial-group").Count(&badCount).Error)
	assert.Zero(t, badCount)

	var good models.Group
	require.NoError(t, db.Where("name = ?", "good-group-after-failure").First(&good).Error)
}

func jsonString(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}
