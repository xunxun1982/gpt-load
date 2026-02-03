package sitemanagement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Task type constants - must match services.TaskType* constants
// Defined here to avoid circular dependency with services package
const (
	taskTypeKeyImport  = "KEY_IMPORT"
	taskTypeKeyDelete  = "KEY_DELETE"
	taskTypeKeyRestore = "KEY_RESTORE"
)

const (
	autoCheckinStatusKey     = "managed_site:auto_checkin_status"
	autoCheckinRunNowChannel = "managed_site:auto_checkin_run_now"
	// Note: autoCheckinConfigUpdatedChannel is defined in site_service.go (same package)

	maxResponseBodySize = 2 << 20 // 2 MB limit for HTTP response body
)

type AutoCheckinService struct {
	db            *gorm.DB
	store         store.Store
	encryptionSvc encryption.Service
	client        *http.Client

	// Proxy client cache to reuse HTTP clients with same proxy URL for connection pooling.
	// Key: normalized proxy URL, Value: *http.Client with proxy transport.
	// Using sync.Map for concurrent access without explicit locking.
	proxyClients sync.Map

	// Stealth client manager for TLS fingerprint spoofing to bypass Cloudflare
	stealthClientMgr *StealthClientManager

	stopCh       chan struct{}
	rescheduleCh chan struct{}
	runNowCh     chan struct{}
	cleanupCh    chan struct{} // Channel for periodic cleanup ticker

	wg sync.WaitGroup

	timerMu sync.Mutex
	timer   *time.Timer

	subConfig store.Subscription
	subRunNow store.Subscription
}

func NewAutoCheckinService(db *gorm.DB, store store.Store, encryptionSvc encryption.Service) *AutoCheckinService {
	// Note: rand.Seed is deprecated in Go 1.20+, global RNG is auto-seeded at startup
	var transport *http.Transport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
		transport.MaxIdleConns = 50  // Reduced for aggressive memory release (site management is non-critical)
		transport.MaxIdleConnsPerHost = 10  // Reduced for aggressive memory release
		transport.IdleConnTimeout = 5 * time.Second // Aggressive timeout for faster resource cleanup
	} else {
		// Fallback if DefaultTransport was replaced with a different type
		transport = &http.Transport{
			MaxIdleConns:        50,  // Reduced for aggressive memory release
			MaxIdleConnsPerHost: 10,  // Reduced for aggressive memory release
			IdleConnTimeout:     5 * time.Second, // Aggressive timeout for faster resource cleanup
		}
	}

	return &AutoCheckinService{
		db:            db,
		store:         store,
		encryptionSvc: encryptionSvc,
		client: &http.Client{
			Transport: transport,
			Timeout:   20 * time.Second,
		},
		stealthClientMgr: NewStealthClientManager(30 * time.Second),
		stopCh:           make(chan struct{}),
		rescheduleCh:     make(chan struct{}, 1),
		runNowCh:         make(chan struct{}, 1),
		cleanupCh:        make(chan struct{}),
	}
}

func (s *AutoCheckinService) Start() {
	if s.store == nil {
		logrus.Debug("ManagedSite AutoCheckinService disabled: store not configured")
		return
	}

	s.wg.Add(1)
	go s.runLoop()

	// Start periodic cleanup goroutine for aggressive memory release
	s.wg.Add(1)
	go s.periodicCleanup()

	// Best-effort subscriptions for multi-node setups.
	if sub, err := s.store.Subscribe(autoCheckinConfigUpdatedChannel); err == nil {
		s.subConfig = sub
		s.wg.Add(1)
		go s.listenSubscription(sub, s.rescheduleCh)
	}
	if sub, err := s.store.Subscribe(autoCheckinRunNowChannel); err == nil {
		s.subRunNow = sub
		s.wg.Add(1)
		go s.listenSubscription(sub, s.runNowCh)
	}

	// Initial schedule.
	s.requestReschedule()
	logrus.Debug("ManagedSite AutoCheckinService started")
}

func (s *AutoCheckinService) Stop(ctx context.Context) {
	close(s.stopCh)
	close(s.cleanupCh) // Stop periodic cleanup goroutine

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

	if s.subConfig != nil {
		_ = s.subConfig.Close()
	}
	if s.subRunNow != nil {
		_ = s.subRunNow.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("ManagedSite AutoCheckinService stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("ManagedSite AutoCheckinService stop timed out.")
	}
}

func (s *AutoCheckinService) GetStatus() AutoCheckinStatus {
	if s.store == nil {
		return AutoCheckinStatus{IsRunning: false}
	}
	data, err := s.store.Get(autoCheckinStatusKey)
	if err != nil {
		return AutoCheckinStatus{IsRunning: false}
	}
	var st AutoCheckinStatus
	if json.Unmarshal(data, &st) != nil {
		return AutoCheckinStatus{IsRunning: false}
	}
	return st
}

func (s *AutoCheckinService) TriggerRunNow() {
	select {
	case s.runNowCh <- struct{}{}:
	default:
	}

	if s.store == nil {
		return
	}
	_ = s.store.Publish(autoCheckinRunNowChannel, []byte("1"))
}

func (s *AutoCheckinService) requestReschedule() {
	select {
	case s.rescheduleCh <- struct{}{}:
	default:
	}
}

func (s *AutoCheckinService) listenSubscription(sub store.Subscription, out chan<- struct{}) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case _, ok := <-sub.Channel():
			if !ok {
				return
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}
}

