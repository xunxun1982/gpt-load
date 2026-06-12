package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"gorm.io/gorm"
)

const (
	defaultProxyPoolTestTargetURL = "https://www.gstatic.com/generate_204"
	defaultProxyPoolTestTimeout   = 10 * time.Second

	defaultProxyPoolCountryLookupURL = "http://ip-api.com/json/?fields=status,country,countryCode,query"
)

// ProxyPoolInput captures editable proxy pool fields.
type ProxyPoolInput struct {
	Name string
	URL  string
}

// ProxyPoolTestResult reports the result of an explicit proxy connectivity test.
type ProxyPoolTestResult struct {
	Success     bool   `json:"success"`
	URL         string `json:"url"`
	TargetURL   string `json:"target_url"`
	TimeoutMS   int64  `json:"timeout_ms"`
	DurationMS  int64  `json:"duration_ms"`
	StatusCode  int    `json:"status_code,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	CountryName string `json:"country_name,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ProxyPoolSelectionOption is used by proxy selectors outside the proxy pool page.
type ProxyPoolSelectionOption struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
	URL   string `json:"url,omitempty"`
}

// ProxyPoolService manages reusable upstream proxy URLs.
type ProxyPoolService struct {
	db                       *gorm.DB
	healthCheckTarget        string
	healthCheckTimeout       time.Duration
	countryLookupURL         string
	settingsProvider         ProxyPoolSettingsProvider
	invalidateProxySelection func()
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
		db:                 db,
		healthCheckTarget:  defaultProxyPoolTestTargetURL,
		healthCheckTimeout: defaultProxyPoolTestTimeout,
		countryLookupURL:   defaultProxyPoolCountryLookupURL,
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
			if err == gorm.ErrRecordNotFound {
				return "", nil
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
