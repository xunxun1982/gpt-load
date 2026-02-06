package sitemanagement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// balanceRequestTimeout is the timeout for balance fetch requests
	balanceRequestTimeout = 10 * time.Second
	// balanceRefreshHour is the hour (Beijing time) when balances are auto-refreshed
	balanceRefreshHour = 5
)

// BalanceInfo represents the balance information for a site
type BalanceInfo struct {
	SiteID  uint    `json:"site_id"`
	Balance *string `json:"balance"` // nil means not available, string for display (may be negative)
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

// BalanceService handles fetching balance information from managed sites
type BalanceService struct {
	db               *gorm.DB
	encryptionSvc    encryption.Service
	client           *http.Client
	stealthClientMgr *StealthClientManager
	proxyClients     sync.Map // Cache for proxy-enabled HTTP clients

	// Background refresh control
	stopCh    chan struct{}
	cleanupCh chan struct{} // Channel for periodic cleanup ticker
	wg        sync.WaitGroup

	// Protect against double-close panics when Stop() is called multiple times
	stopOnce sync.Once
}

// NewBalanceService creates a new balance service
func NewBalanceService(db *gorm.DB, encryptionSvc encryption.Service) *BalanceService {
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
		cleanupCh:        make(chan struct{}),
	}
}

// Start begins the background balance refresh scheduler
func (s *BalanceService) Start() {
	s.wg.Add(1)
	go s.runScheduler()

	// Start periodic cleanup goroutine for aggressive memory release
	s.wg.Add(1)
	go s.periodicCleanup()

	logrus.Info("Balance refresh scheduler started")
}

// Stop gracefully stops the background scheduler
func (s *BalanceService) Stop(_ context.Context) {
	// Use sync.Once to prevent double-close panics if Stop() is called multiple times
	// This is important for error recovery scenarios where Stop() might be called repeatedly
	s.stopOnce.Do(func() {
		close(s.stopCh)
		close(s.cleanupCh) // Stop periodic cleanup goroutine
	})

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

	s.wg.Wait()
	logrus.Info("Balance refresh scheduler stopped")
}

// runScheduler runs the daily balance refresh at 05:00 Beijing time
func (s *BalanceService) runScheduler() {
	defer s.wg.Done()

	for {
		// Calculate next 05:00 Beijing time
		nextRefresh := s.nextRefreshTime()
		waitDuration := time.Until(nextRefresh)

		logrus.WithField("next_refresh", nextRefresh.Format(time.RFC3339)).
			Debug("Balance refresh scheduled")

		select {
		case <-s.stopCh:
			return
		case <-time.After(waitDuration):
			s.refreshAllBalancesBackground()
		}
	}
}

// nextRefreshTime calculates the next 05:00 Beijing time
func (s *BalanceService) nextRefreshTime() time.Time {
	now := time.Now().In(beijingLocation)
	target := time.Date(now.Year(), now.Month(), now.Day(),
		balanceRefreshHour, 0, 0, 0, beijingLocation)

	// If already past 05:00 today, schedule for tomorrow
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

// refreshAllBalancesBackground refreshes balances for all enabled sites in background
func (s *BalanceService) refreshAllBalancesBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	logrus.Info("Starting daily balance refresh")

	var sites []ManagedSite
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&sites).Error; err != nil {
		logrus.WithError(err).Error("Failed to load sites for balance refresh")
		return
	}

	results := s.FetchAllBalances(ctx, sites)
	s.updateBalancesInDB(ctx, results)

	// Close idle connections after batch refresh to free resources immediately
	s.closeIdleConnections()

	logrus.WithField("count", len(results)).Info("Daily balance refresh completed")
}

// updateBalancesInDB updates balance cache in database
func (s *BalanceService) updateBalancesInDB(ctx context.Context, results map[uint]*BalanceInfo) {
	today := GetBeijingCheckinDay()

	for siteID, info := range results {
		balance := ""
		if info.Balance != nil {
			balance = *info.Balance
		}

		// Update only balance fields to avoid touching other columns
		if err := s.db.WithContext(ctx).Model(&ManagedSite{}).
			Where("id = ?", siteID).
			Updates(map[string]interface{}{
				"last_balance":      balance,
				"last_balance_date": today,
			}).Error; err != nil {
			logrus.WithError(err).WithField("site_id", siteID).
				Debug("Failed to update balance cache")
		}
	}
}

