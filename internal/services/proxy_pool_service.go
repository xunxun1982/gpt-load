package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"gorm.io/gorm"
)

const (
	defaultProxyPoolTestTargetURL = "https://www.gstatic.com/generate_204"
	defaultProxyPoolTestTimeout   = 10 * time.Second
	defaultGatewayProxyTestCount  = 3
	defaultGatewayProxyTestGap    = 3 * time.Second

	defaultProxyPoolCountryLookupURL = "http://ip-api.com/json/?fields=status,country,countryCode,query"
)

// ProxyPoolInput captures editable proxy pool fields.
type ProxyPoolInput struct {
	Name string
	URL  string
}

// ProxyPoolTestResult reports the result of an explicit proxy connectivity test.
type ProxyPoolTestResult struct {
	Success            bool   `json:"success"`
	URL                string `json:"url"`
	TargetURL          string `json:"target_url"`
	TimeoutMS          int64  `json:"timeout_ms"`
	DurationMS         int64  `json:"duration_ms"`
	Attempts           int    `json:"attempts,omitempty"`
	SuccessfulAttempts int    `json:"successful_attempts,omitempty"`
	FailedAttempts     int    `json:"failed_attempts,omitempty"`
	StatusCode         int    `json:"status_code,omitempty"`
	CountryCode        string `json:"country_code,omitempty"`
	CountryName        string `json:"country_name,omitempty"`
	Error              string `json:"error,omitempty"`
}

// ProxyPoolSelectionOption is used by proxy selectors outside the proxy pool page.
type ProxyPoolSelectionOption struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
	URL   string `json:"url,omitempty"`
}

// GatewayProxyOption describes a built-in API gateway proxy choice.
type GatewayProxyOption struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Value       string `json:"value"`
	CandidateID string `json:"candidate_id"`
	URL         string `json:"url"`
	Active      bool   `json:"active,omitempty"`
}

// ProxyPoolService manages reusable upstream proxy URLs.
type ProxyPoolService struct {
	db                       *gorm.DB
	healthCheckTarget        string
	healthCheckTimeout       time.Duration
	countryLookupURL         string
	gatewayProxyOptions      []GatewayProxyOption
	gatewayProxyTestCount    int
	gatewayProxyTestGap      time.Duration
	settingsProvider         ProxyPoolSettingsProvider
	invalidateProxySelection func()
	gatewayAutoTestMu        sync.Mutex
	gatewayAutoTestCancel    context.CancelFunc
	gatewayAutoTestDone      chan struct{}
}

// ProxyPoolSettingsProvider supplies runtime proxy pool health-check settings.
type ProxyPoolSettingsProvider interface {
	GetSettings() types.SystemSettings
}

// ProxyPoolServiceOption customizes ProxyPoolService behavior.
type ProxyPoolServiceOption func(*ProxyPoolService)

// WithProxyPoolHealthCheck overrides the proxy health-check target and timeout.
func WithProxyPoolHealthCheck(targetURL string, timeout time.Duration) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		if strings.TrimSpace(targetURL) != "" {
			s.healthCheckTarget = strings.TrimSpace(targetURL)
		}
		if timeout > 0 {
			s.healthCheckTimeout = timeout
		}
	}
}

// WithProxyPoolSettingsProvider lets proxy tests use the current system settings.
func WithProxyPoolSettingsProvider(provider ProxyPoolSettingsProvider) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		s.settingsProvider = provider
	}
}

// WithProxyPoolCountryLookupURL overrides the lightweight country lookup endpoint.
func WithProxyPoolCountryLookupURL(rawURL string) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		if strings.TrimSpace(rawURL) != "" {
			s.countryLookupURL = strings.TrimSpace(rawURL)
		}
	}
}

// WithGatewayProxyOptions overrides built-in gateway proxy options for tests.
func WithGatewayProxyOptions(options []GatewayProxyOption) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		s.gatewayProxyOptions = append([]GatewayProxyOption(nil), options...)
	}
}

