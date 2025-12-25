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
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	autoCheckinStatusKey     = "managed_site:auto_checkin_status"
	autoCheckinRunNowChannel = "managed_site:auto_checkin_run_now"
)

type AutoCheckinService struct {
	db            *gorm.DB
	store         store.Store
	encryptionSvc encryption.Service
	client        *http.Client

	stopCh       chan struct{}
	rescheduleCh chan struct{}
	runNowCh     chan struct{}

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
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 20
		transport.IdleConnTimeout = 90 * time.Second
	} else {
		// Fallback if DefaultTransport was replaced with a different type
		transport = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
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
		stopCh:       make(chan struct{}),
		rescheduleCh: make(chan struct{}, 1),
		runNowCh:     make(chan struct{}, 1),
	}
}

func (s *AutoCheckinService) Start() {
	if s.store == nil {
		logrus.Debug("ManagedSite AutoCheckinService disabled: store not configured")
		return
	}

	s.wg.Add(1)
	go s.runLoop()

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

	if config.ScheduleMode == AutoCheckinScheduleModeDeterministic {
		if t := computeDeterministicTrigger(config, now); !t.IsZero() {
			return t, true, nil
		}
	}
	next, err := computeRandomTrigger(config.WindowStart, config.WindowEnd, now)
	return next, true, err
}

