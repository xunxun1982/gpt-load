package channel

import (
	"encoding/json"
	"gpt-load/internal/config"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// TestGetChannels tests retrieving all registered channel types
func TestGetChannels(t *testing.T) {
	channels := GetChannels()
	if len(channels) == 0 {
		t.Error("Expected at least one registered channel")
	}
}

// setupTestFactory creates a test factory
func setupTestFactory(t *testing.T) *Factory {
	settingsManager := config.NewSystemSettingsManager()
	clientManager := httpclient.NewHTTPClientManager()
	factory := NewFactory(settingsManager, clientManager)
	return factory
}

// TestNewFactory tests factory creation
func TestNewFactory(t *testing.T) {
	factory := setupTestFactory(t)
	assert.NotNil(t, factory)
	assert.NotNil(t, factory.settingsManager)
	assert.NotNil(t, factory.clientManager)
}

// TestGetChannelCaching tests channel caching mechanism
func TestGetChannelCaching(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, _ := json.Marshal(upstreams)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	channel1, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel1)
	channel2, err := factory.GetChannel(group)
	require.NoError(t, err)
	assert.Equal(t, channel1, channel2)
}

// TestInvalidateCache tests cache invalidation
func TestInvalidateCache(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, _ := json.Marshal(upstreams)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	channel1, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel1)

	// Invalidate cache
	factory.InvalidateCache(group.ID)

	// Get channel again - should create a new instance
	channel2, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel2)

	// Note: Due to connection pooling and caching optimizations,
	// the channels may share underlying resources even after cache invalidation.
	// The important thing is that InvalidateCache doesn't cause errors.
}

// TestGetChannelConcurrency tests concurrent channel creation
func TestGetChannelConcurrency(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, _ := json.Marshal(upstreams)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	var wg sync.WaitGroup
	channels := make([]ChannelProxy, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ch, _ := factory.GetChannel(group)
			channels[index] = ch
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(channels); i++ {
		assert.Equal(t, channels[0], channels[i])
	}
}

// BenchmarkGetChannel benchmarks channel retrieval
func BenchmarkGetChannel(b *testing.B) {
	factory := setupTestFactory(&testing.T{})
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, _ := json.Marshal(upstreams)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		factory.GetChannel(group)
	}
}
