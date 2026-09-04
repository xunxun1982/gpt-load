package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gpt-load/internal/db"
	"gpt-load/internal/failover"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/syncer"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const SettingsUpdateChannel = "system_settings:updated"

// MinCodexAffinityAttempts keeps write-time validation and proxy runtime clamping aligned.
const MinCodexAffinityAttempts = 1

// MaxCodexAffinityAttempts keeps write-time validation and proxy runtime clamping aligned.
const MaxCodexAffinityAttempts = 500

// SystemSettingsManager manages system configuration.
type SystemSettingsManager struct {
	syncer           *syncer.CacheSyncer[types.SystemSettings]
	proxyURLResolver ProxyURLResolver
}

// ProxyURLResolver resolves runtime proxy config values such as manual proxy pool references.
type ProxyURLResolver interface {
	ResolveProxyURL(ctx context.Context, raw string) (string, error)
}

// ProxyURLBatchResolver optionally resolves multiple proxy-pool references in one bounded query.
type ProxyURLBatchResolver interface {
	ResolveProxyURLs(ctx context.Context, refs []string) (map[string]string, error)
}

// NewSystemSettingsManager creates a new, uninitialized SystemSettingsManager.
func NewSystemSettingsManager() *SystemSettingsManager {
	return &SystemSettingsManager{}
}

// SetProxyURLResolver configures runtime resolution for manual proxy pool references.
func (sm *SystemSettingsManager) SetProxyURLResolver(resolver ProxyURLResolver) {
	sm.proxyURLResolver = resolver
}

func validateStringSettingValue(key, val string) error {
	if key == "failover_status_codes" {
		if _, err := failover.ParseStatusCodeMatcher(val); err != nil {
			return fmt.Errorf("invalid value for %s (%q): %w", key, val, err)
		}
	}
	if key == "proxy_url" {
		if val == "" || utils.IsProxyPoolRef(val) {
			return nil
		}
		if _, err := utils.NormalizeProxyURL(val); err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
	}
	if key == "proxy_pool_test_target_url" {
		parsed, err := url.Parse(strings.TrimSpace(val))
		if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid value for %s: must be an absolute http or https URL", key)
		}
	}
	return nil
}

type groupManager interface {
	Invalidate() error
}