// WithGatewayProxySampling overrides gateway test sampling for unit tests.
func WithGatewayProxySampling(count int, gap time.Duration) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		if count > 0 {
			s.gatewayProxyTestCount = count
		}
		if gap >= 0 {
			s.gatewayProxyTestGap = gap
		}
	}
}

// WithProxyPoolSelectionInvalidation refreshes runtime proxy config after manual proxy changes.
func WithProxyPoolSelectionInvalidation(callback func()) ProxyPoolServiceOption {
	return func(s *ProxyPoolService) {
		s.invalidateProxySelection = callback
	}
}

// NewProxyPoolService constructs a ProxyPoolService.
func NewProxyPoolService(db *gorm.DB) *ProxyPoolService {
	return NewProxyPoolServiceWithOptions(db)
}

// NewProxyPoolServiceWithOptions constructs a ProxyPoolService with explicit options.
func NewProxyPoolServiceWithOptions(db *gorm.DB, opts ...ProxyPoolServiceOption) *ProxyPoolService {
	svc := &ProxyPoolService{
		db:                    db,
		healthCheckTarget:     defaultProxyPoolTestTargetURL,
		healthCheckTimeout:    defaultProxyPoolTestTimeout,
		countryLookupURL:      defaultProxyPoolCountryLookupURL,
		gatewayProxyOptions:   defaultGatewayProxyOptions(),
		gatewayProxyTestCount: defaultGatewayProxyTestCount,
		gatewayProxyTestGap:   defaultGatewayProxyTestGap,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func normalizeProxyPoolInput(input ProxyPoolInput) (ProxyPoolInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	if input.Name == "" {
		return ProxyPoolInput{}, app_errors.NewValidationError("proxy name is required")
	}
	if input.URL == "" {
		return ProxyPoolInput{}, app_errors.NewValidationError("proxy URL is required")
	}
	normalizedURL, err := utils.NormalizeProxyURL(input.URL)
	if err != nil {
		return ProxyPoolInput{}, app_errors.NewValidationError(err.Error())
	}
	input.URL = normalizedURL
	return input, nil
}

// List returns all proxy pool items ordered by creation ID.
func (s *ProxyPoolService) List(ctx context.Context) ([]models.ProxyPoolItem, error) {
	items := make([]models.ProxyPoolItem, 0)
	if err := s.ListQuery(ctx).Find(&items).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return items, nil
}

// ListQuery returns the ordered proxy pool list query for handlers that paginate results.
func (s *ProxyPoolService) ListQuery(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).
		Model(&models.ProxyPoolItem{}).
		Select("id", "name", "url", "created_at", "updated_at").
		Order("id ASC")
}

// Create validates and stores a proxy pool item.
func (s *ProxyPoolService) Create(ctx context.Context, input ProxyPoolInput) (*models.ProxyPoolItem, error) {
	cleaned, err := normalizeProxyPoolInput(input)
	if err != nil {
		return nil, err
	}
	item := &models.ProxyPoolItem{Name: cleaned.Name, URL: cleaned.URL}
	if err := s.db.WithContext(ctx).Create(item).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	s.invalidateProxyRuntime()
	return item, nil
}

// Update validates and updates an existing proxy pool item.
func (s *ProxyPoolService) Update(ctx context.Context, id uint, input ProxyPoolInput) (*models.ProxyPoolItem, error) {
	if id == 0 {
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID")
	}
	cleaned, err := normalizeProxyPoolInput(input)
	if err != nil {
		return nil, err
	}
	var item models.ProxyPoolItem
	if err := s.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	nextURL := preserveProxyCredentialsForSameEndpoint(cleaned.URL, item.URL)
	if err := s.db.WithContext(ctx).Model(&item).Updates(map[string]any{
		"name": cleaned.Name,
		"url":  nextURL,
	}).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	item.Name = cleaned.Name
	item.URL = nextURL
	s.invalidateProxyRuntime()
	return &item, nil
}

func preserveProxyCredentialsForSameEndpoint(nextURL, storedURL string) string {
	nextParsed, nextErr := url.Parse(nextURL)
	storedParsed, storedErr := url.Parse(storedURL)
	if nextErr != nil || storedErr != nil || nextParsed.User != nil || storedParsed.User == nil {
		return nextURL
	}

	nextEndpoint := *nextParsed
	nextEndpoint.User = nil
	storedEndpoint := *storedParsed
	storedEndpoint.User = nil
	if nextEndpoint.String() != storedEndpoint.String() {
		return nextURL
	}

	// The UI receives masked URLs; preserve stored credentials when only metadata changed.
	return storedURL
}

// Delete removes a proxy pool item.
func (s *ProxyPoolService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID")
	}
	if err := s.db.WithContext(ctx).Delete(&models.ProxyPoolItem{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	s.invalidateProxyRuntime()
	return nil
}

// ResolveProxyURL converts proxy pool references into runtime proxy URLs.
func (s *ProxyPoolService) ResolveProxyURL(ctx context.Context, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if itemID, ok := utils.ParseProxyPoolItemRef(trimmed); ok {
		var item models.ProxyPoolItem
		if err := s.db.WithContext(ctx).Select("id", "url").First(&item, itemID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", app_errors.NewNotFoundError("proxy pool item not found")
			}
			return "", app_errors.ParseDBError(err)
		}
		return item.URL, nil
	}
	return utils.NormalizeProxyURL(trimmed)
}

// ListSelectionOptions returns manual proxies for proxy selection controls.
func (s *ProxyPoolService) ListSelectionOptions(ctx context.Context) ([]ProxyPoolSelectionOption, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	options := make([]ProxyPoolSelectionOption, 0, len(items))
	for _, item := range items {
		options = append(options, ProxyPoolSelectionOption{
			Type:  "manual",
			Label: item.Name,
			Value: utils.BuildProxyPoolItemRef(item.ID),
			URL:   utils.SanitizeProxyString(item.URL),
		})
	}
	return options, nil
}

func defaultGatewayProxyOptions() []GatewayProxyOption {
	return []GatewayProxyOption{
		{Type: "gateway", Label: "BetterClaude", Value: "betterclaude", CandidateID: "betterclaude-default", URL: "https://betterclau.de"},
		{Type: "gateway", Label: "BetterClaude CF", Value: "betterclaude", CandidateID: "betterclaude-cf", URL: "https://cf.betterclau.de"},
		{Type: "gateway", Label: "BetterClaude JP-01", Value: "betterclaude", CandidateID: "betterclaude-jp-01", URL: "https://jp-01.betterclau.de"},
		{Type: "gateway", Label: "BetterClaude HK-01", Value: "betterclaude", CandidateID: "betterclaude-hk-01", URL: "https://hk-01.betterclau.de"},
		{Type: "gateway", Label: "BetterClaude US-01", Value: "betterclaude", CandidateID: "betterclaude-us-01", URL: "https://us-01.betterclau.de"},
	}
}

// ListGatewayProxyOptions returns built-in API gateway proxy candidates.
func (s *ProxyPoolService) ListGatewayProxyOptions() []GatewayProxyOption {
	options := append([]GatewayProxyOption(nil), s.gatewayProxyOptions...)
	for i := range options {
		options[i].Active = sameGatewayProxyBaseURL(options[i].URL, channel.GatewayProxyBaseURL(options[i].Value))
	}
	return options
}

// TestGatewayProxy verifies a built-in API gateway proxy endpoint with a bounded request.
func (s *ProxyPoolService) TestGatewayProxy(ctx context.Context, candidateID string) (*ProxyPoolTestResult, error) {
	selected, err := s.gatewayProxyOptionByCandidateID(candidateID)
	if err != nil {
		return nil, err
	}
	return s.testGatewayProxyOption(ctx, selected)
}

func (s *ProxyPoolService) gatewayProxyOptionByCandidateID(candidateID string) (GatewayProxyOption, error) {
	trimmed := strings.TrimSpace(candidateID)
	var selected *GatewayProxyOption
	for i := range s.gatewayProxyOptions {
		if s.gatewayProxyOptions[i].CandidateID == trimmed {
			selected = &s.gatewayProxyOptions[i]
			break
		}
	}
	if selected == nil {
		return GatewayProxyOption{}, app_errors.NewValidationError("unsupported gateway proxy")
	}
	return *selected, nil
}

func (s *ProxyPoolService) testGatewayProxyOption(ctx context.Context, selected GatewayProxyOption) (*ProxyPoolTestResult, error) {
	parsed, err := parseGatewayProxyBaseURL(selected.URL)
	if err != nil {
		return nil, err
	}
	timeout := s.gatewayProxyTestTimeout()
	targetAddress := gatewayProxyDialAddress(parsed)
	attempts := s.gatewayProxyTestCount
	if attempts <= 0 {
		attempts = defaultGatewayProxyTestCount
	}
	result := &ProxyPoolTestResult{
		URL:       strings.TrimSpace(selected.URL),
		TargetURL: "tcp://" + targetAddress,
		TimeoutMS: timeout.Milliseconds(),
		Attempts:  attempts,
	}

	var totalDuration int64
	var lastErr string
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 && s.gatewayProxyTestGap > 0 {
			timer := time.NewTimer(s.gatewayProxyTestGap)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				result.Error = utils.TruncateString(utils.SanitizeErrorBody(ctx.Err().Error()), 300)
				return result, nil
			}
		}
		duration, dialErr := dialGatewayProxy(ctx, targetAddress, timeout)
		if dialErr != nil {
			result.FailedAttempts++
			totalDuration += timeout.Milliseconds()
			lastErr = dialErr.Error()
			continue
		}
		result.SuccessfulAttempts++
		totalDuration += duration.Milliseconds()
	}
	result.Success = result.SuccessfulAttempts > 0
	result.DurationMS = totalDuration / int64(attempts)
	if result.Success {
		if result.FailedAttempts > 0 {
			result.Error = fmt.Sprintf("%d/%d gateway TCP checks succeeded; last error: %s", result.SuccessfulAttempts, attempts, utils.TruncateString(utils.SanitizeErrorBody(lastErr), 180))
		}
		return result, nil
	}
	result.Error = fmt.Sprintf("all %d gateway TCP checks failed: %s", attempts, utils.TruncateString(utils.SanitizeErrorBody(lastErr), 180))
	return result, nil
}