func (s *AutoCheckinService) runLoop() {
	defer s.wg.Done()

	for {
		next, enabled, err := s.computeNextTriggerTime(context.Background())
		if err != nil {
			logrus.WithError(err).Warn("ManagedSite AutoCheckinService: compute next trigger failed")
			// Backoff on unexpected errors.
			next = time.Now().Add(5 * time.Minute)
			enabled = true
		}
		if enabled {
			s.setNextScheduledAt(next)
		} else {
			st := s.GetStatus()
			st.NextScheduledAt = ""
			s.setStatus(st)
		}
		s.resetTimer(time.Until(next))

		select {
		case <-s.stopCh:
			s.stopTimer()
			return
		case <-s.rescheduleCh:
			s.stopTimer()
			continue
		case <-s.runNowCh:
			s.stopTimer()
			s.runAllCheckins(context.Background())
			continue
		case <-s.timerC():
			s.stopTimer()
			s.runAllCheckins(context.Background())
			continue
		}
	}
}

func (s *AutoCheckinService) timerC() <-chan time.Time {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.timer == nil {
		ch := make(chan time.Time)
		close(ch)
		return ch
	}
	return s.timer.C
}

func (s *AutoCheckinService) resetTimer(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.timer == nil {
		s.timer = time.NewTimer(d)
		return
	}
	s.timer.Reset(d)
}

func (s *AutoCheckinService) stopTimer() {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.timer == nil {
		return
	}
	if !s.timer.Stop() {
		select {
		case <-s.timer.C:
		default:
		}
	}
	// Keep the timer object for reuse.
}

func (s *AutoCheckinService) setStatus(status AutoCheckinStatus) {
	if s.store == nil {
		return
	}
	b, err := json.Marshal(status)
	if err != nil {
		return
	}
	_ = s.store.Set(autoCheckinStatusKey, b, 24*time.Hour)
}

func (s *AutoCheckinService) setNextScheduledAt(next time.Time) {
	st := s.GetStatus()
	st.NextScheduledAt = next.UTC().Format(time.RFC3339)
	s.setStatus(st)
}

func (s *AutoCheckinService) setRunning(running bool) {
	st := s.GetStatus()
	st.IsRunning = running
	s.setStatus(st)
}

func (s *AutoCheckinService) computeNextTriggerTime(ctx context.Context) (time.Time, bool, error) {
	if s.store != nil {
		// Avoid DB contention during heavy import/delete tasks.
		if s.isBusy() {
			return time.Now().Add(5 * time.Minute), true, nil
		}
	}

	config, err := s.loadConfig(ctx)
	if err != nil {
		return time.Time{}, true, err
	}
	if !config.GlobalEnabled {
		// Poll periodically to recover even if pubsub is unavailable.
		return time.Now().Add(10 * time.Minute), false, nil
	}

	status := s.GetStatus()
	now := time.Now()
	retryTime := computeRetryTime(config, status, now)
	if !retryTime.IsZero() {
		return retryTime, true, nil
	}

	// Handle different schedule modes
	switch config.ScheduleMode {
	case AutoCheckinScheduleModeMultiple:
		if t := computeMultipleTrigger(config.ScheduleTimes, now); !t.IsZero() {
			return t, true, nil
		}
	case AutoCheckinScheduleModeDeterministic:
		if t := computeDeterministicTrigger(config, now); !t.IsZero() {
			return t, true, nil
		}
	}
	// Default to random mode
	next, err := computeRandomTrigger(config.WindowStart, config.WindowEnd, now)
	return next, true, err
}

func (s *AutoCheckinService) loadConfig(ctx context.Context) (*AutoCheckinConfig, error) {
	var row ManagedSiteSetting
	err := s.db.WithContext(ctx).First(&row, 1).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Singleton row (ID=1) created at startup; race condition extremely unlikely
			row = ManagedSiteSetting{ID: 1, ScheduleTimes: "09:00", WindowStart: "09:00", WindowEnd: "18:00", ScheduleMode: AutoCheckinScheduleModeMultiple, RetryIntervalMinutes: 60, RetryMaxAttemptsPerDay: 2}
			if createErr := s.db.WithContext(ctx).Create(&row).Error; createErr != nil {
				return nil, app_errors.ParseDBError(createErr)
			}
		} else {
			return nil, app_errors.ParseDBError(err)
		}
	}

	// Parse schedule times from comma-separated string
	var scheduleTimes []string
	if row.ScheduleTimes != "" {
		for _, t := range strings.Split(row.ScheduleTimes, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				scheduleTimes = append(scheduleTimes, t)
			}
		}
	}
	if len(scheduleTimes) == 0 {
		scheduleTimes = []string{"09:00"}
	}

	return &AutoCheckinConfig{
		GlobalEnabled:     row.AutoCheckinEnabled,
		ScheduleTimes:     scheduleTimes,
		WindowStart:       row.WindowStart,
		WindowEnd:         row.WindowEnd,
		ScheduleMode:      row.ScheduleMode,
		DeterministicTime: row.DeterministicTime,
		RetryStrategy: AutoCheckinRetryStrategy{
			Enabled:           row.RetryEnabled,
			IntervalMinutes:   row.RetryIntervalMinutes,
			MaxAttemptsPerDay: row.RetryMaxAttemptsPerDay,
		},
	}, nil
}

