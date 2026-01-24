import { reactive } from "vue";

interface AppState {
  loading: boolean;
  taskPollingTrigger: number;
  groupDataRefreshTrigger: number;
  syncOperationTrigger: number;
  siteBindingTrigger: number;
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
  forceShowProgressBar: false,
});

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