// Initialize initializes the SystemSettingsManager with database and store dependencies.
func (sm *SystemSettingsManager) Initialize(store store.Store, gm groupManager, isMaster bool) error {
	settingsLoader := func() (types.SystemSettings, error) {
		var dbSettings []models.SystemSetting
		if err := db.DB.Find(&dbSettings).Error; err != nil {
			return types.SystemSettings{}, fmt.Errorf("failed to load system settings from db: %w", err)
		}

		settingsMap := make(map[string]string)
		for _, setting := range dbSettings {
			settingsMap[setting.SettingKey] = setting.SettingValue
		}
		// migrateSettingKey delegates to the shared transactional rename helper:
		// the legacy value is read INSIDE the transaction, so a concurrent
		// legacy-key update either becomes the migrated value or makes the whole
		// migration fail (row lock) instead of silently overwriting it with a
		// stale snapshot value. The authoritative replacement value is returned
		// for the in-memory map. Works on SQLite/MySQL/PostgreSQL.
		migrateSettingKey := func(tx *gorm.DB, from, to string) (string, bool, error) {
			if _, ok := settingsMap[from]; !ok {
				return "", false, nil
			}
			return migrateSettingKeyInTx(tx, from, to)
		}
		// One-time, intentionally incompatible renames (no backward compatibility is kept):
		//   1) non_stream_request_timeout -> request_timeout  (request_timeout now bounds
		//      both stream and non-stream request lifecycles; see types.SystemSettings)
		//   2) stream_request_timeout -> stream_first_byte_timeout
		if _, has := settingsMap["non_stream_request_timeout"]; has {
			if _, conflict := settingsMap["request_timeout"]; conflict {
				delete(settingsMap, "non_stream_request_timeout")
				if err := db.DB.Where("setting_key = ?", "non_stream_request_timeout").Delete(&models.SystemSetting{}).Error; err != nil {
					logrus.WithError(err).Warn("Failed to remove obsolete non-stream request timeout setting")
				}
			} else if err := db.DB.Transaction(func(tx *gorm.DB) error {
				value, migrated, err := migrateSettingKey(tx, "non_stream_request_timeout", "request_timeout")
				if err != nil {
					return err
				}
				if migrated {
					settingsMap["request_timeout"] = value
				}
				return nil
			}); err != nil {
				logrus.WithError(err).Warn("Failed to migrate non-stream request timeout setting")
				// Keep the legacy value for this load (mirrors the stream
				// branch): without it the reflector would fall back to the
				// default request_timeout while the DB row still holds the
				// custom legacy value, until a later reload retries the
				// migration.
				settingsMap["request_timeout"] = settingsMap["non_stream_request_timeout"]
			} else {
				delete(settingsMap, "non_stream_request_timeout")
			}
		}
		if _, ok := settingsMap["stream_request_timeout"]; ok {
			if _, exists := settingsMap["stream_first_byte_timeout"]; !exists {
				if err := db.DB.Transaction(func(tx *gorm.DB) error {
					value, migrated, err := migrateSettingKey(tx, "stream_request_timeout", "stream_first_byte_timeout")
					if err != nil {
						return err
					}
					if migrated {
						settingsMap["stream_first_byte_timeout"] = value
					}
					return nil
				}); err != nil {
					logrus.WithError(err).Warn("Failed to migrate stream request timeout setting")
					// Keep the legacy value under the replacement key for this
					// load (mirrors the non_stream branch): dropping it here
					// would leave neither key in the map, so the default 0
					// would silently disable the first-byte timeout until a
					// later reload succeeds.
					settingsMap["stream_first_byte_timeout"] = settingsMap["stream_request_timeout"]
				} else {
					delete(settingsMap, "stream_request_timeout")
				}
			} else if err := db.DB.Where("setting_key = ?", "stream_request_timeout").Delete(&models.SystemSetting{}).Error; err != nil {
				logrus.WithError(err).Warn("Failed to remove obsolete stream request timeout setting")
			} else {
				delete(settingsMap, "stream_request_timeout")
			}
		}

		// Start with default settings, then override with values from the database.
		settings := utils.DefaultSystemSettings()
		v := reflect.ValueOf(&settings).Elem()
		t := v.Type()
		jsonToField := make(map[string]string)
		for i := range t.NumField() {
			field := t.Field(i)
			jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
			if jsonTag != "" {
				jsonToField[jsonTag] = field.Name
			}
		}

		for key, valStr := range settingsMap {
			if fieldName, ok := jsonToField[key]; ok {
				fieldValue := v.FieldByName(fieldName)
				if fieldValue.IsValid() && fieldValue.CanSet() {
					if err := utils.SetFieldFromString(fieldValue, valStr); err != nil {
						logrus.Warnf("Failed to set value from map for field %s: %v", fieldName, err)
					}
				}
			}
		}

		settings.ProxyKeysMap = utils.StringToSet(settings.ProxyKeys, ",")

		sm.DisplaySystemConfig(settings)

		return settings, nil
	}

	afterLoader := func(newData types.SystemSettings) {
		if !isMaster {
			return
		}
		gm.Invalidate()
	}

	syncer, err := syncer.NewCacheSyncer(
		settingsLoader,
		store,
		SettingsUpdateChannel,
		logrus.WithField("syncer", "system_settings"),
		afterLoader,
	)
	if err != nil {
		return fmt.Errorf("failed to create system settings syncer: %w", err)
	}

	sm.syncer = syncer
	return nil
}

// Stop gracefully stops the SystemSettingsManager's background syncer.
func (sm *SystemSettingsManager) Stop(ctx context.Context) {
	if sm.syncer != nil {
		sm.syncer.Stop()
	}
}

// EnsureSettingsInitialized ensures all system setting records exist in the database.
func (sm *SystemSettingsManager) EnsureSettingsInitialized(authConfig types.AuthConfig) error {
	// Migrate legacy timeout keys BEFORE inserting defaults: EnsureSettingsInitialized
	// runs before SystemSettingsManager.Initialize's loader, so without this step the
	// default request_timeout/stream_first_byte_timeout rows would be inserted first
	// and the loader would see both key forms, delete the legacy key, and lose the
	// user's custom value. Migrating here preserves the custom value and removes the
	// legacy key so the loader's conflict branch never fires for these keys.
	if err := migrateLegacyTimeoutSettings(); err != nil {
		return err
	}

	defaultSettings := utils.DefaultSystemSettings()
	metadata := utils.GenerateSettingsMetadata(&defaultSettings)

	for _, meta := range metadata {
		var existing models.SystemSetting
		err := db.DB.Where("setting_key = ?", meta.Key).First(&existing).Error
		if err != nil {
			value := fmt.Sprintf("%v", meta.DefaultValue)
			if meta.Key == "app_url" {
				host := os.Getenv("HOST")
				if host == "" || host == "0.0.0.0" {
					host = "localhost"
				}
				port := os.Getenv("PORT")
				if port == "" {
					port = "3001"
				}
				value = fmt.Sprintf("http://%s:%s", host, port)
			}

			if meta.Key == "proxy_keys" {
				value = authConfig.Key
			}

			setting := models.SystemSetting{
				SettingKey:   meta.Key,
				SettingValue: value,
				Description:  meta.Description,
			}
			if err := db.DB.Create(&setting).Error; err != nil {
				logrus.Errorf("Failed to initialize setting %s: %v", setting.SettingKey, err)
				return err
			}
			logrus.Infof("Initialized system setting: %s = %s", setting.SettingKey, sanitizedSettingValueForLog(setting.SettingKey, setting.SettingValue))
		}
	}

	return nil
}