// computeMultipleTrigger calculates the next trigger time from multiple scheduled times.
// All times are in Beijing time (UTC+8).
func computeMultipleTrigger(scheduleTimes []string, now time.Time) time.Time {
	if len(scheduleTimes) == 0 {
		return time.Time{}
	}

	beijingNow := now.In(beijingLocation)
	var nextTrigger time.Time

	for _, timeStr := range scheduleTimes {
		minutes, err := parseTimeToMinutes(timeStr)
		if err != nil {
			continue
		}

		// Create target time for today in Beijing timezone
		target := time.Date(beijingNow.Year(), beijingNow.Month(), beijingNow.Day(),
			minutes/60, minutes%60, 0, 0, beijingLocation)

		// If target is in the past, schedule for tomorrow
		if !target.After(beijingNow) {
			target = target.Add(24 * time.Hour)
		}

		// Find the earliest next trigger
		if nextTrigger.IsZero() || target.Before(nextTrigger) {
			nextTrigger = target
		}
	}

	return nextTrigger
}

func computeRetryTime(cfg *AutoCheckinConfig, st AutoCheckinStatus, now time.Time) time.Time {
	if !cfg.RetryStrategy.Enabled || !st.PendingRetry {
		return time.Time{}
	}
	if st.Attempts == nil || st.Attempts.Date == "" {
		return time.Time{}
	}
	if st.Attempts.Date != todayString(now) {
		return time.Time{}
	}
	if st.Attempts.Attempts >= cfg.RetryStrategy.MaxAttemptsPerDay {
		return time.Time{}
	}
	if st.LastRunAt == "" {
		return time.Time{}
	}
	lastRun, err := time.Parse(time.RFC3339, st.LastRunAt)
	if err != nil {
		return time.Time{}
	}
	next := lastRun.Add(time.Duration(cfg.RetryStrategy.IntervalMinutes) * time.Minute)
	if next.Before(now) {
		return now.Add(15 * time.Second)
	}
	return next
}

