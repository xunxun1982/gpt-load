package sitemanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveManagedSiteAdapterCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		siteType         string
		wantAdapter      bool
		wantCheckin      bool
		wantBalance      bool
		wantBalanceRoute string
	}{
		{
			name:             "new api compatible",
			siteType:         SiteTypeNewAPI,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/user/self",
		},
		{
			name:             "legacy veloera compatible",
			siteType:         SiteTypeVeloera,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/user/self",
		},
		{
			name:             "one hub compatible",
			siteType:         SiteTypeOneHub,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/user/self",
		},
		{
			name:             "done hub compatible",
			siteType:         SiteTypeDoneHub,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/user/self",
		},
		{
			name:             "sub2api",
			siteType:         SiteTypeSub2API,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/v1/user/profile",
		},
		{
			name:             "wong gongyi compatible",
			siteType:         SiteTypeWongGongyi,
			wantAdapter:      true,
			wantCheckin:      true,
			wantBalance:      true,
			wantBalanceRoute: "/api/user/self",
		},
		{
			name:        "anyrouter checkin only",
			siteType:    SiteTypeAnyrouter,
			wantAdapter: true,
			wantCheckin: true,
			wantBalance: false,
		},
		{
			name:        "brand has no managed adapter",
			siteType:    SiteTypeBrand,
			wantAdapter: false,
		},
		{
			name:        "unknown has no managed adapter",
			siteType:    SiteTypeUnknown,
			wantAdapter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := resolveManagedSiteAdapter(tt.siteType)
			capabilities := resolveSiteCapabilities(tt.siteType)
			if !tt.wantAdapter {
				assert.Nil(t, adapter)
				assert.False(t, capabilities.SupportsCheckin)
				assert.False(t, capabilities.SupportsBalance)
				assert.Empty(t, capabilities.BalanceEndpoint)
				return
			}

			require.NotNil(t, adapter)
			assert.Equal(t, tt.siteType, adapter.Type())
			assert.Equal(t, tt.wantCheckin, capabilities.SupportsCheckin)
			assert.Equal(t, tt.wantBalance, capabilities.SupportsBalance)
			assert.Equal(t, tt.wantBalanceRoute, capabilities.BalanceEndpoint)
			assert.Equal(t, capabilities, adapter.Capabilities())
		})
	}
}
