package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type proxyPoolSettingsProviderStub struct {
	settings types.SystemSettings
}

func (s proxyPoolSettingsProviderStub) GetSettings() types.SystemSettings {
	return s.settings
}

func setupProxyPoolService(t *testing.T) *ProxyPoolService {
	return setupProxyPoolServiceWithOptions(t)
}

func setupProxyPoolServiceWithOptions(t *testing.T, opts ...ProxyPoolServiceOption) *ProxyPoolService {
	t.Helper()
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.ProxyPoolItem{},
	))
	return NewProxyPoolServiceWithOptions(db, opts...)
}

func TestProxyPoolServiceResolveProxyReferenceUsesManualProxyPoolItem(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	item, err := svc.Create(context.Background(), ProxyPoolInput{
		Name: "manual ref",
		URL:  "http://user:pass@manual.example.com:8080",
	})
	require.NoError(t, err)

	resolved, err := svc.ResolveProxyURL(context.Background(), utils.BuildProxyPoolItemRef(item.ID))
	require.NoError(t, err)
	assert.Equal(t, "http://user:pass@manual.example.com:8080", resolved)
}

func TestProxyPoolServiceSelectionOptionsSanitizeManualProxyURL(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	item, err := svc.Create(context.Background(), ProxyPoolInput{
		Name: "manual secret",
		URL:  "http://user:pass@manual.example.com:8080",
	})
	require.NoError(t, err)

	options, err := svc.ListSelectionOptions(context.Background())
	require.NoError(t, err)
	require.Len(t, options, 1)
	assert.Equal(t, utils.BuildProxyPoolItemRef(item.ID), options[0].Value)
	assert.Equal(t, "http://manual.example.com:8080", options[0].URL)
	assert.NotContains(t, options[0].URL, "user:pass")
}

func TestProxyPoolServiceRejectsSubscriptionReference(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	_, err := svc.ResolveProxyURL(context.Background(), "proxy-subscription:12")
	require.Error(t, err)
}

func TestProxyPoolServiceTestManualProxyStoresCountry(t *testing.T) {
	t.Parallel()

	const targetURL = "http://proxy-test.invalid/generate_204"
	proxyRequests := make(chan string, 2)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- r.URL.String()
		switch r.URL.String() {
		case targetURL:
			w.WriteHeader(http.StatusNoContent)
		case defaultProxyPoolCountryLookupURL:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":      "success",
				"countryCode": "US",
				"country":     "United States",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer proxyServer.Close()

	svc := setupProxyPoolServiceWithOptions(
		t,
		WithProxyPoolHealthCheck(targetURL, 2*time.Second),
	)
	item, err := svc.Create(context.Background(), ProxyPoolInput{
		Name: "country",
		URL:  proxyServer.URL,
	})
	require.NoError(t, err)

	result, err := svc.Test(context.Background(), item.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, "US", result.CountryCode)
	assert.Equal(t, "United States", result.CountryName)

	var stored models.ProxyPoolItem
	require.NoError(t, svc.db.First(&stored, item.ID).Error)
	select {
	case <-proxyRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy health request")
	}
	select {
	case <-proxyRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy country request")
	}
}

func TestProxyPoolServiceCRUD(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, ProxyPoolInput{
		Name: "local socks",
		URL:  " socks5://127.0.0.1:1080 ",
	})
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	assert.Equal(t, "local socks", created.Name)
	assert.Equal(t, "socks5://127.0.0.1:1080", created.URL)

	items, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, created.ID, items[0].ID)

	updated, err := svc.Update(ctx, created.ID, ProxyPoolInput{
		Name: "corp http",
		URL:  "http://proxy.example.com:8080",
	})
	require.NoError(t, err)
	assert.Equal(t, "corp http", updated.Name)
	assert.Equal(t, "http://proxy.example.com:8080", updated.URL)

	require.NoError(t, svc.Delete(ctx, created.ID))
	items, err = svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestProxyPoolServiceUpdateAndDeleteInvalidateRuntimeProxySelection(t *testing.T) {
	t.Parallel()

	var invalidations int
	svc := setupProxyPoolServiceWithOptions(t, WithProxyPoolSelectionInvalidation(func() {
		invalidations++
	}))
	ctx := context.Background()
	created, err := svc.Create(ctx, ProxyPoolInput{
		Name: "runtime proxy",
		URL:  "http://proxy-a.example.com:8080",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, invalidations)

	_, err = svc.Update(ctx, created.ID, ProxyPoolInput{
		Name: "runtime proxy updated",
		URL:  "http://proxy-b.example.com:8080",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, invalidations)

	require.NoError(t, svc.Delete(ctx, created.ID))
	assert.Equal(t, 2, invalidations)
}

func TestProxyPoolServiceUpdatePreservesHiddenCredentialsForSameEndpoint(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, ProxyPoolInput{
		Name: "auth proxy",
		URL:  "http://user:secret@proxy.example.com:8080",
	})
	require.NoError(t, err)

	updated, err := svc.Update(ctx, created.ID, ProxyPoolInput{
		Name: "renamed proxy",
		URL:  "http://proxy.example.com:8080",
	})
	require.NoError(t, err)

	assert.Equal(t, "renamed proxy", updated.Name)
	assert.Equal(t, "http://user:secret@proxy.example.com:8080", updated.URL)
	var stored models.ProxyPoolItem
	require.NoError(t, svc.db.First(&stored, created.ID).Error)
	assert.Equal(t, "http://user:secret@proxy.example.com:8080", stored.URL)
}

func TestProxyPoolServiceRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, ProxyPoolInput{
		Name: "shared proxy",
		URL:  "http://proxy-a.example.com:8080",
	})
	require.NoError(t, err)

	_, err = svc.Create(ctx, ProxyPoolInput{
		Name: "shared proxy",
		URL:  "http://proxy-b.example.com:8080",
	})
	var apiErr *app_errors.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, app_errors.ErrDuplicateResource.Code, apiErr.Code)
}

