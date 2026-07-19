package sitemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"

	"gorm.io/gorm"
)

const (
	authFieldTokenExpiresAt     = "token_expires_at"
	sub2APIAuthLockStripeCount  = 64
	sub2APITokenRefreshLeadTime = 2 * time.Minute
)

// sub2APIAuthManager coordinates rotating credentials for one managed site.
// Fixed lock stripes keep memory bounded while allowing unrelated sites to run concurrently.
// ponytail: 64 stripes bound memory; ID collisions can serialize network calls, so revisit only with throughput evidence.
type sub2APIAuthManager struct {
	db            *gorm.DB
	encryptionSvc encryption.Service
	locks         [sub2APIAuthLockStripeCount]sync.Mutex
}

type sub2APIAuthState struct {
	Site             ManagedSite
	Config           AuthConfig
	AccessToken      string
	RefreshToken     string
	RefreshAttempted bool
}

func newSub2APIAuthManager(db *gorm.DB, encryptionSvc encryption.Service) *sub2APIAuthManager {
	return &sub2APIAuthManager{db: db, encryptionSvc: encryptionSvc}
}

func (m *sub2APIAuthManager) lockSite(siteID uint) func() {
	lock := &m.locks[siteID%sub2APIAuthLockStripeCount]
	lock.Lock()
	return lock.Unlock
}

func (m *sub2APIAuthManager) loadLatestSite(ctx context.Context, snapshot ManagedSite) (ManagedSite, error) {
	if snapshot.ID == 0 {
		return snapshot, nil
	}
	var latest ManagedSite
	if err := m.db.WithContext(ctx).First(&latest, snapshot.ID).Error; err != nil {
		return ManagedSite{}, err
	}
	return latest, nil
}

func (m *sub2APIAuthManager) loadState(ctx context.Context, snapshot ManagedSite) (sub2APIAuthState, error) {
	latest, err := m.loadLatestSite(ctx, snapshot)
	if err != nil {
		return sub2APIAuthState{}, err
	}
	decrypted, err := m.encryptionSvc.Decrypt(latest.AuthValue)
	if err != nil {
		return sub2APIAuthState{}, err
	}
	config := parseAuthConfig(latest.AuthType, decrypted)
	return sub2APIAuthState{
		Site:         latest,
		Config:       config,
		AccessToken:  strings.TrimSpace(config.GetAuthValue(AuthTypeAccessToken)),
		RefreshToken: strings.TrimSpace(config.GetSupplementalValue(authFieldRefreshToken)),
	}, nil
}

func (s *sub2APIAuthState) needsRefresh(now time.Time) bool {
	if expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(s.Config.GetSupplementalValue(authFieldTokenExpiresAt))); err == nil {
		return !expiresAt.After(now.Add(sub2APITokenRefreshLeadTime))
	}
	return sub2APIAccessTokenNeedsRefresh(s.AccessToken, now)
}

func (m *sub2APIAuthManager) refresh(
	ctx context.Context,
	state *sub2APIAuthState,
	client *http.Client,
	useStealth bool,
) error {
	if state.RefreshToken == "" {
		return errors.New("Sub2API refresh token missing")
	}

	refreshed, err := (sub2APIProvider{}).refreshTokens(
		ctx,
		client,
		state.Site,
		state.AccessToken,
		state.RefreshToken,
		useStealth,
		state.Config.GetAuthValue(AuthTypeCookie),
	)
	if err != nil {
		if adopted, loadErr := m.adoptLatestAuthIfChanged(ctx, state); loadErr == nil && adopted {
			return nil
		}
		return err
	}

	updates := sub2APIAuthUpdates(refreshed)
	persistedAuthValue, err := persistManagedSiteAuthUpdates(ctx, m.db, m.encryptionSvc, state.Site, state.Config, updates)
	if err != nil {
		if errors.Is(err, errManagedSiteAuthChangedDuringCheckin) {
			adopted, loadErr := m.adoptLatestAuthIfChanged(ctx, state)
			if loadErr != nil {
				return loadErr
			}
			if adopted {
				return nil
			}
		}
		return err
	}

	state.AccessToken = refreshed.AccessToken
	state.RefreshToken = refreshed.RefreshToken
	state.Site.AuthValue = persistedAuthValue
	state.Config.AuthValues[AuthTypeAccessToken] = refreshed.AccessToken
	state.Config.SupplementalValues[authFieldRefreshToken] = refreshed.RefreshToken
	if !refreshed.TokenExpiresAt.IsZero() {
		state.Config.SupplementalValues[authFieldTokenExpiresAt] = refreshed.TokenExpiresAt.Format(time.RFC3339)
	}
	// Mark only a successful refresh (or an adopted rotation) so a transient
	// proactive failure can still be recovered by the first 401 response.
	state.RefreshAttempted = true
	return nil
}

func (m *sub2APIAuthManager) adoptLatestAuthIfChanged(ctx context.Context, state *sub2APIAuthState) (bool, error) {
	latest, err := m.loadState(ctx, state.Site)
	if err != nil {
		return false, err
	}
	if latest.Site.AuthType == state.Site.AuthType && latest.Site.AuthValue == state.Site.AuthValue {
		return false, nil
	}
	latest.RefreshAttempted = true
	*state = latest
	return true, nil
}

func sub2APIAuthUpdates(refreshed sub2APIRefreshResult) map[string]string {
	updates := map[string]string{
		AuthTypeAccessToken:   refreshed.AccessToken,
		authFieldRefreshToken: refreshed.RefreshToken,
	}
	if !refreshed.TokenExpiresAt.IsZero() {
		updates[authFieldTokenExpiresAt] = refreshed.TokenExpiresAt.Format(time.RFC3339)
	}
	return updates
}

func persistManagedSiteAuthUpdates(
	ctx context.Context,
	db *gorm.DB,
	encryptionSvc encryption.Service,
	site ManagedSite,
	authConfig AuthConfig,
	updates map[string]string,
) (string, error) {
	values := make(map[string]string, len(authConfig.AuthValues)+len(authConfig.SupplementalValues)+len(updates))
	for k, v := range authConfig.AuthValues {
		if strings.TrimSpace(v) != "" {
			values[k] = v
		}
	}
	for k, v := range authConfig.SupplementalValues {
		if k != authFieldAuthToken && strings.TrimSpace(v) != "" {
			values[k] = v
		}
	}
	for k, v := range updates {
		if strings.TrimSpace(v) != "" {
			values[k] = v
		}
	}
	if len(values) == 0 {
		return site.AuthValue, nil
	}

	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	encrypted, err := encryptionSvc.Encrypt(string(data))
	if err != nil {
		return "", err
	}
	result := db.WithContext(ctx).
		Model(&ManagedSite{}).
		Where("id = ? AND auth_type = ? AND auth_value = ?", site.ID, site.AuthType, site.AuthValue).
		Update("auth_value", encrypted)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", errManagedSiteAuthChangedDuringCheckin
	}
	return encrypted, nil
}
