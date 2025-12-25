/**
 * Site Management i18n - Japanese
 */
export default {
  title: "サイト一覧",
  subtitle: "サイトの名前、メモ、説明、URL、自動チェックインを管理",

  // Section titles
  basicInfo: "基本情報",
  checkinSettings: "チェックイン設定",
  authSettings: "認証設定",

  // Basic fields
  name: "名前",
  namePlaceholder: "サイト名を入力",
  notes: "メモ",
  notesPlaceholder: "メモを入力",
  description: "説明",
  descriptionPlaceholder: "サイトの説明を入力",
  sort: "並び順",
  sortTooltip: "数字が小さいほど上に表示",
  baseUrl: "サイトURL",
  baseUrlPlaceholder: "https://example.com",
  siteType: "サイト種別",
  enabled: "有効",
  userId: "ユーザーID",
  userIdPlaceholder: "ユーザーIDを入力",
  userIdTooltip: "チェックインリクエストに使用するユーザー識別子",

  // Check-in related
  checkinPageUrl: "サインイン",
  checkinPageUrlPlaceholder: "https://example.com/checkin",
  checkinPageUrlTooltip: "チェックインページの完全なURL",
  customCheckinUrl: "サインインAPI",
  customCheckinUrlPlaceholder: "/api/user/checkin",
  customCheckinUrlTooltip: "カスタムチェックインAPIパス、空欄でデフォルト使用",
  checkinEnabled: "サインイン",
  checkinEnabledTooltip: "このサイトのチェックイン操作を許可",
  autoCheckinEnabled: "自動サインイン",
  autoCheckinEnabledTooltip: "設定した時間帯に自動でチェックイン",

  // Auth related
  authType: "認証方式",
  authValue: "認証情報",
  authValuePlaceholder: "トークンまたはCookieを入力",
  authValueEditHint: "空欄で既存の認証情報を維持",
  authTypeNone: "なし",
  authTypeAccessToken: "アクセストークン",
  authTypeCookie: "Cookie",
  hasAuth: "認証設定済み",
  noAuth: "認証なし",

  // Site types
  siteTypeOther: "その他",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeWong: "Wong公益站",
  siteTypeAnyrouter: "AnyRouter",

  // Status
  lastStatus: "最新ステータス",
  status: "ステータス",
  statusSuccess: "チェックイン成功",
  statusAlreadyChecked: "チェックイン済み",
  statusFailed: "チェックイン失敗",
  statusSkipped: "スキップ",
  statusNone: "未チェックイン",
  lastCheckinAt: "最終チェックイン時刻",
  lastCheckinMessage: "チェックインメッセージ",

  // Actions
  checkin: "チェックイン",
  checkinNow: "今すぐチェックイン",
  logs: "ログ",
  viewLogs: "ログを表示",
  openSite: "サイトを開く",
  openCheckinPage: "チェックインページを開く",
  deleteSite: "サイトを削除",
  confirmDeleteSite: "サイト「{name}」を削除しますか？関連するチェックインログも削除されます。",
  enterSiteNameToConfirm: "サイト名を入力して確認",
  dangerousDeleteWarning: "これは危険な操作です。サイト ",
  toConfirmDeletion:
    " とすべてのチェックインログが削除されます。サイト名を入力して確認してください：",
  enterSiteName: "サイト名を入力",
  confirmDelete: "削除を確認",
  incorrectSiteName: "サイト名が正しくありません",

  // Logs
  logTime: "時刻",
  logStatus: "ステータス",
  logMessage: "メッセージ",
  noLogs: "チェックインログなし",

  // Auto check-in config
  autoCheckin: "自動チェックイン",
  autoCheckinConfig: "自動チェックイン設定",
  config: "設定",
  globalEnabled: "グローバル有効",
  globalEnabledTooltip: "無効にすると全ての自動チェックインが停止",
  windowStart: "開始時刻",
  windowEnd: "終了時刻",
  windowTooltip: "この時間帯内でランダムに実行",
  scheduleMode: "スケジュールモード",
  scheduleModeRandom: "ランダム",
  scheduleModeDeterministic: "固定時刻",
  scheduleModeTooltip: "ランダムモードは時間帯内でランダムに実行",
  deterministicTime: "固定実行時刻",
  deterministicTimeTooltip: "毎日この時刻にチェックインを実行",

  // Retry strategy
  retryStrategy: "リトライ戦略",
  retryEnabled: "リトライ有効",
  retryEnabledTooltip: "チェックイン失敗時に自動リトライ",
  retryInterval: "リトライ間隔（分）",
  retryIntervalTooltip: "リトライ間の待機時間",
  retryMaxAttempts: "1日の最大試行回数",
  retryMaxAttemptsTooltip: "1日あたりの最大リトライ回数",

  // Status display
  statusRunning: "実行中",
  statusNext: "次回実行",
  statusLastRun: "前回実行",
  statusLastResult: "前回結果",
  statusPendingRetry: "リトライ待ち",
  statusAttempts: "本日の試行回数",

  // Summary
  summaryTotal: "合計サイト",
  summaryExecuted: "実行済み",
  summarySuccess: "成功",
  summaryFailed: "失敗",
  summarySkipped: "スキップ",

  // Statistics
  statsTotal: "合計",
  statsEnabled: "有効",
  statsDisabled: "無効",
  statsAutoCheckin: "自動チェックイン",

  // Actions
  runNow: "今すぐ実行",
  autoCheckinTriggered: "自動チェックインタスクを開始しました",

  // Messages
  checkinSuccess: "チェックイン成功",
  checkinFailed: "チェックイン失敗",
  siteCreated: "サイトを作成しました",
  siteUpdated: "サイトを更新しました",
  siteDeleted: "サイトを削除しました",
  configSaved: "設定を保存しました",

  // Import/Export
  exportEncrypted: "暗号化エクスポート",
  exportPlain: "平文エクスポート",
  exportSuccess: "エクスポート成功",
  importSuccess: "インポート成功：{imported}/{total} サイト",
  importInvalidFormat: "インポートファイル形式が無効です",
  importInvalidJSON: "JSON形式が無効です",

  // Validation
  nameRequired: "サイト名を入力してください",
  nameDuplicate: "サイト名「{name}」は既に存在します",
  baseUrlRequired: "サイトURLを入力してください",
  invalidBaseUrl: "サイトURLの形式が正しくありません",
  invalidTimeFormat: "時刻の形式が正しくありません。HH:mm形式で入力してください",
};