// migrateLegacyTimeoutSettings migrates the two intentional, incompatible system
// setting key renames (non_stream_request_timeout -> request_timeout,
// stream_request_timeout -> stream_first_byte_timeout) before defaults are
// inserted. It preserves the stored value when the replacement key is absent and
// removes the legacy key when the replacement already exists. Works on
// SQLite/MySQL/PostgreSQL via migrateSettingKeyInTx (the same helper the loader
// uses). This function runs once before defaults are inserted and fails closed:
// any migration error aborts startup so defaults are never inserted over an
// unmigrated legacy value. The loader path is the idempotent runtime fallback
// for keys that appear later (e.g. manual DB edits) and only warns on failure.
func migrateLegacyTimeoutSettings() error {
	// Load current keys once to decide whether a migration can apply at all.
	var dbSettings []models.SystemSetting
	if err := db.DB.Find(&dbSettings).Error; err != nil {
		return fmt.Errorf("failed to load system settings for migration: %w", err)
	}
	settingsMap := make(map[string]string, len(dbSettings))
	for _, setting := range dbSettings {
		settingsMap[setting.SettingKey] = setting.SettingValue
	}

	if _, has := settingsMap["non_stream_request_timeout"]; has {
		if _, conflict := settingsMap["request_timeout"]; conflict {
			// Replacement already exists: drop the legacy key, keep the replacement.
			if err := db.DB.Where("setting_key = ?", "non_stream_request_timeout").Delete(&models.SystemSetting{}).Error; err != nil {
				return fmt.Errorf("failed to remove obsolete non_stream_request_timeout setting: %w", err)
			}
		} else if err := db.DB.Transaction(func(tx *gorm.DB) error {
			_, _, err := migrateSettingKeyInTx(tx, "non_stream_request_timeout", "request_timeout")
			return err
		}); err != nil {
			return fmt.Errorf("failed to migrate non_stream_request_timeout setting: %w", err)
		}
	}
	if _, ok := settingsMap["stream_request_timeout"]; ok {
		if _, exists := settingsMap["stream_first_byte_timeout"]; !exists {
			if err := db.DB.Transaction(func(tx *gorm.DB) error {
				_, _, err := migrateSettingKeyInTx(tx, "stream_request_timeout", "stream_first_byte_timeout")
				return err
			}); err != nil {
				return fmt.Errorf("failed to migrate stream_request_timeout setting: %w", err)
			}
		} else if err := db.DB.Where("setting_key = ?", "stream_request_timeout").Delete(&models.SystemSetting{}).Error; err != nil {
			return fmt.Errorf("failed to remove obsolete stream_request_timeout setting: %w", err)
		}
	}
	return nil
}

// migrateSettingKeyInTx atomically renames one setting key to another inside the
// caller's transaction and reports whether a migration happened plus the
// authoritative value now stored under the replacement key. The legacy row is
// read with SELECT ... FOR UPDATE inside the transaction (never from a
// pre-transaction snapshot): the row lock makes the read-delete-create sequence
// atomic against concurrent source-key writers, so a concurrent update either
// commits before the locking read (its value is migrated) or waits for the
// transaction to commit and then hits the deleted row (a no-op) — the newer
// value is never overwritten by a stale snapshot. A source row that has already
// vanished (e.g. migrated by another instance) is reported as not migrated.
// Works on SQLite/MySQL/PostgreSQL; on conflict only updated_at of the
// replacement is touched (never setting_value).
func migrateSettingKeyInTx(tx *gorm.DB, from, to string) (string, bool, error) {
	var legacy models.SystemSetting
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("setting_key = ?", from).First(&legacy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The source key is already gone (e.g. migrated by another
			// instance): nothing to migrate, and the replacement, if any, is
			// authoritative.
			return "", false, nil
		}
		return "", false, err
	}
	if err := tx.Where("setting_key = ?", from).Delete(&models.SystemSetting{}).Error; err != nil {
		return "", false, err
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"updated_at"}),
	}).Create(&models.SystemSetting{SettingKey: to, SettingValue: legacy.SettingValue}).Error; err != nil {
		return "", false, err
	}
	var stored models.SystemSetting
	if err := tx.Where("setting_key = ?", to).First(&stored).Error; err != nil {
		return "", false, err
	}
	return stored.SettingValue, true, nil
}