func TestProxyPoolServiceRejectsDuplicateNamesOnUpdate(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, ProxyPoolInput{
		Name: "first proxy",
		URL:  "http://proxy-a.example.com:8080",
	})
	require.NoError(t, err)
	second, err := svc.Create(ctx, ProxyPoolInput{
		Name: "second proxy",
		URL:  "http://proxy-b.example.com:8080",
	})
	require.NoError(t, err)

	_, err = svc.Update(ctx, second.ID, ProxyPoolInput{
		Name: "first proxy",
		URL:  "http://proxy-c.example.com:8080",
	})
	var apiErr *app_errors.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, app_errors.ErrDuplicateResource.Code, apiErr.Code)
}

func TestProxyPoolServiceRejectsUnsupportedSchemes(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	_, err := svc.Create(context.Background(), ProxyPoolInput{
		Name: "ftp",
		URL:  "ftp://proxy.example.com:21",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported proxy scheme")
}

func TestProxyPoolServiceReturnsTypedErrors(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)

	_, err := svc.Create(context.Background(), ProxyPoolInput{
		Name: "ftp",
		URL:  "ftp://proxy.example.com:21",
	})
	var apiErr *app_errors.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, app_errors.ErrValidation.Code, apiErr.Code)

	_, err = svc.Update(context.Background(), 404, ProxyPoolInput{
		Name: "missing",
		URL:  "http://proxy.example.com:8080",
	})
	apiErr = nil
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, app_errors.ErrResourceNotFound.Code, apiErr.Code)
}

func TestProxyPoolServiceAllowsHTTPAndSocks(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolService(t)
	validURLs := []string{
		"http://proxy.example.com:8080",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
	}

	for _, rawURL := range validURLs {
		_, err := svc.Create(context.Background(), ProxyPoolInput{
			Name: rawURL,
			URL:  rawURL,
		})
		require.NoError(t, err, rawURL)
	}
}

func TestProxyPoolServiceTestUsesConfiguredProxy(t *testing.T) {
	t.Parallel()

	const targetURL = "http://proxy-test.invalid/generate_204"
	svc := setupProxyPoolServiceWithOptions(t, WithProxyPoolHealthCheck(targetURL, 2*time.Second))
	ctx := context.Background()
	proxyRequests := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()

	item, err := svc.Create(ctx, ProxyPoolInput{
		Name: "local proxy",
		URL:  proxyServer.URL,
	})
	require.NoError(t, err)

	result, err := svc.Test(ctx, item.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, targetURL, result.TargetURL)
	assert.Equal(t, int64(2000), result.TimeoutMS)
	assert.Equal(t, http.StatusNoContent, result.StatusCode)
	assert.GreaterOrEqual(t, result.DurationMS, int64(0))
	assert.Equal(t, proxyServer.URL, result.URL)
	select {
	case got := <-proxyRequests:
		assert.Equal(t, targetURL, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy request")
	}
}

func TestProxyPoolServiceTestUsesSettingsHealthCheckConfig(t *testing.T) {
	t.Parallel()

	const targetURL = "http://settings-target.invalid/generate_204"
	svc := setupProxyPoolServiceWithOptions(t, WithProxyPoolSettingsProvider(proxyPoolSettingsProviderStub{
		settings: types.SystemSettings{
			ProxyPoolTestTargetURL:           targetURL,
			ProxyPoolTestTimeoutSeconds:      1,
			ProxyPoolAutoTestIntervalMinutes: 60,
		},
	}))
	ctx := context.Background()
	proxyRequests := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()

	item, err := svc.Create(ctx, ProxyPoolInput{
		Name: "settings proxy",
		URL:  proxyServer.URL,
	})
	require.NoError(t, err)

	result, err := svc.Test(ctx, item.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, targetURL, result.TargetURL)
	assert.Equal(t, int64(1000), result.TimeoutMS)
	select {
	case got := <-proxyRequests:
		assert.Equal(t, targetURL, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy request")
	}
}

func TestProxyPoolServiceTestSanitizesProxyCredentialsInErrors(t *testing.T) {
	t.Parallel()

	svc := setupProxyPoolServiceWithOptions(t, WithProxyPoolHealthCheck("http://proxy-test.invalid/generate_204", 200*time.Millisecond))
	ctx := context.Background()

	item, err := svc.Create(ctx, ProxyPoolInput{
		Name: "credential proxy",
		URL:  "http://proxy-user:proxy-pass@127.0.0.1:1",
	})
	require.NoError(t, err)

	result, err := svc.Test(ctx, item.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.URL, "127.0.0.1:1")
	assert.NotContains(t, result.URL, "proxy-user")
	assert.NotContains(t, result.URL, "proxy-pass")
	assert.NotContains(t, result.Error, "proxy-user")
	assert.NotContains(t, result.Error, "proxy-pass")
	assert.NotContains(t, result.Error, "proxy-user:proxy-pass@")
	assert.LessOrEqual(t, result.TimeoutMS, int64((200 * time.Millisecond).Milliseconds()))
	assert.NotEmpty(t, strings.TrimSpace(result.Error))
}
