package sitemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// balanceRequestTimeout is the timeout for balance fetch requests
	balanceRequestTimeout = 10 * time.Second
	// balancePersistenceTimeout bounds best-effort writes after fetch cancellation.
	balancePersistenceTimeout = 5 * time.Second
)

// BalanceInfo represents the balance information for a site
type BalanceInfo struct {
	SiteID  uint    `json:"site_id"`
	Balance *string `json:"balance"` // nil means no authoritative refresh value; an empty string is authoritative

	// sourceSnapshot identifies the site configuration used for the request.
	// It is kept private so API responses stay unchanged while cache writes can
	// reject responses belonging to a replaced account configuration.
	sourceSnapshot    balanceSourceSnapshot
	hasSourceSnapshot bool
	sourceMultiplier  int64
}

type balanceSourceSnapshot struct {
	baseURL      string
	siteType     string
	userID       string
	authType     string
	authValue    string
	useProxy     bool
	proxyURL     string
	bypassMethod string
}

func (s *balanceSourceSnapshot) set(site ManagedSite) {
	s.baseURL = site.BaseURL
	s.siteType = site.SiteType
	s.userID = site.UserID
	s.authType = site.AuthType
	s.authValue = site.AuthValue
	s.useProxy = site.UseProxy
	s.proxyURL = site.ProxyURL
	s.bypassMethod = site.BypassMethod
}

// userSelfResponse represents the /api/user/self API response structure
type userSelfResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Quota int64 `json:"quota"` // Balance in internal units (divide by 500000 for USD)
	} `json:"data"`
	// Some sites return quota directly without data wrapper
	Quota int64 `json:"quota"`
}

type sub2APIProfileResponse struct {
	Success *bool `json:"success"`
	Code    *int  `json:"code"`
	Data    struct {
		Balance json.RawMessage `json:"balance"`
	} `json:"data"`
	Balance json.RawMessage `json:"balance"`
}

// BalanceService handles fetching balance information from managed sites
type BalanceService struct {
	db               *gorm.DB
	encryptionSvc    encryption.Service
	sub2APIAuth      *sub2APIAuthManager
	client           *http.Client
	stealthClientMgr *StealthClientManager
	proxyClients     sync.Map // Cache for proxy-enabled HTTP clients
	proxyResolver    managedSiteProxyURLResolver
	cacheInvalidator func()
	store            store.Store
	subConfig        store.Subscription

	// Background refresh control
	stopCh          chan struct{}
	rescheduleCh    chan struct{}
	cleanupCh       chan struct{} // Channel for periodic cleanup ticker
	lifecycleCtx    context.Context
	cancelLifecycle context.CancelFunc
	wg              sync.WaitGroup

	// Protect against double-close panics when Stop() is called multiple times
	stopOnce sync.Once
}

// NewBalanceService creates a new balance service
func NewBalanceService(db *gorm.DB, encryptionSvc encryption.Service) *BalanceService {
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	transport := &http.Transport{
		MaxIdleConns:        50,              // Reduced for aggressive memory release (site management is non-critical)
		MaxIdleConnsPerHost: 10,              // Reduced for aggressive memory release
		IdleConnTimeout:     5 * time.Second, // Aggressive timeout for faster resource cleanup
	}

	return &BalanceService{
		db:               db,
		encryptionSvc:    encryptionSvc,
		sub2APIAuth:      newSub2APIAuthManager(db, encryptionSvc),
		client:           &http.Client{Transport: transport, Timeout: balanceRequestTimeout},
		stealthClientMgr: NewStealthClientManager(balanceRequestTimeout),
		stopCh:           make(chan struct{}),
		rescheduleCh:     make(chan struct{}, 1),
		cleanupCh:        make(chan struct{}),
		lifecycleCtx:     lifecycleCtx,
		cancelLifecycle:  cancel,
	}
}

func (s *BalanceService) SetProxyURLResolver(resolver managedSiteProxyURLResolver) {
	s.proxyResolver = resolver
}

func (s *BalanceService) SetStore(store store.Store) {
	s.store = store
}