func sanitizedSettingValueForLog(key, value string) string {
	switch key {
	case "proxy_keys", "proxy_url":
		if strings.TrimSpace(value) == "" {
			return ""
		}
		return "[REDACTED]"
	default:
		return value
	}
}

// GetSettings gets the current system configuration.
func (sm *SystemSettingsManager) GetSettings() types.SystemSettings {
	if sm.syncer == nil {
		logrus.Warn("SystemSettingsManager is not initialized, returning default settings.")
		return utils.DefaultSystemSettings()
	}
	return sm.syncer.Get()
}

// GetAppUrl returns the effective App URL.
func (sm *SystemSettingsManager) GetAppUrl() string {
	settings := sm.GetSettings()
	if settings.AppUrl != "" {
		return settings.AppUrl
	}

	host := os.Getenv("HOST")
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "3001"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// UpdateSettings updates system configuration.
func (sm *SystemSettingsManager) UpdateSettings(settingsMap map[string]any) error {
	// Normalize one-off, intentionally incompatible legacy key renames BEFORE
	// validation, so an old-style payload still lands in the new fields instead of
	// being rejected as an unknown key.
	NormalizeLegacyGroupTimeoutConfig(datatypes.JSONMap(settingsMap))

	// Validate configuration items
	if err := sm.ValidateSettings(settingsMap); err != nil {
		return err
	}

	// Update database
	var settingsToUpdate []models.SystemSetting
	for key, value := range settingsMap {
		settingsToUpdate = append(settingsToUpdate, models.SystemSetting{
			SettingKey:   key,
			SettingValue: fmt.Sprintf("%v", value), // Convert any to string
		})
	}

	if len(settingsToUpdate) > 0 {
		if err := db.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "setting_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"setting_value", "updated_at"}),
		}).Create(&settingsToUpdate).Error; err != nil {
			return fmt.Errorf("failed to update system settings: %w", err)
		}
	}

	// Trigger all instances to reload
	return sm.syncer.Invalidate()
}

// ReloadSettings forces a synchronous reload of settings from the database.
// This method is useful after importing settings to ensure cache is immediately updated.
func (sm *SystemSettingsManager) ReloadSettings() error {
	if sm.syncer == nil {
		return fmt.Errorf("SystemSettingsManager is not initialized")
	}

	// Force synchronous reload of the cache
	if err := sm.syncer.Reload(); err != nil {
		return fmt.Errorf("failed to reload settings: %w", err)
	}

	logrus.Info("System settings cache reloaded synchronously")

	// Also notify other instances to reload
	if err := sm.syncer.Invalidate(); err != nil {
		logrus.Warnf("Failed to notify other instances: %v", err)
		// Don't return error as local reload was successful
	}

	return nil
}

// GetEffectiveConfig gets effective configuration (system config + group overrides)
func (sm *SystemSettingsManager) GetEffectiveConfig(groupConfigJSON datatypes.JSONMap) types.SystemSettings {
	effectiveConfig := sm.GetSettings()

	// Normalize legacy group config keys in-place so callers see migrated values immediately.
	NormalizeLegacyGroupTimeoutConfig(groupConfigJSON)

	if groupConfigJSON == nil {
		effectiveConfig.ProxyURL = sm.ResolveRuntimeProxyURL(context.Background(), effectiveConfig.ProxyURL)
		return effectiveConfig
	}

	var groupConfig models.GroupConfig
	groupConfigBytes, err := groupConfigJSON.MarshalJSON()
	if err != nil {
		logrus.Warnf("Failed to marshal group config JSON, using system settings only. Error: %v", err)
		effectiveConfig.ProxyURL = sm.ResolveRuntimeProxyURL(context.Background(), effectiveConfig.ProxyURL)
		return effectiveConfig
	}
	if err := json.Unmarshal(groupConfigBytes, &groupConfig); err != nil {
		logrus.Warnf("Failed to unmarshal group config, using system settings only. Error: %v", err)
		effectiveConfig.ProxyURL = sm.ResolveRuntimeProxyURL(context.Background(), effectiveConfig.ProxyURL)
		return effectiveConfig
	}

	gcv := reflect.ValueOf(groupConfig)
	ecv := reflect.ValueOf(&effectiveConfig).Elem()

	for i := range gcv.NumField() {
		groupField := gcv.Field(i)
		if groupField.Kind() == reflect.Ptr && !groupField.IsNil() {
			groupFieldValue := groupField.Elem()
			effectiveField := ecv.FieldByName(gcv.Type().Field(i).Name)
			if effectiveField.IsValid() && effectiveField.CanSet() {
				if effectiveField.Type() == groupFieldValue.Type() {
					effectiveField.Set(groupFieldValue)
				}
			}
		}
	}
	effectiveConfig.ProxyURL = sm.ResolveRuntimeProxyURL(context.Background(), effectiveConfig.ProxyURL)

	return effectiveConfig
}