func computeDeterministicTrigger(cfg *AutoCheckinConfig, now time.Time) time.Time {
	deterministicMin, err := parseTimeToMinutes(cfg.DeterministicTime)
	if err != nil {
		return time.Time{}
	}
	startMin, err := parseTimeToMinutes(cfg.WindowStart)
	if err != nil {
		return time.Time{}
	}
	endMin, err := parseTimeToMinutes(cfg.WindowEnd)
	if err != nil {
		return time.Time{}
	}
	if !isMinutesWithinWindow(deterministicMin, startMin, endMin) {
		return time.Time{}
	}

	// Use Beijing time (UTC+8) for scheduling
	beijingNow := now.In(beijingLocation)
	target := time.Date(beijingNow.Year(), beijingNow.Month(), beijingNow.Day(), deterministicMin/60, deterministicMin%60, 0, 0, beijingLocation)
	if !target.After(beijingNow) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

func computeRandomTrigger(windowStart, windowEnd string, now time.Time) (time.Time, error) {
	startMin, err := parseTimeToMinutes(windowStart)
	if err != nil {
		return time.Time{}, err
	}
	endMin, err := parseTimeToMinutes(windowEnd)
	if err != nil {
		return time.Time{}, err
	}

	// Use Beijing time (UTC+8) for scheduling
	beijingNow := now.In(beijingLocation)
	today := time.Date(beijingNow.Year(), beijingNow.Month(), beijingNow.Day(), 0, 0, 0, 0, beijingLocation)
	start := today.Add(time.Duration(startMin) * time.Minute)
	end := today.Add(time.Duration(endMin) * time.Minute)

	nowMin := beijingNow.Hour()*60 + beijingNow.Minute()
	if end.Before(start) || end.Equal(start) {
		end = end.Add(24 * time.Hour)
		// Window crosses midnight and we're after midnight but before the end.
		if beijingNow.Before(start) && nowMin <= endMin {
			start = start.Add(-24 * time.Hour)
			end = end.Add(-24 * time.Hour)
		}
	}

	if beijingNow.After(end) {
		start = start.Add(24 * time.Hour)
		end = end.Add(24 * time.Hour)
	} else if beijingNow.After(start) {
		start = beijingNow
	}

	duration := end.Sub(start)
	if duration <= 0 {
		return beijingNow.Add(24 * time.Hour), nil
	}
	offset := time.Duration(rand.Int63n(int64(duration)))
	return start.Add(offset), nil
}

func todayString(now time.Time) string {
	// Use Beijing time (UTC+8) for date string
	return now.In(beijingLocation).Format("2006-01-02")
}

func (s *AutoCheckinService) runAllCheckins(ctx context.Context) {
	if s.store != nil && s.isBusy() {
		logrus.Debug("ManagedSite AutoCheckinService: busy mode detected, skipping run")
		return
	}

	config, err := s.loadConfig(ctx)
	if err != nil {
		logrus.WithError(err).Warn("ManagedSite AutoCheckinService: load config failed")
		return
	}
	if !config.GlobalEnabled {
		logrus.Debug("ManagedSite AutoCheckinService: global disabled")
		return
	}

	s.setRunning(true)
	defer s.setRunning(false)

	var sites []ManagedSite
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Query sites where auto check-in should run: enabled=true AND (checkin_enabled=true OR auto_checkin_enabled=true)
	// This supports both new logic (checkin_enabled) and legacy data (auto_checkin_enabled)
	err = s.db.WithContext(qctx).
		Select("id, name, base_url, site_type, user_id, custom_checkin_url, use_proxy, proxy_url, checkin_enabled, auth_type, auth_value").
		Where("enabled = ? AND (checkin_enabled = ? OR auto_checkin_enabled = ?)", true, true, true).
		Order("id ASC").
		Find(&sites).Error
	if err != nil {
		logrus.WithError(err).Warn("ManagedSite AutoCheckinService: query sites failed")
		return
	}

	// Note: We intentionally do NOT skip sites that show "already checked in" status.
	// Reasons:
	// 1. Each site may have different check-in time windows (not necessarily starting at 00:00)
	// 2. The site's "today" definition may differ from our system's timezone
	// 3. The "already_checked" status from a previous attempt may be stale
	// The site's API will return "already_checked" if truly checked in, which is harmless.

	result := s.runSitesCheckin(ctx, sites)
	s.persistRunStatus(config, result)

	// Close idle connections after batch check-in to free resources immediately
	s.closeIdleConnections()

	logrus.WithFields(logrus.Fields{
		"total":       len(sites),
		"success":     result.SuccessCount,
		"failed":      result.FailedCount,
		"skipped":     result.SkippedCount,
		"needs_retry": result.NeedsRetry,
	}).Info("ManagedSite auto check-in run completed")
}

type runSummary struct {
	TotalEligible int
	Executed      int
	SuccessCount  int
	FailedCount   int
	SkippedCount  int
	NeedsRetry    bool
}

func (s *AutoCheckinService) runSitesCheckin(ctx context.Context, sites []ManagedSite) runSummary {
	summary := runSummary{TotalEligible: len(sites)}
	if len(sites) == 0 {
		summary.SkippedCount = 0
		return summary
	}

	concurrency := 5
	if len(sites) < concurrency {
		concurrency = len(sites)
	}

	jobs := make(chan ManagedSite)
	results := make(chan CheckinResult)

	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for site := range jobs {
				res := s.checkInOne(ctx, site)
				results <- res
			}
		}()
	}

	go func() {
		for i := range sites {
			jobs <- sites[i]
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	for res := range results {
		summary.Executed++
		switch res.Status {
		case CheckinResultSuccess, CheckinResultAlreadyChecked:
			summary.SuccessCount++
		case CheckinResultSkipped:
			summary.SkippedCount++
		default:
			summary.FailedCount++
		}
	}

	if summary.FailedCount > 0 {
		summary.NeedsRetry = true
	}
	return summary
}

type CheckinResult struct {
	SiteID  uint   `json:"site_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// CheckInSite performs a manual check-in for a specific site.
// Note: This does NOT check if the site was already checked in today.
// Each site may have different check-in time windows, so we always attempt
// the check-in and let the site's API determine if it's valid.
func (s *AutoCheckinService) CheckInSite(ctx context.Context, siteID uint) (*CheckinResult, error) {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	// Check both checkin_enabled and auto_checkin_enabled for backward compatibility with legacy data
	if !site.Enabled || (!site.CheckInEnabled && !site.AutoCheckInEnabled) {
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "check-in is disabled")
	}
	res := s.checkInOne(ctx, site)
	return &res, nil
}

func (s *AutoCheckinService) checkInOne(ctx context.Context, site ManagedSite) CheckinResult {
	result := CheckinResult{SiteID: site.ID, Status: CheckinResultFailed}

	provider := resolveProvider(site.SiteType)
	if provider == nil {
		result.Status = CheckinResultSkipped
		result.Message = "no provider"
		s.persistSiteResult(ctx, site.ID, result.Status, result.Message)
		return result
	}

	authValue, err := s.decryptAuthValue(site.AuthValue)
	if err != nil {
		result.Status = CheckinResultFailed
		result.Message = "decrypt auth failed"
		s.persistSiteResult(ctx, site.ID, result.Status, result.Message)
		return result
	}

	// Decrypt user_id (stored encrypted like auth_value)
	userID, err := s.decryptAuthValue(site.UserID)
	if err != nil {
		result.Status = CheckinResultFailed
		result.Message = "decrypt user_id failed"
		s.persistSiteResult(ctx, site.ID, result.Status, result.Message)
		return result
	}
	site.UserID = userID

	// Get HTTP client based on bypass method and proxy settings
	client := s.getCheckinHTTPClient(site)

	res, err := provider.CheckIn(ctx, client, site, authValue)
	if err != nil {
		result.Status = CheckinResultFailed
		result.Message = err.Error()
		s.persistSiteResult(ctx, site.ID, result.Status, result.Message)
		return result
	}

	result.Status = res.Status
	result.Message = res.Message
	s.persistSiteResult(ctx, site.ID, result.Status, result.Message)

	return result
}

// getCheckinHTTPClient returns an HTTP client based on site's bypass method and proxy settings.
// For stealth bypass method, uses TLS fingerprint spoofing client.
// For normal requests, uses standard client with optional proxy.
func (s *AutoCheckinService) getCheckinHTTPClient(site ManagedSite) *http.Client {
	// Use stealth client for TLS fingerprint spoofing when explicitly enabled
	if isStealthBypassMethod(site.BypassMethod) {
		proxyURL := ""
		if site.UseProxy {
			proxyURL = strings.TrimSpace(site.ProxyURL)
		}
		return s.stealthClientMgr.GetClient(proxyURL)
	}

	// Use standard client with optional proxy
	return s.getHTTPClient(site.UseProxy, site.ProxyURL)
}

// getHTTPClient returns an HTTP client, optionally configured with proxy from site settings.
// When useProxy is true and proxyURL is provided, returns a proxy-enabled client from cache.
// Otherwise returns the default client without proxy.
// Proxy clients are cached by URL to enable connection pooling across requests.
func (s *AutoCheckinService) getHTTPClient(useProxy bool, proxyURL string) *http.Client {
	if !useProxy {
		return s.client
	}

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		// No proxy URL configured, use default client
		return s.client
	}

	// Check cache first (fast path)
	if cached, ok := s.proxyClients.Load(proxyURL); ok {
		return cached.(*http.Client)
	}

	// Parse and validate proxy URL
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		logrus.WithError(err).Warn("Invalid proxy URL in site settings, using direct connection")
		return s.client
	}

	// Create a new transport with proxy
	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsedProxyURL),
		MaxIdleConns:        50,  // Reduced for aggressive memory release (site management is non-critical)
		MaxIdleConnsPerHost: 10,  // Reduced for aggressive memory release
		IdleConnTimeout:     5 * time.Second, // Aggressive timeout for faster resource cleanup
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}

	// Store in cache (LoadOrStore handles race condition - if another goroutine
	// stored a client for the same URL, we use that one instead)
	actual, _ := s.proxyClients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client)
}

func (s *AutoCheckinService) decryptAuthValue(encrypted string) (string, error) {
	if strings.TrimSpace(encrypted) == "" {
		return "", nil
	}
	return s.encryptionSvc.Decrypt(encrypted)
}

func (s *AutoCheckinService) persistSiteResult(ctx context.Context, siteID uint, status, message string) {
	now := time.Now().UTC()
	date := todayString(now)

	update := map[string]any{
		"last_checkin_at":      now,
		"last_checkin_date":    date,
		"last_checkin_status":  status,
		"last_checkin_message": message,
	}
	uCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.db.WithContext(uCtx).Model(&ManagedSite{}).Where("id = ?", siteID).Updates(update).Error; err != nil {
		logrus.WithError(err).Debugf("Failed to update site %d check-in status", siteID)
	}

	logRow := ManagedSiteCheckinLog{SiteID: siteID, Status: status, Message: message, CreatedAt: now}
	lCtx, lcancel := context.WithTimeout(ctx, 2*time.Second)
	defer lcancel()
	if err := s.db.WithContext(lCtx).Create(&logRow).Error; err != nil {
		logrus.WithError(err).Debugf("Failed to create check-in log for site %d", siteID)
	}
}

func (s *AutoCheckinService) persistRunStatus(config *AutoCheckinConfig, summary runSummary) {
	st := s.GetStatus()
	st.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	st.Summary = &AutoCheckinRunSummary{
		TotalEligible: summary.TotalEligible,
		Executed:      summary.Executed,
		SuccessCount:  summary.SuccessCount,
		FailedCount:   summary.FailedCount,
		SkippedCount:  summary.SkippedCount,
		NeedsRetry:    summary.NeedsRetry,
	}

	// Update attempts tracker.
	if st.Attempts == nil {
		st.Attempts = &AutoCheckinAttemptsTracker{}
	}
	today := todayString(time.Now())
	if st.Attempts.Date != today {
		st.Attempts.Date = today
		st.Attempts.Attempts = 1
	} else {
		st.Attempts.Attempts++
	}

	shouldRetry := summary.FailedCount > 0 && config != nil && config.RetryStrategy.Enabled
	if summary.FailedCount == 0 {
		st.LastRunResult = AutoCheckinRunResultSuccess
		st.PendingRetry = false
	} else if summary.SuccessCount > 0 {
		st.LastRunResult = AutoCheckinRunResultPartial
		st.PendingRetry = shouldRetry
	} else {
		st.LastRunResult = AutoCheckinRunResultFailed
		st.PendingRetry = shouldRetry
	}

	s.setStatus(st)
}

func (s *AutoCheckinService) isBusy() bool {
	if s.store == nil {
		return false
	}
	b, err := s.store.Get("global_task")
	if err != nil {
		return false
	}
	var st struct {
		TaskType  string `json:"task_type"`
		IsRunning bool   `json:"is_running"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	if !st.IsRunning {
		return false
	}
	// Use local constants to avoid circular dependency with services package
	return st.TaskType == taskTypeKeyImport || st.TaskType == taskTypeKeyDelete || st.TaskType == taskTypeKeyRestore
}

// closeIdleConnections closes idle connections for all HTTP clients to free resources.
// This should be called after batch operations (check-in, balance refresh) complete.
func (s *AutoCheckinService) closeIdleConnections() {
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
func (s *AutoCheckinService) periodicCleanup() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Minute) // Cleanup every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-s.cleanupCh:
			return
		case <-ticker.C:
			s.closeIdleConnections()
			logrus.Debug("ManagedSite AutoCheckinService: periodic cleanup completed")
		}
	}
}