// SetCacheInvalidationCallback registers a callback for cached balance consumers.
func (s *BalanceService) SetCacheInvalidationCallback(callback func()) {
	s.cacheInvalidator = callback
}

func (s *BalanceService) invalidateBalanceConsumers() {
	if s.cacheInvalidator != nil {
		s.cacheInvalidator()
	}
}

// Start begins the background balance refresh scheduler
func (s *BalanceService) Start() {
	s.wg.Add(1)
	go s.runScheduler()

	// Start periodic cleanup goroutine for aggressive memory release
	s.wg.Add(1)
	go s.periodicCleanup()

	if s.store != nil {
		if sub, err := s.store.Subscribe(siteScheduleConfigUpdatedChannel); err == nil {
			s.subConfig = sub
			s.wg.Add(1)
			go s.listenScheduleConfigUpdates(sub)
		}
	}
	// Reconcile any configuration change that raced with scheduler startup.
	s.RequestReschedule()

	logrus.Info("Balance refresh scheduler started")
}

// Stop gracefully stops the background scheduler.
func (s *BalanceService) Stop(ctx context.Context) {
	// Use sync.Once to prevent double-close panics if Stop() is called multiple times
	// This is important for error recovery scenarios where Stop() might be called repeatedly
	s.stopOnce.Do(func() {
		s.cancelLifecycle()
		close(s.stopCh)
		close(s.cleanupCh) // Stop periodic cleanup goroutine
		// Subscription cleanup belongs to the one-time shutdown contract.
		if s.subConfig != nil {
			_ = s.subConfig.Close()
		}
	})

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	stopped := false
	select {
	case <-done:
		stopped = true
	default:
		select {
		case <-done:
			stopped = true
		case <-ctx.Done():
			logrus.WithError(ctx.Err()).Warn("Balance refresh scheduler stop timed out")
		}
	}

	// Clean up proxy client cache
	s.proxyClients.Range(func(key, value interface{}) bool {
		if client, ok := value.(*http.Client); ok {
			if transport, ok := client.Transport.(*http.Transport); ok {
				transport.CloseIdleConnections()
			}
		}
		s.proxyClients.Delete(key)
		return true
	})

	// Clean up stealth client cache
	s.stealthClientMgr.Cleanup()

	if stopped {
		logrus.Info("Balance refresh scheduler stopped")
	}
}

// RequestReschedule asks the scheduler to reload its persisted configuration.
func (s *BalanceService) RequestReschedule() {
	select {
	case s.rescheduleCh <- struct{}{}:
	default:
	}
}

func (s *BalanceService) listenScheduleConfigUpdates(sub store.Subscription) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case _, ok := <-sub.Channel():
			if !ok {
				return
			}
			s.RequestReschedule()
		}
	}
}

// runScheduler runs balance refreshes at configured local-time slots.
func (s *BalanceService) runScheduler() {
	defer s.wg.Done()

	for {
		nextRefresh, enabled, err := s.nextRefreshTime(s.lifecycleCtx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			logrus.WithError(err).Warn("Failed to load automatic balance schedule")
			nextRefresh = time.Now().Add(5 * time.Minute)
			enabled = false
		}
		waitDuration := time.Until(nextRefresh)
		if waitDuration < 0 {
			waitDuration = 0
		}

		if enabled {
			logrus.WithField("next_refresh", nextRefresh.Format(time.RFC3339)).
				Debug("Balance refresh scheduled")
		}
		timer := time.NewTimer(waitDuration)

		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-s.rescheduleCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			if enabled {
				s.refreshAllBalancesBackground(s.lifecycleCtx)
			}
		}
	}
}

func (s *BalanceService) nextRefreshTime(ctx context.Context) (time.Time, bool, error) {
	config := AutoBalanceConfig{GlobalEnabled: true, IntervalHours: defaultAutoBalanceIntervalHours}
	var setting ManagedSiteSetting
	if err := s.db.WithContext(ctx).First(&setting, 1).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return time.Time{}, false, err
		}
	} else {
		config.GlobalEnabled = setting.AutoBalanceEnabled
		config.IntervalHours = setting.BalanceRefreshIntervalHours
	}
	config.IntervalHours = normalizeAutoBalanceIntervalHours(config.IntervalHours)
	return nextBalanceRefreshTimeAt(time.Now(), config.IntervalHours, checkinLocation()), config.GlobalEnabled, nil
}