// NormalizeLegacyGroupTimeoutConfig migrates the two intentional, incompatible group
// config key renames in place: non_stream_request_timeout -> request_timeout and
// stream_request_timeout -> stream_first_byte_timeout. A legacy value is copied only
// when the replacement key is absent, so a concurrently-written value is never
// clobbered. It reports whether the map changed, letting the GroupManager loader persist
// the migrated JSON once.. SQLite/MySQL/PostgreSQL JSON maps ingest ordinary Go maps,
// so no dialect-specific handling is needed here.

func NormalizeLegacyGroupTimeoutConfig(config datatypes.JSONMap) bool {
	if config == nil {
		return false
	}
	changed := false
	if oldValue, ok := config["non_stream_request_timeout"]; ok {
		if _, exists := config["request_timeout"]; !exists {
			config["request_timeout"] = oldValue
		}
		delete(config, "non_stream_request_timeout")
		changed = true
	}
	if oldValue, ok := config["stream_request_timeout"]; ok {
		if _, exists := config["stream_first_byte_timeout"]; !exists {
			config["stream_first_byte_timeout"] = oldValue
		}
		delete(config, "stream_request_timeout")
		changed = true
	}
	return changed
}

// ResolveRuntimeProxyURL returns the actual proxy URL for runtime use.
func (sm *SystemSettingsManager) ResolveRuntimeProxyURL(ctx context.Context, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !utils.IsProxyPoolRef(trimmed) {
		return trimmed
	}
	if sm.proxyURLResolver == nil {
		logrus.Warn("Proxy pool reference cannot be resolved because no resolver is configured")
		return trimmed
	}
	resolved, err := sm.proxyURLResolver.ResolveProxyURL(ctx, trimmed)
	if err != nil {
		logrus.WithError(err).Warn("Failed to resolve proxy pool reference")
		return trimmed
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		logrus.Warn("Proxy pool reference resolved to an empty proxy URL")
		return trimmed
	}
	return resolved
}

// ResolveRuntimeProxyURLs resolves distinct proxy-pool references as one batch when supported.
// Failed or missing resolutions remain references so outbound clients fail closed.
func (sm *SystemSettingsManager) ResolveRuntimeProxyURLs(ctx context.Context, refs []string) map[string]string {
	resolved := make(map[string]string, len(refs))
	pending := make([]string, 0, len(refs))
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" || !utils.IsProxyPoolRef(ref) {
			resolved[ref] = ref
			continue
		}
		if _, exists := resolved[ref]; exists {
			continue
		}
		resolved[ref] = ref
		pending = append(pending, ref)
	}
	if len(pending) == 0 {
		return resolved
	}
	if sm.proxyURLResolver == nil {
		logrus.WithField("reference_count", len(pending)).Warn("Proxy pool references cannot be resolved because no resolver is configured")
		return resolved
	}

	batchResolver, isBatchResolver := sm.proxyURLResolver.(ProxyURLBatchResolver)
	var batch map[string]string
	var err error
	if isBatchResolver {
		batch, err = batchResolver.ResolveProxyURLs(ctx, pending)
	} else {
		batch = make(map[string]string, len(pending))
		for _, ref := range pending {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if err == nil {
					err = ctxErr
				}
				break
			}
			value, resolveErr := sm.proxyURLResolver.ResolveProxyURL(ctx, ref)
			if resolveErr != nil {
				// Keep resolving later references so one stale pool entry does not
				// hide valid entries in the same bounded batch.
				if err == nil {
					err = resolveErr
				}
				continue
			}
			batch[ref] = value
		}
	}
	if err != nil {
		if !isBatchResolver {
			// Preserve successful non-batch resolutions; unresolved entries keep their references below.
			for ref, value := range batch {
				if value = strings.TrimSpace(value); value != "" {
					resolved[ref] = value
				}
			}
		}
		logrus.WithError(err).WithField("reference_count", len(pending)).Warn("Failed to resolve proxy pool references")
		return resolved
	}

	missing := 0
	for _, ref := range pending {
		value := strings.TrimSpace(batch[ref])
		if value == "" {
			missing++
			continue
		}
		resolved[ref] = value
	}
	if missing > 0 {
		logrus.WithField("missing_count", missing).Warn("Some proxy pool references could not be resolved")
	}
	return resolved
}

