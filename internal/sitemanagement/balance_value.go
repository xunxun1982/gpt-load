package sitemanagement

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const defaultManagedSiteBalanceMultiplier int64 = 1

func normalizeManagedSiteBalanceMultiplier(multiplier int64) int64 {
	if multiplier < defaultManagedSiteBalanceMultiplier {
		return defaultManagedSiteBalanceMultiplier
	}
	return multiplier
}

// scaledManagedSiteBalance is the single display boundary for cached upstream balances.
func scaledManagedSiteBalance(balance string, multiplier int64) string {
	multiplier = normalizeManagedSiteBalanceMultiplier(multiplier)
	if balance == "" || multiplier == defaultManagedSiteBalanceMultiplier {
		return balance
	}

	value := strings.TrimSpace(balance)
	if !strings.HasPrefix(value, "$") {
		return balance
	}
	// Provider parsers already persist canonical "$%.2f" display strings; preserve unknown legacy formats.
	amount, err := strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return balance
	}
	scaled := amount / float64(multiplier)
	if scaled == 0 {
		scaled = 0 // Avoid rendering a negative zero after dividing a small negative balance.
	}
	return fmt.Sprintf("$%.2f", scaled)
}

func scaleManagedSiteBalanceInfo(info *BalanceInfo, multiplier int64) {
	if info == nil || info.Balance == nil {
		return
	}
	scaled := scaledManagedSiteBalance(*info.Balance, multiplier)
	info.Balance = &scaled
}

func scaleManagedSiteBalanceResults(results map[uint]*BalanceInfo, sites []ManagedSite) {
	for i := range sites {
		scaleManagedSiteBalanceInfo(results[sites[i].ID], sites[i].BalanceMultiplier)
	}
}
