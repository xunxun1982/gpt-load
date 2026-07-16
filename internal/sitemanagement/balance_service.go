package sitemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
)

// BalanceInfo represents the balance information for a site
type BalanceInfo struct {
	SiteID  uint    `json:"site_id"`
	Balance *string `json:"balance"` // nil means no authoritative refresh value; an empty string is authoritative
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
		Balance *float64 `json:"balance"`
	} `json:"data"`
	Balance *float64 `json:"balance"`
}

// BalanceService handles fetching balance information from managed sites
type BalanceService struct {
	db               *gorm.DB
	encryptionSvc    encryption.Service
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
	})
	if s.subConfig != nil {
		_ = s.subConfig.Close()
	}

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

// updateBalancesInDB updates balance cache in database
func (s *BalanceService) updateBalancesInDB(ctx context.Context, results map[uint]*BalanceInfo) {
	today := GetBeijingCheckinDay()
	updated := false

	for siteID, info := range results {
		if info.Balance == nil {
			continue
		}
		balance := *info.Balance

		// Update only balance fields to avoid touching other columns
		if err := s.db.WithContext(ctx).Model(&ManagedSite{}).
			Where("id = ?", siteID).
			Updates(map[string]interface{}{
				"last_balance":      balance,
				"last_balance_date": today,
			}).Error; err != nil {
			logrus.WithError(err).WithField("site_id", siteID).
				Debug("Failed to update balance cache")
			continue
		}
		updated = true
	}

	if updated {
		s.invalidateBalanceConsumers()
	}
}

// FetchSiteBalance fetches balance for a single site and updates cache in database.
// This is the public API for single-site balance fetch (e.g., clicking balance cell).
func (s *BalanceService) FetchSiteBalance(ctx context.Context, site *ManagedSite) *BalanceInfo {
	result := s.fetchSiteBalanceInternal(ctx, site)

	if result.Balance != nil {
		s.updateSiteBalance(ctx, site.ID, *result.Balance)
	}

	return result
}

// fetchSiteBalanceInternal fetches balance for a single site without updating database.
// Used internally by batch operations to avoid duplicate DB updates.
func (s *BalanceService) fetchSiteBalanceInternal(ctx context.Context, site *ManagedSite) *BalanceInfo {
	result := &BalanceInfo{SiteID: site.ID}

	// Check if site type supports balance fetching
	if !s.supportsBalance(site.SiteType) {
		return result
	}

	// Check if site has auth configured
	if strings.TrimSpace(site.AuthValue) == "" {
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

// updateSiteBalance updates a single site's balance cache
func (s *BalanceService) updateSiteBalance(ctx context.Context, siteID uint, balance string) {
	today := GetBeijingCheckinDay()
	if err := s.db.WithContext(ctx).Model(&ManagedSite{}).
		Where("id = ?", siteID).
		Updates(map[string]interface{}{
			"last_balance":      balance,
			"last_balance_date": today,
		}).Error; err != nil {
		logrus.WithError(err).WithField("site_id", siteID).
			Debug("Failed to update site balance cache")
		return
	}
	s.invalidateBalanceConsumers()
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

	for _, authType := range []string{AuthTypeAccessToken, AuthTypeCookie} {
		if !authConfig.HasAuthType(authType) {
			continue
		}
		authValue := authConfig.GetAuthValue(authType)
		if authValue == "" {
			continue
		}
		headers := buildBalanceHeaders(authType, authValue, userID, cookieSession)
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

	if resp.Success != nil && !*resp.Success {
		return nil
	}
	if resp.Code != nil && *resp.Code != 0 {
		return nil
	}

	balance := resp.Data.Balance
	if balance == nil {
		balance = resp.Balance
	}
	if balance == nil {
		return nil
	}

	balanceStr := fmt.Sprintf("$%.2f", *balance)
	return &balanceStr
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
	s.updateBalancesInDB(ctx, results)

	return results, nil
}