// ValidateSettings validates the validity of system configuration
func (sm *SystemSettingsManager) ValidateSettings(settingsMap map[string]any) error {
	tempSettings := utils.DefaultSystemSettings()
	v := reflect.ValueOf(&tempSettings).Elem()
	t := v.Type()
	jsonToField := make(map[string]reflect.StructField)
	for i := range t.NumField() {
		field := t.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag != "" {
			jsonToField[jsonTag] = field
		}
	}

	for key, value := range settingsMap {
		field, ok := jsonToField[key]
		if !ok {
			return fmt.Errorf("invalid setting key: %s", key)
		}

		validateTag := field.Tag.Get("validate")
		rules := strings.Split(validateTag, ",")

		switch field.Type.Kind() {
		case reflect.Int:
			floatVal, ok := value.(float64)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a number, got %T", key, value)
			}
			intVal := int(floatVal)
			if floatVal != float64(intVal) {
				return fmt.Errorf("invalid value for %s: must be an integer", key)
			}

			// The 'required' check is implicitly handled by the type assertion above.
			for _, rule := range rules {
				trimmedRule := strings.TrimSpace(rule)
				if strings.HasPrefix(trimmedRule, "min=") {
					minValStr := strings.TrimPrefix(trimmedRule, "min=")
					minVal, _ := strconv.Atoi(minValStr)
					if intVal < minVal {
						return fmt.Errorf("value for %s (%d) is below minimum value (%d)", key, intVal, minVal)
					}
				}
			}
		case reflect.Bool:
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("invalid type for %s: expected a boolean, got %T", key, value)
			}
		case reflect.String:
			strVal, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			for _, rule := range rules {
				trimmedRule := strings.TrimSpace(rule)
				if trimmedRule == "required" {
					if strVal == "" {
						return fmt.Errorf("value for %s is required", key)
					}
				}
			}
			if err := validateStringSettingValue(key, strVal); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported type for setting key validation: %s", key)
		}
	}

	return nil
}

