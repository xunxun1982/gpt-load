package sitemanagement

import (
	"context"
	"strings"

	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

type managedSiteProxyURLResolver interface {
	ResolveProxyURL(ctx context.Context, raw string) (string, error)
}

func resolveManagedSiteProxyURL(ctx context.Context, resolver managedSiteProxyURLResolver, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || resolver == nil || !utils.IsProxyPoolRef(trimmed) {
		return trimmed
	}

	resolved, err := resolver.ResolveProxyURL(ctx, trimmed)
	if err != nil {
		logrus.WithError(err).
			WithField("proxy_url", utils.SanitizeProxyString(trimmed)).
			Warn("Failed to resolve managed site proxy pool reference, using direct connection")
		return ""
	}
	return strings.TrimSpace(resolved)
}