func nextBalanceRefreshTimeAt(base time.Time, intervalHours int, loc *time.Location) time.Time {
	intervalHours = normalizeAutoBalanceIntervalHours(intervalHours)
	localBase := base.In(loc)
	for hour := intervalHours; hour < 24; hour += intervalHours {
		candidate := time.Date(localBase.Year(), localBase.Month(), localBase.Day(), hour, 0, 0, 0, loc)
		if candidate.After(localBase) {
			return candidate
		}
	}
	nextDay := localBase.AddDate(0, 0, 1)
	return time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), 0, 0, 0, 0, loc)
}

// refreshAllBalancesBackground refreshes balances for all enabled sites in background
func (s *BalanceService) refreshAllBalancesBackground(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()

	logrus.Info("Starting scheduled balance refresh")

	results, err := s.RefreshAllBalances(ctx)
	if err != nil {
		logrus.WithError(err).Error("Scheduled balance refresh failed")
		return
	}
	logrus.WithField("count", len(results)).Info("Scheduled balance refresh completed")
}

// updateBalancesInDB updates balance cache in database and aggregates partial-write failures.
func (s *BalanceService) updateBalancesInDB(ctx context.Context, results map[uint]*BalanceInfo) error {
	today := GetBeijingCheckinDay()
	updated := false
	var firstUpdateErr error
	failedUpdates := 0

	for siteID, info := range results {
		if ctx.Err() != nil {
			break
		}
		if info.Balance == nil {
			continue
		}
		balance := *info.Balance

		// Update only balance fields, and only if the response still belongs to
		// the same request-affecting site configuration.
		updateResult := s.balanceSourceQuery(ctx, siteID, info).
			Updates(map[string]interface{}{
				"last_balance":      balance,
				"last_balance_date": today,
			})
		if err := updateResult.Error; err != nil {
			if ctx.Err() != nil {
				updated = updated || updateResult.RowsAffected > 0
				break
			}
			logrus.WithError(err).WithField("site_id", siteID).
				Debug("Failed to update balance cache")
			failedUpdates++
			if firstUpdateErr == nil {
				firstUpdateErr = fmt.Errorf("failed to update balance cache for site %d: %w", siteID, err)
			}
			continue
		}
		if updateResult.RowsAffected == 0 && info.hasSourceSnapshot {
			matches, matchErr := s.balanceSourceStillMatches(ctx, siteID, info)
			if matchErr != nil {
				failedUpdates++
				if firstUpdateErr == nil {
					firstUpdateErr = fmt.Errorf("failed to verify balance source for site %d: %w", siteID, matchErr)
				}
				continue
			}
			if !matches {
				info.Balance = nil
				continue
			}
		}
		updated = updated || updateResult.RowsAffected > 0
	}

	if updated {
		// Invalidators expose no error channel; partial successful writes still invalidate once.
		s.invalidateBalanceConsumers()
	}
	if failedUpdates > 1 {
		return fmt.Errorf("%w; %d additional balance cache updates failed", firstUpdateErr, failedUpdates-1)
	}
	return firstUpdateErr
}

// FetchSiteBalance fetches balance for a single site and updates cache in database.
// This is the public API for single-site balance fetch (e.g., clicking balance cell).
func (s *BalanceService) FetchSiteBalance(ctx context.Context, site *ManagedSite) *BalanceInfo {
	result := s.fetchSiteBalanceInternal(ctx, site)

	if result.Balance != nil {
		if !s.updateSiteBalance(ctx, result) {
			// Do not expose a response that no longer belongs to the saved site.
			result.Balance = nil
		} else {
			multiplier := result.sourceMultiplier
			if multiplier < 1 {
				multiplier = site.BalanceMultiplier
			}
			scaleManagedSiteBalanceInfo(result, multiplier)
		}
	}

	return result
}