type providerResult struct {
	Status  string
	Message string
}

type checkinProvider interface {
	CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error)
}

func resolveProvider(siteType string) checkinProvider {
	switch siteType {
	case SiteTypeNewAPI, SiteTypeOneHub, SiteTypeDoneHub:
		// NewAPI-compatible sites share the same checkin endpoint: POST /api/user/checkin
		return newAPIProvider{}
	case SiteTypeVeloera:
		return veloeraProvider{}
	case SiteTypeWongGongyi:
		return wongProvider{}
	case SiteTypeAnyrouter:
		return anyrouterProvider{}
	// Note: SiteTypeBrand, SiteTypeUnknown do not have dedicated checkin providers
	default:
		return nil
	}
}

// extractBaseURL extracts the base URL (scheme + host) from a full URL.
// e.g., "https://ai.huan666.de/app/me" -> "https://ai.huan666.de"
func extractBaseURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return strings.TrimRight(rawURL, "/")
	}
	return u.Scheme + "://" + u.Host
}

// buildCheckinURL constructs the check-in API URL.
// If customURL is provided, it's appended to the base URL (extracted from siteBaseURL).
// Otherwise, the defaultPath is used.
// Examples:
//   - siteBaseURL="https://example.com/console", customURL="", defaultPath="/api/user/checkin"
//     -> "https://example.com/api/user/checkin"
//   - siteBaseURL="https://example.com", customURL="/custom/checkin", defaultPath="/api/user/checkin"
//     -> "https://example.com/custom/checkin"
func buildCheckinURL(siteBaseURL, customURL, defaultPath string) string {
	baseURL := extractBaseURL(siteBaseURL)
	path := strings.TrimSpace(customURL)
	if path == "" {
		path = defaultPath
	}
	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

func buildUserHeaders(userID string) map[string]string {
	// Multiple header names for compatibility with different third-party API implementations
	// (New-API, Veloera, VoAPI, One-Hub, Rix-API, Neo-API, etc.)
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return nil
	}
	return map[string]string{
		"New-API-User": uid,
		"Veloera-User": uid,
		"voapi-user":   uid,
		"User-id":      uid,
		"Rix-Api-User": uid,
		"neo-api-user": uid,
	}
}