// ValidateGroupConfigOverrides validates a map of group-level configuration overrides.
func (sm *SystemSettingsManager) ValidateGroupConfigOverrides(configMap map[string]any) error {
	if forceStream, ok := configMap["force_stream"].(bool); ok && forceStream {
		if forceNonStream, ok := configMap["force_non_stream"].(bool); ok && forceNonStream {
			return fmt.Errorf("force_stream and force_non_stream cannot both be enabled")
		}
	}

	tempSettings := types.SystemSettings{}
	v := reflect.ValueOf(&tempSettings).Elem()
	t := v.Type()
	jsonToField := make(map[string]reflect.StructField)
	for i := range t.NumField() {
		field := t.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag != "" {
			jsonToField[jsonTag] = field
		}
	}

	for key, value := range configMap {
		if value == nil {
			continue
		}

		if key == "codex_affinity_max_retries" {
			intVal, err := integerConfigValue(key, value)
			if err != nil {
				return err
			}
			if intVal < MinCodexAffinityAttempts {
				return fmt.Errorf("value for %s (%d) is below minimum value (%d)", key, intVal, MinCodexAffinityAttempts)
			}
			if intVal > MaxCodexAffinityAttempts {
				return fmt.Errorf("value for %s (%d) exceeds maximum value (%d)", key, intVal, MaxCodexAffinityAttempts)
			}
			continue
		}

		// Allow group-only override keys that are not part of system-level settings metadata.
		// Currently this is used for aggregate group sub-group retry configuration.
		if key == "sub_max_retries" {
			floatVal, ok := value.(float64)
			if !ok {
				// For safety, accept integer-like types from potential import flows.
				switch numVal := value.(type) {
				case int:
					floatVal = float64(numVal)
				case int64:
					floatVal = float64(numVal)
				default:
					return fmt.Errorf("invalid type for %s: expected a number, got %T", key, value)
				}
			}
			intVal := int(floatVal)
			if floatVal != float64(intVal) {
				return fmt.Errorf("invalid value for %s: must be an integer", key)
			}
			if intVal < 0 {
				return fmt.Errorf("value for %s (%d) is below minimum value (%d)", key, intVal, 0)
			}
			// Upper bound (0-500) is enforced at runtime by proxy server clamping logic.
			continue
		}

		if key == "health_reset_interval_seconds" {
			intVal, err := integerConfigValue(key, value)
			if err != nil {
				return err
			}
			// Zero intentionally disables scheduled health resets; enabled intervals start at 30 minutes.
			if intVal < 0 {
				return fmt.Errorf("value for %s (%d) is below minimum value (%d)", key, intVal, 0)
			}
			const minHealthResetIntervalSeconds = 30 * 60
			if intVal > 0 && intVal < minHealthResetIntervalSeconds {
				return fmt.Errorf("value for %s (%d) is below minimum enabled value (%d)", key, intVal, minHealthResetIntervalSeconds)
			}
			const maxHealthResetIntervalSeconds = 365 * 24 * 60 * 60
			if intVal > maxHealthResetIntervalSeconds {
				return fmt.Errorf("value for %s (%d) exceeds maximum value (%d)", key, intVal, maxHealthResetIntervalSeconds)
			}
			continue
		}

		// Allow group-only boolean flags for experimental features.
		// These flags are stored only at group level and are not part of system-level settings.
		if key == "force_function_call" || key == "cc_support" || key == "codex_support" ||
			key == "intercept_event_log" || key == "parallel_tool_calls" ||
			key == "shorten_tool_names" || key == "validation_stream" ||
			key == "force_stream" || key == "force_non_stream" ||
			key == "responses_include_encrypted_reasoning" ||
			key == "responses_legacy_user_role" ||
			key == "codex_degradation_mitigation_enabled" ||
			key == "codex_affinity_enabled" {
			// Accept only boolean values; nil is already skipped above.
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("invalid type for %s: expected a boolean, got %T", key, value)
			}
			continue
		}

		// Allow validation_prompt_mode string field for key validation prompts.
		// Values: "default", "random_queue".
		if key == "validation_prompt_mode" {
			mode, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "" {
				mode = "default"
			}
			validModes := map[string]bool{"default": true, "random_queue": true}
			if !validModes[mode] {
				return fmt.Errorf("invalid value for %s: must be 'default' or 'random_queue'", key)
			}
			configMap[key] = mode
			continue
		}

		// Allow simulated_client string field for explicit client fingerprint presets.
		// Values: "off" (default), "codex", "claude_code".
		if key == "simulated_client" {
			mode, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "" {
				mode = "off"
			}
			validModes := map[string]bool{"off": true, "codex": true, "claude_code": true}
			if !validModes[mode] {
				return fmt.Errorf("invalid value for %s: must be 'off', 'codex', or 'claude_code'", key)
			}
			configMap[key] = mode
			continue
		}

		if key == "simulated_codex_version" || key == "simulated_claude_code_version" {
			version, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			version = strings.TrimSpace(version)
			if version == "" {
				delete(configMap, key)
				continue
			}
			if !utils.IsDottedNumericVersion(version) {
				return fmt.Errorf("invalid value for %s: must be a dotted numeric version like 1.2 or 1.2.3", key)
			}
			configMap[key] = version
			continue
		}

		// Allow thinking_model string field for CC support extended thinking.
		// This specifies the model to use when Claude Code enables extended thinking mode.
		if key == "thinking_model" {
			strVal, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			if strings.TrimSpace(strVal) != "" {
				ccEnabled, ok := configMap["cc_support"].(bool)
				if !ok || !ccEnabled {
					return fmt.Errorf("thinking_model can only be set when cc_support is enabled")
				}
			}
			continue
		}

		// Allow codex_instructions string field for OpenAI Responses CC support.
		// This specifies custom instructions for providers that validate this field strictly.
		if key == "codex_instructions" {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			continue
		}

		// Allow codex_instructions_mode string field for OpenAI Responses CC support.
		// Values: "auto", "official", "custom"
		if key == "codex_instructions_mode" {
			mode, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected a string, got %T", key, value)
			}
			// Normalize input: trim whitespace and convert to lowercase for case-insensitive matching
			mode = strings.ToLower(strings.TrimSpace(mode))
			validModes := map[string]bool{"auto": true, "official": true, "custom": true}
			if !validModes[mode] {
				return fmt.Errorf("invalid value for %s: must be 'auto', 'official', or 'custom'", key)
			}
			// Persist the normalized value back to configMap for consistent storage
			configMap[key] = mode
			continue
		}

		field, ok := jsonToField[key]
		if !ok {
			return fmt.Errorf("invalid setting key: %s", key)
		}

		validateTag := field.Tag.Get("validate")
		rules := strings.Split(validateTag, ",")

		switch field.Type.Kind() {
		case reflect.Int:
			floatVal, ok := value.(float64)
			if !ok {
				continue
			}
			intVal := int(floatVal)
			if floatVal != float64(intVal) {
				return fmt.Errorf("invalid value for %s: must be an integer", key)
			}

			// The 'required' check is implicitly handled by the type assertion above.
			for _, rule := range rules {
				trimmedRule := strings.TrimSpace(rule)
				if strings.HasPrefix(trimmedRule, "min=") {
					minValStr := strings.TrimPrefix(trimmedRule, "min=")
					minVal, _ := strconv.Atoi(minValStr)
					if intVal < minVal {
						return fmt.Errorf("value for %s (%d) is below minimum value (%d)", key, intVal, minVal)
					}
				}
			}
		case reflect.String:
			strVal, ok := value.(string)
			if !ok {
				continue
			}
			for _, rule := range rules {
				trimmedRule := strings.TrimSpace(rule)
				if trimmedRule == "required" {
					if strVal == "" {
						return fmt.Errorf("value for %s is required", key)
					}
				}
			}
			if err := validateStringSettingValue(key, strVal); err != nil {
				return err
			}
		case reflect.Bool:
			_, ok := value.(bool)
			if !ok {
				return fmt.Errorf("invalid type for %s: expected boolean, got %T", key, value)
			}
		default:
			// Do not validate other types for group overrides
		}
	}

	return nil
}