func (s *AutoCheckinService) loadConfig(ctx context.Context) (*AutoCheckinConfig, error) {
	var row ManagedSiteSetting
	err := s.db.WithContext(ctx).First(&row, 1).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Keep defaults aligned with SiteService.ensureSettingsRow.
			row = ManagedSiteSetting{ID: 1, WindowStart: "09:00", WindowEnd: "18:00", ScheduleMode: AutoCheckinScheduleModeRandom, RetryIntervalMinutes: 60, RetryMaxAttemptsPerDay: 2}
			if createErr := s.db.WithContext(ctx).Create(&row).Error; createErr != nil {
				return nil, app_errors.ParseDBError(createErr)
			}
		} else {
			return nil, app_errors.ParseDBError(err)
		}
	}

	return &AutoCheckinConfig{
		GlobalEnabled:     row.AutoCheckinEnabled,
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

	target := time.Date(now.Year(), now.Month(), now.Day(), deterministicMin/60, deterministicMin%60, 0, 0, now.Location())
	if !target.After(now) {
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

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.Add(time.Duration(startMin) * time.Minute)
	end := today.Add(time.Duration(endMin) * time.Minute)

	nowMin := now.Hour()*60 + now.Minute()
	if end.Before(start) || end.Equal(start) {
		end = end.Add(24 * time.Hour)
		// Window crosses midnight and we're after midnight but before the end.
		if now.Before(start) && nowMin <= endMin {
			start = start.Add(-24 * time.Hour)
			end = end.Add(-24 * time.Hour)
		}
	}

	if now.After(end) {
		start = start.Add(24 * time.Hour)
		end = end.Add(24 * time.Hour)
	} else if now.After(start) {
		start = now
	}

	duration := end.Sub(start)
	if duration <= 0 {
		return now.Add(24 * time.Hour), nil
	}
	offset := time.Duration(rand.Int63n(int64(duration)))
	return start.Add(offset), nil
}

func todayString(now time.Time) string {
	return now.Format("2006-01-02")
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
	err = s.db.WithContext(qctx).
		Select("id, name, base_url, site_type, user_id, custom_checkin_url, check_in_enabled, auto_checkin_enabled, auth_type, auth_value").
		Where("enabled = ? AND check_in_enabled = ? AND auto_checkin_enabled = ?", true, true, true).
		Order("id ASC").
		Find(&sites).Error
	cancel()
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
	if !site.Enabled || !site.CheckInEnabled {
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

	res, err := provider.CheckIn(ctx, s.client, site, authValue)
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

func (s *AutoCheckinService) decryptAuthValue(encrypted string) (string, error) {
	if strings.TrimSpace(encrypted) == "" {
		return "", nil
	}
	return s.encryptionSvc.Decrypt(encrypted)
}

func (s *AutoCheckinService) persistSiteResult(ctx context.Context, siteID uint, status, message string) {
	now := time.Now().UTC()
	date := todayString(time.Now().UTC())

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
	return st.TaskType == "KEY_IMPORT" || st.TaskType == "KEY_DELETE"
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
	case SiteTypeVeloera:
		return veloeraProvider{}
	case SiteTypeAnyrouter:
		return anyrouterProvider{}
	case SiteTypeWongGongyi:
		return wongProvider{}
	default:
		return nil
	}
}

func buildUserHeaders(userID string) map[string]string {
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

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
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
	return strings.Contains(m, "已签到") || strings.Contains(m, "已经签到") || strings.Contains(m, "already")
}

type veloeraProvider struct{}

func (p veloeraProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	if site.AuthType != AuthTypeAccessToken || authValue == "" || strings.TrimSpace(site.UserID) == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}

	headers := buildUserHeaders(site.UserID)
	headers["Authorization"] = "Bearer " + authValue

	url := strings.TrimRight(site.BaseURL, "/") + "/api/user/check_in"
	data, _, err := doJSONRequest(ctx, client, http.MethodPost, url, headers, map[string]any{})
	if err != nil {
		return providerResult{}, err
	}

	var resp struct {
		Success bool        `json:"success"`
		Message string      `json:"message"`
		Data    interface{} `json:"data"`
	}
	_ = json.Unmarshal(data, &resp)
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
	headers := buildUserHeaders(site.UserID)
	if site.AuthType == AuthTypeAccessToken {
		if authValue == "" {
			return providerResult{Status: CheckinResultSkipped, Message: "missing token"}, nil
		}
		headers["Authorization"] = "Bearer " + authValue
	} else if site.AuthType == AuthTypeCookie {
		if authValue == "" {
			return providerResult{Status: CheckinResultSkipped, Message: "missing cookie"}, nil
		}
		headers["Cookie"] = authValue
	} else {
		return providerResult{Status: CheckinResultSkipped, Message: "unsupported auth"}, nil
	}

	url := strings.TrimRight(site.BaseURL, "/") + "/api/user/checkin"
	data, _, err := doJSONRequest(ctx, client, http.MethodPost, url, headers, map[string]any{})
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

type anyrouterProvider struct{}

func (p anyrouterProvider) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authValue string) (providerResult, error) {
	if site.AuthType != AuthTypeCookie || authValue == "" || strings.TrimSpace(site.UserID) == "" {
		return providerResult{Status: CheckinResultSkipped, Message: "missing credentials"}, nil
	}
	headers := map[string]string{
		"Cookie":           authValue,
		"X-Requested-With": "XMLHttpRequest",
		"Content-Type":     "application/json",
	}
	url := strings.TrimRight(site.BaseURL, "/") + "/api/user/sign_in"
	data, _, err := doJSONRequest(ctx, client, http.MethodPost, url, headers, json.RawMessage("{}"))
	if err != nil {
		return providerResult{}, err
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &resp)
	msg := strings.TrimSpace(resp.Message)
	if resp.Success {
		if msg == "" || isAlreadyCheckedMessage(msg) {
			return providerResult{Status: CheckinResultAlreadyChecked, Message: msg}, nil
		}
		if strings.Contains(strings.ToLower(msg), "success") || strings.Contains(msg, "签到成功") {
			return providerResult{Status: CheckinResultSuccess, Message: msg}, nil
		}
		return providerResult{Status: CheckinResultSuccess, Message: msg}, nil
	}
	if msg != "" {
		return providerResult{Status: CheckinResultFailed, Message: msg}, nil
	}
	return providerResult{Status: CheckinResultFailed, Message: "check-in failed"}, nil
}