// knownWAFCookieNames lists known Cloudflare/WAF cookie names that indicate bypass capability.
// At least one of these cookies should be present for stealth bypass to work.
// IMPORTANT: This list is duplicated in frontend (SiteManagementPanel.vue).
// Keep both lists in sync when adding/removing cookie names.
var knownWAFCookieNames = []string{
	"cf_clearance", // Cloudflare clearance cookie (most important)
	"acw_tc",       // Alibaba Cloud WAF cookie
	"cdn_sec_tc",   // CDN security cookie
	"acw_sc__v2",   // Alibaba Cloud WAF v2 cookie
	"__cf_bm",      // Cloudflare bot management
	"_cfuvid",      // Cloudflare unique visitor ID
}

// parseCookieString parses a cookie string into a map.
// Format: "key1=value1; key2=value2"
func parseCookieString(cookieStr string) map[string]string {
	result := make(map[string]string)
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, "=")
		if idx > 0 {
			key := strings.TrimSpace(part[:idx])
			val := strings.TrimSpace(part[idx+1:])
			result[key] = val
		}
	}
	return result
}

// validateCFCookies checks if the cookie string contains required CF/WAF cookies for stealth bypass.
// Returns list of missing cookie names if validation fails.
func validateCFCookies(cookieStr string) []string {
	if cookieStr == "" {
		return knownWAFCookieNames
	}

	cookieMap := parseCookieString(cookieStr)

	// Check if at least one WAF cookie is present
	for _, name := range knownWAFCookieNames {
		if _, ok := cookieMap[name]; ok {
			return nil // Found at least one WAF cookie
		}
	}

	// No WAF cookies found, return all as missing (user needs at least one)
	return knownWAFCookieNames
}

// shouldUseStealthRequest checks if stealth request should be used based on site config.
func shouldUseStealthRequest(site ManagedSite) bool {
	return isStealthBypassMethod(site.BypassMethod)
}

// isCFChallengeResponse checks if an HTTP response indicates a Cloudflare challenge.
// Returns true if the response appears to be a CF challenge page (403 with CF markers).
// Note: Per Cloudflare docs, the official way is to check cf-mitigated header,
// but we also check body content as fallback for compatibility with various setups.
func isCFChallengeResponse(statusCode int, responseBody []byte) bool {
	if statusCode != 403 {
		return false
	}
	// Normalize to lowercase once for consistent case-insensitive matching
	respLower := strings.ToLower(string(responseBody))
	return strings.Contains(respLower, "cloudflare") ||
		strings.Contains(respLower, "cf-") ||
		strings.Contains(respLower, "challenge") ||
		strings.Contains(respLower, "ray id")
}

