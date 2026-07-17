export function hasImpreciseManagedSiteBalanceMultiplier(value: unknown): boolean {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }

  const sites = (value as { sites?: unknown }).sites;
  if (!Array.isArray(sites)) {
    return false;
  }

  return sites.some(site => {
    if (site === null || typeof site !== "object" || Array.isArray(site)) {
      return false;
    }
    const multiplier = (site as Record<string, unknown>).balance_multiplier;
    // Legacy zero normalizes to 1 and negative rows are skipped; only positive values can be persisted.
    return typeof multiplier === "number" && multiplier > 0 && !Number.isSafeInteger(multiplier);
  });
}