func (s *ProxyPoolService) RunGatewayProxyAutoTest(ctx context.Context) []ProxyPoolTestResult {
	options := append([]GatewayProxyOption(nil), s.gatewayProxyOptions...)
	results := make([]ProxyPoolTestResult, len(options))

	var wg sync.WaitGroup
	for i := range options {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := s.testGatewayProxyOption(ctx, options[index])
			if err != nil {
				results[index] = ProxyPoolTestResult{
					Success: false,
					URL:     strings.TrimSpace(options[index].URL),
					Error:   utils.TruncateString(utils.SanitizeErrorBody(err.Error()), 300),
				}
				return
			}
			results[index] = *result
		}(i)
	}
	wg.Wait()

	bestByProvider := make(map[string]ProxyPoolTestResult)
	for _, result := range results {
		if !result.Success {
			continue
		}
		providerID := gatewayProxyProviderIDForURL(options, result.URL)
		if providerID == "" {
			continue
		}
		current, ok := bestByProvider[providerID]
		if !ok || isBetterGatewayProxyResult(result, current) {
			bestByProvider[providerID] = result
		}
	}
	for providerID, result := range bestByProvider {
		channel.SetGatewayProxyBaseURL(providerID, result.URL)
	}
	providerIDs := make(map[string]struct{})
	for _, option := range options {
		if strings.TrimSpace(option.Value) != "" {
			providerIDs[option.Value] = struct{}{}
		}
	}
	for providerID := range providerIDs {
		if _, ok := bestByProvider[providerID]; !ok {
			channel.DisableGatewayProxyBaseURL(providerID)
		}
	}
	return results
}