// fetchSiteBalanceInternal fetches balance for a single site without updating database.
// Used internally by batch operations to avoid duplicate DB updates.
func (s *BalanceService) fetchSiteBalanceInternal(ctx context.Context, site *ManagedSite) *BalanceInfo {
	result := &BalanceInfo{SiteID: site.ID}
	result.sourceSnapshot.set(*site)
	result.hasSourceSnapshot = true
	result.sourceMultiplier = site.BalanceMultiplier

	// Check if site type supports balance fetching
	if !s.supportsBalance(site.SiteType) {
		return result
	}

	// Check if site has auth configured
	if strings.TrimSpace(site.AuthValue) == "" {
		return result
	}
	if site.SiteType == SiteTypeSub2API {
		balance, effectiveSite := s.fetchSub2APIBalance(ctx, *site)
		result.Balance = balance
		result.sourceSnapshot.set(effectiveSite)
		result.sourceMultiplier = effectiveSite.BalanceMultiplier
		return result
	}

	// Decrypt auth value
	authValue, err := s.encryptionSvc.Decrypt(site.AuthValue)
	if err != nil {
		logrus.WithError(err).WithField("site_id", site.ID).Debug("Failed to decrypt auth value for balance fetch")
		return result
	}
	authConfig := parseAuthConfig(site.AuthType, authValue)

	// Decrypt user_id if present
	userID := ""
	if site.UserID != "" {
		if decrypted, err := s.encryptionSvc.Decrypt(site.UserID); err == nil {
			userID = decrypted
		}
	}

	// Fetch balance from site API
	balance := s.fetchBalanceFromAPI(ctx, site, authConfig, userID)
	if balance != nil {
		result.Balance = balance
	}

	return result
}

func (s *BalanceService) fetchSub2APIBalance(ctx context.Context, snapshot ManagedSite) (*string, ManagedSite) {
	unlock := s.sub2APIAuth.lockSite(snapshot.ID)
	defer unlock()

	state, err := s.sub2APIAuth.loadState(ctx, snapshot)
	if err != nil {
		logrus.WithError(err).WithField("site_id", snapshot.ID).
			Debug("Failed to load Sub2API auth for balance fetch")
		return nil, snapshot
	}
	if state.Site.SiteType != SiteTypeSub2API {
		return nil, state.Site
	}
	if state.AccessToken == "" && state.RefreshToken == "" && strings.TrimSpace(state.Config.GetAuthValue(AuthTypeCookie)) == "" {
		return nil, state.Site
	}

	client := s.getHTTPClient(ctx, &state.Site)
	useStealth := shouldUseStealthRequest(state.Site)
	if state.RefreshToken != "" && (state.AccessToken == "" || state.needsRefresh(time.Now())) {
		if err := s.sub2APIAuth.refresh(ctx, &state, client, useStealth); err != nil {
			logrus.WithError(err).WithField("site_id", state.Site.ID).
				Warn("Sub2API token proactive balance refresh failed")
		} else {
			client = s.getHTTPClient(ctx, &state.Site)
			useStealth = shouldUseStealthRequest(state.Site)
		}
	}

	balance, endpoint, bareTokenUnauthorized := s.fetchSub2APIBalanceEndpoints(ctx, client, &state, useStealth)
	if endpoint == "" || !bareTokenUnauthorized || state.RefreshAttempted || state.RefreshToken == "" {
		return balance, state.Site
	}
	if err := s.sub2APIAuth.refresh(ctx, &state, client, useStealth); err != nil {
		logrus.WithError(err).WithField("site_id", state.Site.ID).
			Warn("Sub2API token reactive balance refresh failed")
		return balance, state.Site
	}
	client = s.getHTTPClient(ctx, &state.Site)
	useStealth = shouldUseStealthRequest(state.Site)

	if refreshedBalance := s.fetchSub2APIBalanceWithAccessToken(ctx, client, &state, endpoint, useStealth); refreshedBalance != nil {
		return refreshedBalance, state.Site
	}
	return balance, state.Site
}

