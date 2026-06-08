package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupProxyPoolService(t *testing.T) *ProxyPoolService {
	t.Helper()
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ProxyPoolItem{}))
	return NewProxyPoolService(db)
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
	svc := setupProxyPoolService(t)
	ctx := context.Background()
	const targetURL = "http://proxy-test.invalid/generate_204"
	proxyRequests := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()

	oldTargetURL := proxyPoolTestTargetURL
	oldTimeout := proxyPoolTestTimeout
	proxyPoolTestTargetURL = targetURL
	proxyPoolTestTimeout = 2 * time.Second
	defer func() {
		proxyPoolTestTargetURL = oldTargetURL
		proxyPoolTestTimeout = oldTimeout
	}()

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
	assert.Equal(t, targetURL, <-proxyRequests)
}

func TestProxyPoolServiceTestSanitizesProxyCredentialsInErrors(t *testing.T) {
	svc := setupProxyPoolService(t)
	ctx := context.Background()

	oldTargetURL := proxyPoolTestTargetURL
	oldTimeout := proxyPoolTestTimeout
	proxyPoolTestTargetURL = "http://proxy-test.invalid/generate_204"
	proxyPoolTestTimeout = 200 * time.Millisecond
	defer func() {
		proxyPoolTestTargetURL = oldTargetURL
		proxyPoolTestTimeout = oldTimeout
	}()

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
	assert.True(t, result.TimeoutMS <= int64((200*time.Millisecond).Milliseconds()))
	assert.True(t, strings.TrimSpace(result.Error) != "")
}
