/// <reference types="vite/client" />

import type { MessageApi, NotificationApi } from "naive-ui";

declare global {
  interface Window {
    $message: MessageApi;
    $notification: NotificationApi;
  }
}