func (s *BalanceService) fetchSub2APIBalanceEndpoints(
	ctx context.Context,
	client *http.Client,
	state *sub2APIAuthState,
	useStealth bool,
) (*string, string, bool) {
	// /api/v1/auth/me is the Sub2API standard. Keep the former profile path
	// only as a missing-endpoint compatibility fallback for legacy deployments.
	endpoints := []string{resolveSiteCapabilities(SiteTypeSub2API).BalanceEndpoint, "/api/v1/user/profile"}
	for _, endpoint := range endpoints {
		balance, missingEndpoint, bareTokenUnauthorized := s.fetchSub2APIBalanceEndpoint(ctx, client, state, endpoint, useStealth)
		if missingEndpoint {
			continue
		}
		return balance, endpoint, bareTokenUnauthorized
	}
	return nil, "", false
}

func (s *BalanceService) fetchSub2APIBalanceEndpoint(
	ctx context.Context,
	client *http.Client,
	state *sub2APIAuthState,
	endpoint string,
	useStealth bool,
) (*string, bool, bool) {
	cookie := strings.TrimSpace(state.Config.GetAuthValue(AuthTypeCookie))
	type authAttempt struct {
		authType string
		value    string
		cookie   string
	}
	attempts := make([]authAttempt, 0, 3)
	if state.AccessToken != "" {
		if cookie != "" {
			attempts = append(attempts, authAttempt{authType: AuthTypeAccessToken, value: state.AccessToken, cookie: cookie})
		}
		attempts = append(attempts, authAttempt{authType: AuthTypeAccessToken, value: state.AccessToken})
	}
	if cookie != "" {
		attempts = append(attempts, authAttempt{authType: AuthTypeCookie, value: cookie})
	}

	bareTokenUnauthorized := false
	challengeDetected := false
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return nil, false, bareTokenUnauthorized
		}
		data, statusCode, err := s.requestSub2APIBalance(
			ctx,
			client,
			state.Site,
			endpoint,
			buildBalanceHeaders(attempt.authType, attempt.value, "", attempt.cookie),
			useStealth,
		)
		if isBrowserChallengeResponse(statusCode, data) {
			logrus.WithField("site_id", state.Site.ID).
				Debug("Sub2API balance request returned a browser challenge")
			challengeDetected = true
			continue
		}
		if sub2APIMissingEndpointStatus(statusCode) {
			if challengeDetected {
				return nil, false, bareTokenUnauthorized
			}
			return nil, true, bareTokenUnauthorized
		}
		if attempt.authType == AuthTypeAccessToken && attempt.cookie == "" && statusCode == http.StatusUnauthorized {
			bareTokenUnauthorized = true
		}
		if err != nil {
			logrus.WithError(err).WithField("site_id", state.Site.ID).
				Debug("Failed to fetch Sub2API balance")
			continue
		}
		if balance := s.parseSub2APIBalanceResponse(data); balance != nil {
			return balance, false, bareTokenUnauthorized
		}
	}
	return nil, false, bareTokenUnauthorized
}

func (s *BalanceService) fetchSub2APIBalanceWithAccessToken(
	ctx context.Context,
	client *http.Client,
	state *sub2APIAuthState,
	endpoint string,
	useStealth bool,
) *string {
	balance, _, _ := s.fetchSub2APIBalanceEndpoint(ctx, client, state, endpoint, useStealth)
	if balance == nil {
		logrus.WithField("site_id", state.Site.ID).
			Debug("Failed to retry Sub2API balance after token refresh")
	}
	return balance
}

func (s *BalanceService) requestSub2APIBalance(
	ctx context.Context,
	client *http.Client,
	site ManagedSite,
	endpoint string,
	headers map[string]string,
	useStealth bool,
) ([]byte, int, error) {
	apiURL := extractBaseURL(site.BaseURL) + endpoint
	if useStealth {
		return doStealthJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
	}
	return doJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
}