func dialGatewayProxy(ctx context.Context, targetAddress string, timeout time.Duration) (time.Duration, error) {
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := dialer.DialContext(testCtx, "tcp", targetAddress)
	duration := time.Since(start)
	if err != nil {
		return duration, err
	}
	_ = conn.Close()
	return duration, nil
}

func isBetterGatewayProxyResult(candidate, current ProxyPoolTestResult) bool {
	if candidate.FailedAttempts != current.FailedAttempts {
		return candidate.FailedAttempts < current.FailedAttempts
	}
	return candidate.DurationMS < current.DurationMS
}

// StartGatewayProxyAutoTest starts the background gateway node selector.
func (s *ProxyPoolService) StartGatewayProxyAutoTest() {
	s.gatewayAutoTestMu.Lock()
	defer s.gatewayAutoTestMu.Unlock()
	if s.gatewayAutoTestCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.gatewayAutoTestCancel = cancel
	s.gatewayAutoTestDone = done

	go func() {
		defer close(done)
		s.RunGatewayProxyAutoTest(ctx)
		for {
			interval := s.gatewayProxyAutoTestInterval()
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
				s.RunGatewayProxyAutoTest(ctx)
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
		}
	}()
}

// Stop gracefully stops the gateway auto-test worker.
func (s *ProxyPoolService) Stop(ctx context.Context) {
	s.gatewayAutoTestMu.Lock()
	cancel := s.gatewayAutoTestCancel
	done := s.gatewayAutoTestDone
	s.gatewayAutoTestCancel = nil
	s.gatewayAutoTestDone = nil
	s.gatewayAutoTestMu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *ProxyPoolService) gatewayProxyTestTimeout() time.Duration {
	timeout := defaultProxyPoolTestTimeout
	if s.settingsProvider != nil {
		settings := s.settingsProvider.GetSettings()
		if settings.GatewayProxyTestTimeoutSeconds > 0 {
			timeout = time.Duration(settings.GatewayProxyTestTimeoutSeconds) * time.Second
		}
	}
	if timeout <= 0 {
		timeout = defaultProxyPoolTestTimeout
	}
	return timeout
}