func doJSONRequest(ctx context.Context, client *http.Client, method, fullURL string, headers map[string]string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	// Set default User-Agent to help bypass basic bot detection
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Return data along with error so caller can parse error response body
		return data, resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

// doStealthJSONRequest performs a JSON request with stealth headers for Cloudflare bypass.
// It applies browser-like headers to help evade bot detection.
func doStealthJSONRequest(ctx context.Context, client *http.Client, method, fullURL string, headers map[string]string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}

	// Extract base URL for Referer/Origin headers
	baseURL := extractBaseURL(fullURL)

	// Apply stealth headers first (browser-like fingerprint)
	applyStealthHeaders(req, baseURL)

	// Apply custom headers (may override stealth headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Set Content-Type for JSON body
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

func isAlreadyCheckedMessage(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	// Chinese patterns
	if strings.Contains(m, "已签到") || strings.Contains(m, "已经签到") || strings.Contains(m, "今天已") {
		return true
	}
	// English patterns (use specific phrases to avoid false positives like "Token already expired")
	if strings.Contains(m, "already checked") || strings.Contains(m, "already signed") {
		return true
	}
	// Japanese patterns
	if strings.Contains(m, "チェックイン済") || strings.Contains(m, "サインイン済") {
		return true
	}
	return false
}

type veloeraProvider struct{}

func (p veloeraProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	// Support both access_token and cookie auth
	if authValue == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}

	headers := buildUserHeaders(site.UserID)
	if headers == nil {
		headers = make(map[string]string)
	}

	useStealth := shouldUseStealthRequest(site)

	// Set auth header based on auth type
	switch site.AuthType {
	case AuthTypeAccessToken:
		if useStealth {
			return providerResult{Status: CheckinResultFailed, Message: "stealth bypass requires cookie auth"}, nil
		}
		if strings.TrimSpace(site.UserID) == "" {
			return providerResult{Status: CheckinResultSkipped, Message: "missing user_id"}, nil
		}
		headers["Authorization"] = "Bearer " + authValue
	case AuthTypeCookie:
		if useStealth {
			missingCookies := validateCFCookies(authValue)
			if len(missingCookies) > 0 {
				return providerResult{
					Status:  CheckinResultFailed,
					Message: fmt.Sprintf("missing cf cookies, need one of: %s", strings.Join(missingCookies, ", ")),
				}, nil
			}
		}
		headers["Cookie"] = authValue
	default:
		return providerResult{Status: CheckinResultSkipped, Message: "unsupported auth type"}, nil
	}

	apiURL := buildCheckinURL(site.BaseURL, site.CustomCheckInURL, "/api/user/check_in")

	var data []byte
	var statusCode int
	var err error
	if useStealth {
		data, statusCode, err = doStealthJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	} else {
		data, statusCode, err = doJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	}

	var resp struct {
		Success bool        `json:"success"`
		Message string      `json:"message"`
		Data    interface{} `json:"data"`
	}

	// Try to parse response body even on error (may contain useful error message)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &resp)
	}

	// Handle HTTP errors with parsed response message
	if err != nil {
		// Log detailed error info for debugging
		logrus.WithFields(logrus.Fields{
			"site_id":     site.ID,
			"site_name":   site.Name,
			"status_code": statusCode,
			"response":    string(data),
			"resp_msg":    resp.Message,
		}).Debug("Veloera check-in HTTP error")

		// Check for Cloudflare challenge response
		if useStealth && isCFChallengeResponse(statusCode, data) {
			return providerResult{Status: CheckinResultFailed, Message: "cloudflare challenge, update cookies from browser"}, nil
		}

		// Check if response body contains "already checked" message
		if isAlreadyCheckedMessage(resp.Message) {
			return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
		}
		// Return error with response message if available, otherwise HTTP status
		if resp.Message != "" {
			return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
		}
		return providerResult{}, fmt.Errorf("http %d", statusCode)
	}

	// Log response for debugging when check-in fails
	if !resp.Success {
		logrus.WithFields(logrus.Fields{
			"site_id":     site.ID,
			"site_name":   site.Name,
			"status_code": statusCode,
			"response":    string(data),
			"resp_msg":    resp.Message,
			"success":     resp.Success,
		}).Debug("Veloera check-in failed")
	}

	if isAlreadyCheckedMessage(resp.Message) {
		return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
	}
	if resp.Success {
		return providerResult{Status: CheckinResultSuccess, Message: resp.Message}, nil
	}
	if resp.Message != "" {
		return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
	}
	return providerResult{Status: CheckinResultFailed, Message: "check-in failed"}, nil
}

type wongProvider struct{}