func (s *BalanceService) balanceSourceQuery(ctx context.Context, siteID uint, info *BalanceInfo) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&ManagedSite{}).Where("id = ?", siteID)
	if info == nil || !info.hasSourceSnapshot {
		return query
	}
	snapshot := info.sourceSnapshot
	return query.Where(
		"base_url = ? AND site_type = ? AND user_id = ? AND auth_type = ? AND auth_value = ? AND use_proxy = ? AND proxy_url = ? AND bypass_method = ?",
		snapshot.baseURL,
		snapshot.siteType,
		snapshot.userID,
		snapshot.authType,
		snapshot.authValue,
		snapshot.useProxy,
		snapshot.proxyURL,
		snapshot.bypassMethod,
	)
}

func (s *BalanceService) balanceSourceStillMatches(ctx context.Context, siteID uint, info *BalanceInfo) (bool, error) {
	var count int64
	if err := s.balanceSourceQuery(ctx, siteID, info).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// updateSiteBalance updates a single site's balance cache if its request source is still current.
func (s *BalanceService) updateSiteBalance(ctx context.Context, info *BalanceInfo) bool {
	if info == nil || info.Balance == nil {
		return false
	}
	today := GetBeijingCheckinDay()
	result := s.balanceSourceQuery(ctx, info.SiteID, info).
		Updates(map[string]interface{}{
			"last_balance":      *info.Balance,
			"last_balance_date": today,
		})
	if result.Error != nil {
		logrus.WithError(result.Error).WithField("site_id", info.SiteID).
			Debug("Failed to update site balance cache")
		return false
	}
	if result.RowsAffected == 0 && info.hasSourceSnapshot {
		matches, err := s.balanceSourceStillMatches(ctx, info.SiteID, info)
		if err != nil {
			logrus.WithError(err).WithField("site_id", info.SiteID).
				Debug("Failed to verify site balance source")
			return false
		}
		if !matches {
			return false
		}
	}
	if result.RowsAffected > 0 {
		s.invalidateBalanceConsumers()
	}
	return true
}

// supportsBalance checks if a site type supports balance fetching
func (s *BalanceService) supportsBalance(siteType string) bool {
	return resolveSiteCapabilities(siteType).SupportsBalance
}

// fetchBalanceFromAPI fetches balance from the site's provider-specific profile endpoint.
func (s *BalanceService) fetchBalanceFromAPI(ctx context.Context, site *ManagedSite, authConfig AuthConfig, userID string) *string {
	capabilities := resolveSiteCapabilities(site.SiteType)
	if !capabilities.SupportsBalance || capabilities.BalanceEndpoint == "" {
		return nil
	}
	switch capabilities.balanceParser {
	case balanceParserSub2API:
		return s.fetchBalanceWithParser(ctx, site, authConfig, userID, capabilities.BalanceEndpoint, s.parseSub2APIBalanceResponse)
	default:
		return s.fetchBalanceWithParser(ctx, site, authConfig, userID, capabilities.BalanceEndpoint, s.parseBalanceResponse)
	}
}

func (s *BalanceService) fetchBalanceWithParser(
	ctx context.Context,
	site *ManagedSite,
	authConfig AuthConfig,
	userID string,
	urlSuffix string,
	parse func([]byte) *string,
) *string {
	apiURL := extractBaseURL(site.BaseURL) + urlSuffix
	client := s.getHTTPClient(ctx, site)
	cookieSession := ""
	if authConfig.HasAuthType(AuthTypeCookie) {
		cookieSession = authConfig.GetAuthValue(AuthTypeCookie)
	}

	// Only configured, non-empty credentials create attempts; single-auth sites never send placeholder headers.
	for _, authType := range []string{AuthTypeAccessToken, AuthTypeCookie} {
		if !authConfig.HasAuthType(authType) {
			continue
		}
		authValue := authConfig.GetAuthValue(authType)
		if authValue == "" {
			continue
		}

		attempts := 1
		if authType == AuthTypeAccessToken && cookieSession != "" {
			// Keep the combined WAF-compatible request first, then isolate bearer auth so a bad cookie cannot mask a valid token.
			attempts = 2
		}
		for attempt := 0; attempt < attempts; attempt++ {
			// In-flight requests already use ctx; stop before constructing or logging any later fallback request.
			if ctx.Err() != nil {
				return nil
			}
			requestCookie := cookieSession
			if attempt > 0 {
				requestCookie = ""
			}
			headers := buildBalanceHeaders(authType, authValue, userID, requestCookie)
			if headers == nil {
				continue
			}

			var data []byte
			var err error
			if shouldUseStealthRequest(*site) {
				data, _, err = doStealthJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
			} else {
				data, _, err = doJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
			}
			if err != nil {
				logrus.WithError(err).WithField("site_id", site.ID).Debug("Failed to fetch balance from site API")
				continue
			}
			if balance := parse(data); balance != nil {
				return balance
			}
		}
	}

	return nil
}

func buildBalanceHeaders(authType, authValue, userID, cookieSession string) map[string]string {
	headers := make(map[string]string)
	if userID != "" {
		for k, v := range buildUserHeaders(userID) {
			headers[k] = v
		}
	}
	switch authType {
	case AuthTypeAccessToken:
		headers["Authorization"] = bearerAuthorizationValue(authValue)
		// Some WAF-protected sites require the browser session cookie alongside bearer auth.
		if cookieSession != "" {
			headers["Cookie"] = cookieSession
		}
	case AuthTypeCookie:
		headers["Cookie"] = authValue
	default:
		return nil
	}
	return headers
}

// parseBalanceResponse parses the API response and extracts balance
func (s *BalanceService) parseBalanceResponse(data []byte) *string {
	var resp userSelfResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil
	}

	// Treat unsuccessful responses as unavailable balance
	// This prevents reporting $0.00 for failed API calls
	if !resp.Success {
		return nil
	}

	// Try to get quota from data wrapper first, then from root.
	// Only fall back to root quota if Data.Quota is zero AND root Quota has a value.
	// This preserves legitimate $0.00 balances when Data.Quota is explicitly set to 0.
	quota := resp.Data.Quota
	if quota == 0 && resp.Quota != 0 {
		quota = resp.Quota
	}

	// Convert quota to USD (divide by 500000 as per new-api convention)
	// Note: quota can be negative (user owes money)
	balanceUSD := float64(quota) / 500000.0
	balanceStr := fmt.Sprintf("$%.2f", balanceUSD)

	return &balanceStr
}

