package services

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"gorm.io/gorm"
)

const (
	defaultProxyPoolTestTargetURL = "https://www.gstatic.com/generate_204"
	defaultProxyPoolTestTimeout   = 10 * time.Second
)

// ProxyPoolInput captures editable proxy pool fields.
type ProxyPoolInput struct {
	Name string
	URL  string
}

// ProxyPoolTestResult reports the result of an explicit proxy connectivity test.
type ProxyPoolTestResult struct {
	Success    bool   `json:"success"`
	URL        string `json:"url"`
	TargetURL  string `json:"target_url"`
	TimeoutMS  int64  `json:"timeout_ms"`
	DurationMS int64  `json:"duration_ms"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ProxyPoolService manages reusable upstream proxy URLs.
type ProxyPoolService struct {
	db                 *gorm.DB
	healthCheckTarget  string
	healthCheckTimeout time.Duration
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
	if err := s.db.WithContext(ctx).
		Select("id", "name", "url", "created_at", "updated_at").
		Order("id ASC").
		Find(&items).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return items, nil
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
	if err := s.db.WithContext(ctx).Model(&item).Updates(map[string]any{
		"name": cleaned.Name,
		"url":  cleaned.URL,
	}).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	item.Name = cleaned.Name
	item.URL = cleaned.URL
	return &item, nil
}

// Delete removes a proxy pool item.
func (s *ProxyPoolService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID")
	}
	if err := s.db.WithContext(ctx).Delete(&models.ProxyPoolItem{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	return nil
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
	return s.testProxyURL(ctx, item.URL)
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

	timeout := s.healthCheckTimeout
	if timeout <= 0 {
		timeout = defaultProxyPoolTestTimeout
	}
	targetURL := s.healthCheckTarget
	if strings.TrimSpace(targetURL) == "" {
		targetURL = defaultProxyPoolTestTargetURL
	}
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
