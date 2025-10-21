package channel

import (
	"context"
	"gpt-load/internal/models"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// UpstreamSelection contains the selected upstream information including dedicated clients.
type UpstreamSelection struct {
	URL          string
	HTTPClient   *http.Client
	StreamClient *http.Client
	ProxyURL     *string // The proxy URL used by this upstream (for logging/observability)
}

// ChannelProxy defines the interface for different API channel proxies.
type ChannelProxy interface {
	// SelectUpstreamWithClients selects an upstream and returns its URL with dedicated HTTP clients.
	// This method should be used instead of BuildUpstreamURL + GetHTTPClient/GetStreamClient
	// to ensure the correct client (with the right proxy) is used for each upstream.
	SelectUpstreamWithClients(originalURL *url.URL, groupName string) (*UpstreamSelection, error)

	// BuildUpstreamURL constructs the target URL for the upstream service.
	// Deprecated: Use SelectUpstreamWithClients instead.
	BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error)

	// IsConfigStale checks if the channel's configuration is stale compared to the provided group.
	IsConfigStale(group *models.Group) bool

	// GetHTTPClient returns the client for standard requests.
	// Deprecated: Use SelectUpstreamWithClients instead to get the upstream-specific client.
	GetHTTPClient() *http.Client

	// GetStreamClient returns the client for streaming requests.
	// Deprecated: Use SelectUpstreamWithClients instead to get the upstream-specific client.
	GetStreamClient() *http.Client

	// ModifyRequest allows the channel to add specific headers or modify the request
	ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group)

	// IsStreamRequest checks if the request is for a streaming response,
	IsStreamRequest(c *gin.Context, bodyBytes []byte) bool

	// ExtractModel extracts the model name from the request.
	ExtractModel(c *gin.Context, bodyBytes []byte) string

	// ValidateKey checks if the given API key is valid.
	ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error)
}