func (s *BalanceService) parseSub2APIBalanceResponse(data []byte) *string {
	var resp sub2APIProfileResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil
	}

	if resp.Success == nil && resp.Code == nil {
		return nil
	}
	if resp.Success != nil && !*resp.Success {
		return nil
	}
	if resp.Code != nil && *resp.Code != 0 {
		return nil
	}

	balance, ok := parseSub2APIBalanceValue(resp.Data.Balance)
	if !ok {
		balance, ok = parseSub2APIBalanceValue(resp.Balance)
	}
	if !ok {
		return nil
	}

	balanceStr := fmt.Sprintf("$%.2f", balance)
	return &balanceStr
}

func parseSub2APIBalanceValue(raw json.RawMessage) (float64, bool) {
	var number *float64
	if err := json.Unmarshal(raw, &number); err == nil && number != nil && !math.IsNaN(*number) && !math.IsInf(*number, 0) {
		return *number, true
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

// getHTTPClient returns appropriate HTTP client based on site settings.
// Uses stealth client for TLS fingerprint spoofing when bypass method is stealth.
// Uses proxy client when proxy is enabled. Clients are cached for connection reuse.
func (s *BalanceService) getHTTPClient(ctx context.Context, site *ManagedSite) *http.Client {
	// Use stealth client for TLS fingerprint spoofing
	if isStealthBypassMethod(site.BypassMethod) {
		proxyURL := ""
		if site.UseProxy {
			proxyURL = resolveManagedSiteProxyURL(ctx, s.proxyResolver, site.ProxyURL)
		}
		return s.stealthClientMgr.GetClient(proxyURL)
	}

	// Use default client if no proxy
	if !site.UseProxy || strings.TrimSpace(site.ProxyURL) == "" {
		return s.client
	}

	// Check proxy client cache
	proxyURL := resolveManagedSiteProxyURL(ctx, s.proxyResolver, site.ProxyURL)
	if proxyURL == "" {
		return s.client
	}
	if cached, ok := s.proxyClients.Load(proxyURL); ok {
		return cached.(*http.Client)
	}

	// Parse and validate proxy URL
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		logrus.WithError(err).Warn("Invalid proxy URL, using direct connection")
		return s.client
	}

	// Create new proxy client
	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsedURL),
		MaxIdleConns:        50,              // Reduced for aggressive memory release (site management is non-critical)
		MaxIdleConnsPerHost: 10,              // Reduced for aggressive memory release
		IdleConnTimeout:     5 * time.Second, // Aggressive timeout for faster resource cleanup
	}

	client := &http.Client{Transport: transport, Timeout: balanceRequestTimeout}
	actual, _ := s.proxyClients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client)
}

