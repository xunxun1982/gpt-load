/**
 * Site Management i18n - Japanese
 */
export default {
  title: "サイト一覧",
  subtitle: "サイトの名前、メモ、説明、URL、チェックインを管理",

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
  checkinAvailable: "チェックイン可能",
  checkinAvailableTooltip: "このサイトがチェックインをサポートしているかどうか",
  checkinEnabled: "サインイン",
  checkinEnabledTooltip: "このサイトのチェックイン操作を許可",

  // Auth related
  authType: "認証方式",
  authValue: "認証情報",
  authValuePlaceholder: "アクセストークンを入力",
  authValueEditHint: "空欄で既存の認証情報を維持",
  authTypeNone: "なし",
  authTypeAccessToken: "アクセストークン",
  hasAuth: "認証設定済み",
  noAuth: "認証なし",

  // Site types
  siteTypeOther: "その他",
  siteTypeBrand: "ブランド",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong公益站",

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
  copySite: "サイトをコピー",
  siteCopied: "サイトをコピーしました",
  deleteSite: "サイトを削除",
  confirmDeleteSite: "サイト「{name}」を削除しますか？関連するチェックインログも削除されます。",
  dangerousDeleteWarning: "これは危険な操作です。サイト ",
  toConfirmDeletion:
    " とすべてのチェックインログが削除されます。確認のためサイト名を入力してください：",
  enterSiteName: "サイト名を入力",
  confirmDelete: "削除を確認",
  incorrectSiteName: "サイト名が正しくありません",
  siteHasBinding:
    "サイト「{name}」はグループ「{groupName}」にバインドされています。削除する前にバインドを解除してください。",
  mustUnbindFirst: "先にバインド解除",

  // Logs
  logTime: "時刻",
  logStatus: "ステータス",
  logMessage: "メッセージ",
  noLogs: "チェックインログなし",

  // Statistics
  statsTotal: "合計",
  statsEnabled: "有効",
  statsDisabled: "無効",
  statsCheckinAvailable: "チェックイン可",

  // Filter & Search
  filterCheckinAvailable: "チェックイン可能のみ表示",
  searchPlaceholder: "名前、URL、メモを検索...",

  // Messages
  checkinSuccess: "チェックイン成功",
  checkinFailed: "チェックイン失敗",
  siteCreated: "サイトを作成しました",
  siteUpdated: "サイトを更新しました",
  siteDeleted: "サイトを削除しました",

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

  // Bulk delete
  deleteAllUnbound: "すべて削除",
  deleteAllUnboundTooltip: "グループにバインドされていないすべてのサイトを削除",
  confirmDeleteAllUnbound: "バインドされていないすべてのサイトを削除しますか？",
  deleteAllUnboundWarning:
    "これは危険な操作です。グループにバインドされていないすべてのサイト（{count}件）とそのチェックインログが削除されます。確認のため ",
  deleteAllUnboundConfirmText: "DELETE",
  deleteAllUnboundPlaceholder: "DELETEと入力して確認",
  incorrectConfirmText: "確認テキストが正しくありません",
  noUnboundSites: "削除するバインドされていないサイトがありません",
};
