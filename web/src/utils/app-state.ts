import type { ManagedSiteCheckinStatus } from "@/api/site-management";
import { reactive } from "vue";

interface SiteSnapshotInput {
  id: number;
  last_balance?: string | null;
  last_checkin_status?: ManagedSiteCheckinStatus;
}

interface AppState {
  loading: boolean;
  taskPollingTrigger: number;
  groupDataRefreshTrigger: number;
  syncOperationTrigger: number;
  siteBindingTrigger: number;
  siteBalances: Record<number, string | null>;
  siteCheckinStatuses: Record<number, ManagedSiteCheckinStatus>;
  // Direct control for progress bar visibility
  forceShowProgressBar: boolean;
  progressBarGroupName?: string;
  lastCompletedTask?: {
    groupName: string;
    taskType: string;
    finishedAt: string;
  };
  lastSyncOperation?: {
    groupName: string;
    operationType: string;
    finishedAt: string;
  };
}

export const appState = reactive<AppState>({
  loading: false,
  taskPollingTrigger: 0,
  groupDataRefreshTrigger: 0,
  syncOperationTrigger: 0,
  siteBindingTrigger: 0,
  siteBalances: {},
  siteCheckinStatuses: {},
  forceShowProgressBar: false,
  progressBarGroupName: undefined,
});

let siteBalanceRevision = 0;

// Show progress bar immediately for import/delete operations
export function showProgressBar(groupName?: string) {
  appState.forceShowProgressBar = true;
  appState.progressBarGroupName = groupName;
  appState.taskPollingTrigger++;
}

// Hide progress bar
export function hideProgressBar() {
  appState.forceShowProgressBar = false;
  appState.progressBarGroupName = undefined;
}

// Trigger data refresh after a sync operation completes
export function triggerSyncOperationRefresh(groupName: string, operationType: string) {
  appState.lastSyncOperation = {
    groupName,
    operationType,
    finishedAt: new Date().toISOString(),
  };
  appState.syncOperationTrigger++;
}

// Trigger site list refresh after binding/unbinding
export function triggerSiteBindingRefresh() {
  appState.siteBindingTrigger++;
}

function normalizeSiteBalance(balance: string | null | undefined): string | null {
  const value = balance?.trim();
  return value ? value : null;
}

export function getSiteBalanceRevision() {
  return siteBalanceRevision;
}

export function replaceSiteBalances(sites: SiteSnapshotInput[], expectedRevision?: number) {
  if (expectedRevision !== undefined && siteBalanceRevision !== expectedRevision) {
    return false;
  }

  const balances: Record<number, string | null> = {};
  const checkinStatuses: Record<number, ManagedSiteCheckinStatus> = {};
  for (const site of sites) {
    balances[site.id] = normalizeSiteBalance(site.last_balance);
    checkinStatuses[site.id] = site.last_checkin_status ?? "";
  }
  appState.siteBalances = balances;
  appState.siteCheckinStatuses = checkinStatuses;
  siteBalanceRevision++;
  return true;
}

export function updateSiteBalances(sites: SiteSnapshotInput[], expectedRevision?: number) {
  if (expectedRevision !== undefined && siteBalanceRevision !== expectedRevision) {
    return false;
  }

  for (const site of sites) {
    appState.siteBalances[site.id] = normalizeSiteBalance(site.last_balance);
    // Balance-only refreshes do not carry authoritative check-in state, so keep the cached status.
    if (site.last_checkin_status !== undefined) {
      appState.siteCheckinStatuses[site.id] = site.last_checkin_status;
    }
  }
  if (sites.length > 0) {
    siteBalanceRevision++;
  }
  return true;
}

export function updateSiteBalance(siteId: number, balance: string | null | undefined) {
  appState.siteBalances[siteId] = normalizeSiteBalance(balance);
  siteBalanceRevision++;
}