func integerConfigValue(key string, value any) (int64, error) {
	floatVal, ok := value.(float64)
	if !ok {
		switch numVal := value.(type) {
		case int:
			return int64(numVal), nil
		case int64:
			return numVal, nil
		case int32:
			return int64(numVal), nil
		default:
			return 0, fmt.Errorf("invalid type for %s: expected a number, got %T", key, value)
		}
	}
	intVal := int64(floatVal)
	if floatVal != float64(intVal) {
		return 0, fmt.Errorf("invalid value for %s: must be an integer", key)
	}
	return intVal, nil
}

// DisplaySystemConfig displays the current system settings.
func (sm *SystemSettingsManager) DisplaySystemConfig(settings types.SystemSettings) {
	logrus.Info("")
	logrus.Info("========= System Settings =========")
	logrus.Info("  --- Basic Settings ---")
	logrus.Infof("    App URL: %s", settings.AppUrl)
	logrus.Infof("    Request Log Retention: %d days", settings.RequestLogRetentionDays)
	logrus.Infof("    Request Log Write Interval: %d minutes", settings.RequestLogWriteIntervalMinutes)

	logrus.Info("  --- Request Behavior ---")
	logrus.Infof("    Request Timeout: %d seconds", settings.RequestTimeout)
	logrus.Infof("    Stream First Byte Timeout: %d seconds", settings.StreamFirstByteTimeout)
	logrus.Infof("    Connect Timeout: %d seconds", settings.ConnectTimeout)
	logrus.Infof("    Response Header Timeout: %d seconds", settings.ResponseHeaderTimeout)
	logrus.Infof("    Idle Connection Timeout: %d seconds", settings.IdleConnTimeout)
	logrus.Infof("    Max Idle Connections: %d", settings.MaxIdleConns)
	logrus.Infof("    Max Idle Connections Per Host: %d", settings.MaxIdleConnsPerHost)

	logrus.Info("  --- Key & Group Behavior ---")
	logrus.Infof("    Max Retries: %d", settings.MaxRetries)
	logrus.Infof("    Retry Delay: %d ms", settings.RetryDelayMs)
	logrus.Infof("    Retry Backoff Enabled: %t", settings.RetryBackoffEnabled)
	logrus.Infof("    Retry Backoff Max Percent: %d", settings.RetryBackoffMaxPercent)
	logrus.Infof("    Blacklist Threshold: %d", settings.BlacklistThreshold)
	logrus.Infof("    Failover Status Codes: %s", settings.FailoverStatusCodes)
	logrus.Infof("    Key Validation Interval: %d minutes", settings.KeyValidationIntervalMinutes)
	logrus.Info("====================================")
	logrus.Info("")
}