// FetchSiteBalance fetches balance for a single site and updates cache in database.
// This is the public API for single-site balance fetch (e.g., clicking balance cell).
func (s *BalanceService) FetchSiteBalance(ctx context.Context, site *ManagedSite) *BalanceInfo {
	result := s.fetchSiteBalanceInternal(ctx, site)

	// Update database for single-site fetch
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

	// Decrypt user_id if present
	userID := ""
	if site.UserID != "" {
		if decrypted, err := s.encryptionSvc.Decrypt(site.UserID); err == nil {
			userID = decrypted
		}
	}

	// Fetch balance from site API
	balance := s.fetchBalanceFromAPI(ctx, site, authValue, userID)
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
	}
}

// supportsBalance checks if a site type supports balance fetching
func (s *BalanceService) supportsBalance(siteType string) bool {
	switch siteType {
	case SiteTypeNewAPI, SiteTypeVeloera, SiteTypeOneHub, SiteTypeDoneHub, SiteTypeWongGongyi:
		return true
	default:
		return false
	}
}

// fetchBalanceFromAPI fetches balance from the site's /api/user/self endpoint
func (s *BalanceService) fetchBalanceFromAPI(ctx context.Context, site *ManagedSite, authValue, userID string) *string {
	// Build API URL
	apiURL := extractBaseURL(site.BaseURL) + "/api/user/self"

	// Build headers
	headers := make(map[string]string)

	// Add user ID headers if available
	if userID != "" {
		for k, v := range buildUserHeaders(userID) {
			headers[k] = v
		}
	}

	// Set auth header based on auth type
	switch site.AuthType {
	case AuthTypeAccessToken:
		headers["Authorization"] = "Bearer " + authValue
	case AuthTypeCookie:
		headers["Cookie"] = authValue
	default:
		return nil
	}

	// Get appropriate HTTP client and make request
	client := s.getHTTPClient(site)
	var data []byte
	var err error

	// Reuse existing request functions from auto_checkin_service.go
	if shouldUseStealthRequest(*site) {
		data, _, err = doStealthJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
	} else {
		data, _, err = doJSONRequest(ctx, client, http.MethodGet, apiURL, headers, nil)
	}

	if err != nil {
		logrus.WithError(err).WithField("site_id", site.ID).Debug("Failed to fetch balance from site API")
		return nil
	}

	// Parse response
	return s.parseBalanceResponse(data)
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

// getHTTPClient returns appropriate HTTP client based on site settings.
// Uses stealth client for TLS fingerprint spoofing when bypass method is stealth.
// Uses proxy client when proxy is enabled. Clients are cached for connection reuse.
func (s *BalanceService) getHTTPClient(site *ManagedSite) *http.Client {
	// Use stealth client for TLS fingerprint spoofing
	if isStealthBypassMethod(site.BypassMethod) {
		proxyURL := ""
		if site.UseProxy {
			proxyURL = strings.TrimSpace(site.ProxyURL)
		}
		return s.stealthClientMgr.GetClient(proxyURL)
	}

	// Use default client if no proxy
	if !site.UseProxy || strings.TrimSpace(site.ProxyURL) == "" {
		return s.client
	}

	// Check proxy client cache
	proxyURL := strings.TrimSpace(site.ProxyURL)
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

// RefreshAllBalancesManual is called by the manual refresh button.
// It fetches balances for all enabled sites and updates the cache.
func (s *BalanceService) RefreshAllBalancesManual(ctx context.Context) (map[uint]*BalanceInfo, error) {
	var sites []ManagedSite
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&sites).Error; err != nil {
		return nil, err
	}

	results := s.FetchAllBalances(ctx, sites)
	s.updateBalancesInDB(ctx, results)

	// Close idle connections after manual refresh to free resources immediately
	s.closeIdleConnections()

	return results, nil
}
