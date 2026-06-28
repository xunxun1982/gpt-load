package sitemanagement

import (
	"context"
	"net/http"
)

type balanceParserKind string

const (
	balanceParserNewAPI  balanceParserKind = "new-api"
	balanceParserSub2API balanceParserKind = "sub2api"
)

// SiteCapabilities keeps provider feature flags in one registry so check-in,
// balance, and future diagnostics do not drift across services.
type SiteCapabilities struct {
	SupportsCheckin bool
	SupportsBalance bool
	BalanceEndpoint string
	balanceParser   balanceParserKind
}

type managedSiteAdapter interface {
	Type() string
	Capabilities() SiteCapabilities
	CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authConfig AuthConfig) (providerResult, error)
}

type registeredSiteAdapter struct {
	siteType     string
	capabilities SiteCapabilities
	provider     checkinProvider
}

func (a registeredSiteAdapter) Type() string {
	return a.siteType
}

func (a registeredSiteAdapter) Capabilities() SiteCapabilities {
	return a.capabilities
}

func (a registeredSiteAdapter) CheckIn(ctx context.Context, client *http.Client, site ManagedSite, authConfig AuthConfig) (providerResult, error) {
	return a.provider.CheckIn(ctx, client, site, authConfig)
}

func resolveManagedSiteAdapter(siteType string) managedSiteAdapter {
	switch siteType {
	case SiteTypeNewAPI, SiteTypeVeloera, SiteTypeOneHub, SiteTypeDoneHub:
		return registeredSiteAdapter{
			siteType: siteType,
			capabilities: SiteCapabilities{
				SupportsCheckin: true,
				SupportsBalance: true,
				BalanceEndpoint: "/api/user/self",
				balanceParser:   balanceParserNewAPI,
			},
			provider: newAPIProvider{},
		}
	case SiteTypeSub2API:
		return registeredSiteAdapter{
			siteType: siteType,
			capabilities: SiteCapabilities{
				SupportsCheckin: true,
				SupportsBalance: true,
				BalanceEndpoint: "/api/v1/user/profile",
				balanceParser:   balanceParserSub2API,
			},
			provider: sub2APIProvider{},
		}
	case SiteTypeWongGongyi:
		return registeredSiteAdapter{
			siteType: siteType,
			capabilities: SiteCapabilities{
				SupportsCheckin: true,
				SupportsBalance: true,
				BalanceEndpoint: "/api/user/self",
				balanceParser:   balanceParserNewAPI,
			},
			provider: wongProvider{},
		}
	case SiteTypeAnyrouter:
		return registeredSiteAdapter{
			siteType: siteType,
			capabilities: SiteCapabilities{
				SupportsCheckin: true,
			},
			provider: anyrouterProvider{},
		}
	default:
		return nil
	}
}

func resolveSiteCapabilities(siteType string) SiteCapabilities {
	adapter := resolveManagedSiteAdapter(siteType)
	if adapter == nil {
		return SiteCapabilities{}
	}
	return adapter.Capabilities()
}