// FetchAllBalances fetches balances for multiple sites concurrently.
// Uses worker pool pattern to limit concurrent requests and avoid overwhelming target sites.
// Note: This method does NOT update database; caller should call updateBalancesInDB separately.
func (s *BalanceService) FetchAllBalances(ctx context.Context, sites []ManagedSite) map[uint]*BalanceInfo {
	results := make(map[uint]*BalanceInfo)
	if len(sites) == 0 {
		return results
	}

	var mu sync.Mutex

	// Limit concurrency to avoid overwhelming target sites
	concurrency := 5
	if len(sites) < concurrency {
		concurrency = len(sites)
	}

	jobs := make(chan *ManagedSite, len(sites))
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for site := range jobs {
				// Use internal method to avoid duplicate DB updates
				info := s.fetchSiteBalanceInternal(ctx, site)
				mu.Lock()
				results[site.ID] = info
				mu.Unlock()
			}
		}()
	}

	// Send jobs
	for i := range sites {
		jobs <- &sites[i]
	}
	close(jobs)

	wg.Wait()
	return results
}

// closeIdleConnections closes idle connections for all HTTP clients to free resources.
// This should be called after batch operations (balance refresh) complete.
func (s *BalanceService) closeIdleConnections() {
	// Close idle connections for default client
	if transport, ok := s.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}

	// Close idle connections for all cached proxy clients
	s.proxyClients.Range(func(key, value interface{}) bool {
		if client, ok := value.(*http.Client); ok {
			if transport, ok := client.Transport.(*http.Transport); ok {
				transport.CloseIdleConnections()
			}
		}
		return true
	})

	// Close idle connections for stealth client manager
	s.stealthClientMgr.CloseIdleConnections()
}

// periodicCleanup runs periodic cleanup of idle connections for aggressive memory release.
// Site management is a non-critical feature, so we can be more aggressive with resource cleanup.
func (s *BalanceService) periodicCleanup() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Minute) // Cleanup every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-s.cleanupCh:
			return
		case <-ticker.C:
			s.closeIdleConnections()
			logrus.Debug("BalanceService: periodic cleanup completed")
		}
	}
}

// RefreshAllBalances is the shared automatic and manual refresh path.
func (s *BalanceService) RefreshAllBalances(ctx context.Context) (map[uint]*BalanceInfo, error) {
	defer s.closeIdleConnections()

	var sites []ManagedSite
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&sites).Error; err != nil {
		return nil, err
	}

	results := s.FetchAllBalances(ctx, sites)
	// Keep database writes raw; every return path exposes the same scaled response contract.
	defer scaleManagedSiteBalanceResults(results, sites)
	persistCtx := ctx
	var releasePersistCtx func()
	if ctx.Err() != nil {
		// Preserve balances that completed before the fetch deadline, while
		// keeping the post-timeout database work strictly bounded.
		persistCtx, releasePersistCtx = context.WithTimeout(context.WithoutCancel(ctx), balancePersistenceTimeout)
		defer releasePersistCtx()
	}
	updateErr := s.updateBalancesInDB(persistCtx, results)
	if err := ctx.Err(); err != nil {
		// Cancellation remains the primary caller contract even when a persistence attempt also failed.
		return results, err
	}
	if updateErr != nil {
		return results, updateErr
	}

	return results, nil
}