func (s *ProxyPoolService) gatewayProxyAutoTestInterval() time.Duration {
	if s.settingsProvider != nil {
		settings := s.settingsProvider.GetSettings()
		if settings.GatewayProxyAutoTestIntervalMinutes > 0 {
			return time.Duration(settings.GatewayProxyAutoTestIntervalMinutes) * time.Minute
		}
	}
	return time.Hour
}

func parseGatewayProxyBaseURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, app_errors.NewValidationError("invalid gateway proxy URL")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func gatewayProxyDialAddress(parsed *url.URL) string {
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

func sameGatewayProxyBaseURL(left, right string) bool {
	leftURL, leftErr := parseGatewayProxyBaseURL(left)
	rightURL, rightErr := parseGatewayProxyBaseURL(right)
	return leftErr == nil && rightErr == nil && leftURL.String() == rightURL.String()
}

func gatewayProxyProviderIDForURL(options []GatewayProxyOption, rawURL string) string {
	for _, option := range options {
		if sameGatewayProxyBaseURL(option.URL, rawURL) {
			return option.Value
		}
	}
	return ""
}

// Test verifies a stored proxy with a bounded HEAD request through that proxy.
func (s *ProxyPoolService) Test(ctx context.Context, id uint) (*ProxyPoolTestResult, error) {
	if id == 0 {
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID")
	}
	var item models.ProxyPoolItem
	if err := s.db.WithContext(ctx).
		Select("id", "url").
		First(&item, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	result, err := s.testProxyURL(ctx, item.URL)
	if err != nil {
		return nil, err
	}
	if result.Success {
		result.CountryCode, result.CountryName = s.lookupProxyCountry(ctx, item.URL)
	}
	return result, nil
}

func (s *ProxyPoolService) invalidateProxyRuntime() {
	if s.invalidateProxySelection != nil {
		s.invalidateProxySelection()
	}
}

func (s *ProxyPoolService) testProxyURL(ctx context.Context, rawProxyURL string) (*ProxyPoolTestResult, error) {
	normalizedURL, err := utils.NormalizeProxyURL(rawProxyURL)
	if err != nil {
		return nil, app_errors.NewValidationError(err.Error())
	}
	parsedProxyURL, err := url.Parse(normalizedURL)
	if err != nil {
		return nil, app_errors.NewAPIError(app_errors.ErrInternalServer, "failed to parse normalized proxy URL")
	}

	targetURL, timeout := s.healthCheckConfig()
	result := &ProxyPoolTestResult{
		URL:       utils.SanitizeProxyString(normalizedURL),
		TargetURL: targetURL,
		TimeoutMS: timeout.Milliseconds(),
	}

	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsedProxyURL),
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(testCtx, http.MethodHead, targetURL, nil)
	if err != nil {
		return nil, app_errors.NewAPIError(app_errors.ErrInternalServer, "failed to create proxy test request")
	}

	start := time.Now()
	resp, err := client.Do(req)
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = sanitizeProxyTestError(err.Error(), normalizedURL)
		return result, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	result.StatusCode = resp.StatusCode
	result.Success = resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest
	if !result.Success {
		result.Error = fmt.Sprintf("unexpected status code: %d", resp.StatusCode)
	}
	return result, nil
}

func (s *ProxyPoolService) lookupProxyCountry(ctx context.Context, rawProxyURL string) (string, string) {
	if strings.TrimSpace(s.countryLookupURL) == "" {
		return "", ""
	}
	normalizedURL, err := utils.NormalizeProxyURL(rawProxyURL)
	if err != nil {
		return "", ""
	}
	parsedProxyURL, err := url.Parse(normalizedURL)
	if err != nil {
		return "", ""
	}
	_, timeout := s.healthCheckConfig()
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsedProxyURL),
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(testCtx, http.MethodGet, s.countryLookupURL, nil)
	if err != nil {
		return "", ""
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", ""
	}
	var payload struct {
		CountryCodeCamel string `json:"countryCode"`
		CountryCodeSnake string `json:"country_code"`
		Country          string `json:"country"`
		CountryName      string `json:"country_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return "", ""
	}
	countryCode := payload.CountryCodeSnake
	if countryCode == "" {
		countryCode = payload.CountryCodeCamel
	}
	countryName := payload.CountryName
	if countryName == "" && len(strings.TrimSpace(payload.Country)) > 2 {
		countryName = payload.Country
	}
	return strings.ToUpper(strings.TrimSpace(countryCode)), strings.TrimSpace(countryName)
}

func (s *ProxyPoolService) healthCheckConfig() (string, time.Duration) {
	targetURL := s.healthCheckTarget
	timeout := s.healthCheckTimeout

	if s.settingsProvider != nil {
		settings := s.settingsProvider.GetSettings()
		if configuredTarget := strings.TrimSpace(settings.ProxyPoolTestTargetURL); configuredTarget != "" {
			targetURL = configuredTarget
		}
		if settings.ProxyPoolTestTimeoutSeconds > 0 {
			timeout = time.Duration(settings.ProxyPoolTestTimeoutSeconds) * time.Second
		}
	}

	if strings.TrimSpace(targetURL) == "" {
		targetURL = defaultProxyPoolTestTargetURL
	}
	if timeout <= 0 {
		timeout = defaultProxyPoolTestTimeout
	}
	return targetURL, timeout
}

func sanitizeProxyTestError(message string, rawProxyURL string) string {
	sanitizedProxyURL := utils.SanitizeProxyString(rawProxyURL)
	cleaned := strings.ReplaceAll(message, rawProxyURL, sanitizedProxyURL)
	cleaned = strings.ReplaceAll(cleaned, utils.SanitizeErrorBody(rawProxyURL), sanitizedProxyURL)
	if parsed, err := url.Parse(rawProxyURL); err == nil && parsed.User != nil {
		userInfo := parsed.User.String()
		cleaned = strings.ReplaceAll(cleaned, userInfo+"@", "")
		if username := parsed.User.Username(); username != "" {
			cleaned = strings.ReplaceAll(cleaned, username, "[REDACTED]")
		}
		if password, ok := parsed.User.Password(); ok && password != "" {
			cleaned = strings.ReplaceAll(cleaned, password, "[REDACTED]")
		}
	}
	return utils.TruncateString(utils.SanitizeErrorBody(cleaned), 300)
}
