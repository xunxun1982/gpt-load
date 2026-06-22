package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"
	"strings"
)

// ValidationTrace captures the actual upstream validation request for manual key-test logs.
type ValidationTrace struct {
	RequestPath          string
	UpstreamAddr         string
	UpstreamUserAgent    string
	RequestBody          string
	ResponseBody         string
	EstimatedInputTokens int64
	ReportedTokenUsage   tokenusage.Usage
	HasReportedUsage     bool
	ResponseError        bool
}

// KeyValidationTracer is implemented by channels that can return request details for manual validation logs.
type KeyValidationTracer interface {
	ValidateKeyWithTrace(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, *ValidationTrace, error)
}

func validationTraceFromRequest(req *http.Request, requestBody []byte, upstreamAddr string) *ValidationTrace {
	trace := &ValidationTrace{
		UpstreamAddr:         utils.TruncateString(utils.SanitizeRequestURLForLog(upstreamAddr), 500),
		RequestBody:          utils.TruncateString(utils.SanitizeErrorBody(string(requestBody)), 2000),
		EstimatedInputTokens: int64(utils.EstimateTokensFromBytes(requestBody)),
	}
	if req != nil && req.URL != nil {
		trace.UpstreamUserAgent = validationRequestUserAgent(req)
		requestURL := &url.URL{Path: req.URL.Path, RawQuery: req.URL.RawQuery}
		trace.RequestPath = utils.TruncateString(utils.SanitizeURLForLog(requestURL), 500)
	}
	return trace
}

func validationRequestUserAgent(req *http.Request) string {
	if req == nil {
		return ""
	}
	userAgent := req.UserAgent()
	if userAgent == "" {
		// net/http adds this default when no User-Agent header is set.
		userAgent = "Go-http-client/1.1"
	}
	return utils.TruncateString(utils.SanitizeErrorBody(userAgent), 512)
}

func validationUpstreamAddr(selection *UpstreamSelection) string {
	if selection == nil {
		return ""
	}
	upstreamAddr := selection.URL
	if selection.ProxyURL != nil && strings.TrimSpace(*selection.ProxyURL) != "" {
		upstreamAddr += " (via manual proxy: " + utils.SanitizeProxyString(*selection.ProxyURL) + ")"
	}
	if strings.TrimSpace(selection.GatewayProxy) != "" {
		upstreamAddr += " (via gateway: " + utils.SanitizeProxyString(selection.GatewayProxy) + ")"
	}
	return upstreamAddr
}

func validateKeyResponseStatusWithTrace(resp *http.Response, trace *ValidationTrace) (bool, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, invalidValidationStatusErrorWithTrace(resp, trace)
	}
	body, err := parseValidationSuccessResponse(resp)
	if trace != nil {
		if usage, ok := tokenusage.FromResponseBody(body); ok {
			trace.ReportedTokenUsage = usage.Normalize()
			trace.HasReportedUsage = true
		}
		trace.ResponseBody = utils.TruncateString(utils.SanitizeErrorBody(string(body)), 2000)
	}
	if err != nil {
		return false, fmt.Errorf("validation response is not readable: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, fmt.Errorf("validation response is empty")
	}
	if !json.Valid(body) && !looksLikeSSEValidationResponse(body) {
		return false, fmt.Errorf("validation response is not valid JSON or SSE")
	}
	return true, nil
}

func invalidValidationStatusErrorWithTrace(resp *http.Response, trace *ValidationTrace) error {
	parsedError, err := parseValidationErrorResponseWithBody(resp)
	if trace != nil {
		trace.ResponseError = true
		trace.ResponseBody = utils.TruncateString(utils.SanitizeErrorBody(parsedError.body), 2000)
	}
	if err != nil {
		return fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError.message)
}