func (p wongProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	// Support both access_token and cookie auth
	if authValue == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}

	headers := buildUserHeaders(site.UserID)
	if headers == nil {
		headers = make(map[string]string)
	}

	// Set auth header based on auth type
	switch site.AuthType {
	case AuthTypeAccessToken:
		headers["Authorization"] = "Bearer " + authValue
	case AuthTypeCookie:
		headers["Cookie"] = authValue
	default:
		return providerResult{Status: CheckinResultSkipped, Message: "unsupported auth type"}, nil
	}

	apiURL := buildCheckinURL(site.BaseURL, site.CustomCheckInURL, "/api/user/checkin")
	data, _, err := doJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	if err != nil {
		return providerResult{}, err
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			CheckedIn bool  `json:"checked_in"`
			Enabled   *bool `json:"enabled"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &resp)
	if resp.Data.Enabled != nil && !*resp.Data.Enabled {
		return providerResult{Status: CheckinResultFailed, Message: "check-in disabled"}, nil
	}
	if resp.Data.CheckedIn || isAlreadyCheckedMessage(resp.Message) {
		return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
	}
	if resp.Success {
		return providerResult{Status: CheckinResultSuccess, Message: resp.Message}, nil
	}
	if resp.Message != "" {
		return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
	}
	return providerResult{Status: CheckinResultFailed, Message: "check-in failed"}, nil
}

// anyrouterProvider handles check-in for Anyrouter sites.
// Anyrouter uses POST /api/user/sign_in endpoint with cookie auth.
// This provider uses stealth requests to bypass Cloudflare protection when enabled.
type anyrouterProvider struct{}

func (p anyrouterProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	// Anyrouter only supports cookie auth
	if authValue == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}
	if site.AuthType != AuthTypeCookie {
		return providerResult{Status: CheckinResultFailed, Message: "anyrouter requires cookie auth"}, nil
	}

	// Determine if stealth mode should be used
	// Use stealth only when explicitly enabled via bypass_method setting
	useStealth := isStealthBypassMethod(site.BypassMethod)

	// Only validate CF cookies when stealth mode is explicitly enabled
	if useStealth {
		missingCookies := validateCFCookies(authValue)
		if len(missingCookies) > 0 {
			return providerResult{
				Status:  CheckinResultFailed,
				Message: fmt.Sprintf("missing cf cookies, need one of: %s", strings.Join(missingCookies, ", ")),
			}, nil
		}
	}

	// Use new-api-user header for anyrouter (required for API authentication)
	headers := map[string]string{
		"Cookie":           authValue,
		"X-Requested-With": "XMLHttpRequest",
	}

	// Add user ID header if available (some anyrouter instances require this)
	if uid := strings.TrimSpace(site.UserID); uid != "" {
		headers["new-api-user"] = uid
	}

	apiURL := buildCheckinURL(site.BaseURL, site.CustomCheckInURL, "/api/user/sign_in")

	// Use stealth request for Cloudflare bypass when enabled
	var data []byte
	var statusCode int
	var err error
	if useStealth {
		data, statusCode, err = doStealthJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	} else {
		data, statusCode, err = doJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	if len(data) > 0 {
		_ = json.Unmarshal(data, &resp)
	}

	if err != nil {
		logrus.WithFields(logrus.Fields{
			"site_id":     site.ID,
			"site_name":   site.Name,
			"status_code": statusCode,
			"response":    string(data),
		}).Debug("Anyrouter check-in HTTP error")

		// Check for Cloudflare challenge response
		if isCFChallengeResponse(statusCode, data) {
			return providerResult{Status: CheckinResultFailed, Message: "cloudflare challenge, update cookies from browser"}, nil
		}

		if isAlreadyCheckedMessage(resp.Message) {
			return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
		}
		if resp.Message != "" {
			return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
		}
		return providerResult{}, fmt.Errorf("http %d", statusCode)
	}

	// Anyrouter returns empty message when already checked in
	if resp.Message == "" && resp.Success {
		return providerResult{Status: CheckinResultAlreadyChecked, Message: "already checked in"}, nil
	}

	if isAlreadyCheckedMessage(resp.Message) {
		return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
	}
	if resp.Success {
		return providerResult{Status: CheckinResultSuccess, Message: resp.Message}, nil
	}
	if resp.Message != "" {
		return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
	}
	return providerResult{Status: CheckinResultFailed, Message: "check-in failed"}, nil
}

// newAPIProvider handles check-in for NewAPI-compatible sites (new-api, one-hub, done-hub).
// These sites share the same check-in endpoint: POST /api/user/checkin
type newAPIProvider struct{}

func (p newAPIProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	// Support both access_token and cookie auth
	if authValue == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}

	headers := buildUserHeaders(site.UserID)
	if headers == nil {
		headers = make(map[string]string)
	}

	useStealth := shouldUseStealthRequest(site)

	// Set auth header based on auth type
	switch site.AuthType {
	case AuthTypeAccessToken:
		// Stealth bypass requires cookie auth for CF cookies
		if useStealth {
			return providerResult{Status: CheckinResultFailed, Message: "stealth bypass requires cookie auth"}, nil
		}
		headers["Authorization"] = "Bearer " + authValue
	case AuthTypeCookie:
		// Validate CF cookies when stealth mode is enabled
		if useStealth {
			missingCookies := validateCFCookies(authValue)
			if len(missingCookies) > 0 {
				// Return detailed error message about missing CF cookies
				return providerResult{
					Status:  CheckinResultFailed,
					Message: fmt.Sprintf("missing cf cookies, need one of: %s", strings.Join(missingCookies, ", ")),
				}, nil
			}
		}
		headers["Cookie"] = authValue
	default:
		return providerResult{Status: CheckinResultSkipped, Message: "unsupported auth type"}, nil
	}

	apiURL := buildCheckinURL(site.BaseURL, site.CustomCheckInURL, "/api/user/checkin")

	// Use stealth request for Cloudflare bypass when bypass_method is "stealth"
	var data []byte
	var statusCode int
	var err error
	if useStealth {
		data, statusCode, err = doStealthJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	} else {
		data, statusCode, err = doJSONRequest(ctx, client, http.MethodPost, apiURL, headers, map[string]any{})
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}

	// Try to parse response body even on error (may contain useful error message)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &resp)
	}

	// Handle HTTP errors with parsed response message
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"site_id":     site.ID,
			"site_name":   site.Name,
			"status_code": statusCode,
			"response":    string(data),
			"resp_msg":    resp.Message,
		}).Debug("NewAPI check-in HTTP error")

		// Check for Cloudflare challenge response
		if useStealth && isCFChallengeResponse(statusCode, data) {
			return providerResult{Status: CheckinResultFailed, Message: "cloudflare challenge, update cookies from browser"}, nil
		}

		// Check if response body contains "already checked" message
		if isAlreadyCheckedMessage(resp.Message) {
			return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
		}
		// Return error with response message if available, otherwise HTTP status
		if resp.Message != "" {
			return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
		}
		return providerResult{}, fmt.Errorf("http %d", statusCode)
	}

	// Log response for debugging when check-in fails
	if !resp.Success {
		logrus.WithFields(logrus.Fields{
			"site_id":     site.ID,
			"site_name":   site.Name,
			"status_code": statusCode,
			"response":    string(data),
			"resp_msg":    resp.Message,
			"success":     resp.Success,
		}).Debug("NewAPI check-in failed")
	}

	// Check for "already checked in" message
	if isAlreadyCheckedMessage(resp.Message) {
		return providerResult{Status: CheckinResultAlreadyChecked, Message: resp.Message}, nil
	}
	// Success case
	if resp.Success {
		return providerResult{Status: CheckinResultSuccess, Message: resp.Message}, nil
	}
	// Failure with message
	if resp.Message != "" {
		return providerResult{Status: CheckinResultFailed, Message: resp.Message}, nil
	}
	return providerResult{Status: CheckinResultFailed, Message: "check-in failed"}, nil
}
