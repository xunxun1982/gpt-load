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
  reorderSort: "並び順番号を再設定",
  reorderSortTitle: "並び順番号を再設定",
  reorderSortTooltip: "現在の全体順で全サイトの並び順番号を更新",
  reorderStart: "開始値",
  reorderStep: "間隔",
  reorderPreview: "現在の全体順で全サイトの並び順番号を更新します。",
  reorderInvalidInput: "有効な開始値と間隔を入力してください",
  reorderSortSuccess: "並び順番号を更新しました",
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
  autoCheckinEnabled: "自動サインイン",

  // Proxy settings
  useProxy: "プロキシ使用",
  proxyUrl: "プロキシプール",
  proxyUrlPlaceholder: "プロキシなし",
  proxyUrlTooltip: "チェックインリクエストに使用する手動プロキシプール",
  proxyManualProxy: "手動プロキシ",

  // Bypass settings
  bypassMethod: "バイパス方式",
  bypassMethodNone: "なし",
  bypassMethodStealth: "ステルス (TLS指紋)",
  stealthBypassHint: "⚠️ ステルスバイパスにはCookie認証が必要です",
  stealthCookieHint:
    "💡 ブラウザからWAF Cookie（cf_clearance、acw_tc、cdn_sec_tc、acw_sc__v2など）を含めてください",
  stealthRequiresCookieAuth: "ステルスバイパスにはCookie認証が必要です",
  stealthRequiresCookieValue: "ステルスバイパスにはCookie値が必要です",
  missingCFCookies: "ブラウザWAF Cookieが不足しています。次のいずれかが必要：{cookies}",
  maxTwoAuthTypes: "認証方式は最大2つまで選択できます",

  // Auth related
  authType: "認証方式",
  authTypePlaceholder: "認証方式を選択（複数選択可）",
  authValue: "認証情報",
  authValuePlaceholder: "アクセストークンを入力",
  authValueEditHint: "空欄で既存の認証情報を維持",
  authTypeNone: "なし",
  authTypeAccessToken: "アクセストークン",
  sub2ApiRefreshToken: "リフレッシュトークン",
  sub2ApiRefreshTokenPlaceholder: "refresh_tokenを入力",
  authTypeCookie: "Cookie",
  authTypeCookiePlaceholder: "session=xxx; token=xxx; cf_clearance=xxx",
  authTypeCookieHint:
    "ブラウザからCookieを取得してください。session/tokenなどのフィールドを含めます。サイトがブラウザ保護を使用している場合は、cf_clearanceやacw_tcなどのWAF Cookieも含めてください。",
  sub2ApiAuthHint:
    "Sub2APIはアクセストークンを選択します。Access TokenにはApplication/Local Storageの現在のドメインにあるauth_tokenを入力し、Refresh Tokenには同じ場所のrefresh_tokenを入力します。期限切れ時に自動更新できるよう、両方の入力を推奨します。ユーザーIDは空欄にします。",
  anyrouterAuthHint:
    "AnyRouterはCookieを選択します。ログイン後、Networkで https://<AnyRouterドメイン>/api/user/sign_in リクエストを探し、Request HeadersのCookie全体をコピーします。ユーザーIDは空欄にします。",
  multiAuthHint:
    "複数の認証方式が選択されています。チェックイン時はまずアクセストークンを試し、失敗した場合はCookieを試します。いずれかが成功すればチェックイン成功となります。",
  hasAuth: "認証設定済み",
  noAuth: "認証なし",

  // Site types
  siteTypeOther: "その他",
  siteTypeBrand: "ブランド",
  siteTypeNewApi: "New API",
  siteTypeSub2Api: "Sub2API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong公益站",
  siteTypeAnyrouter: "Anyrouter",

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
  openSiteVisited: "サイトを開く (本日訪問済み)",
  openCheckinPage: "チェックインページを開く",
  openCheckinPageVisited: "チェックインページを開く (本日訪問済み)",
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
  siteHasBindings:
    "サイト「{name}」は {count} 個のグループ（{groupNames}）にバインドされています。削除する前にバインドを解除してください。",
  unknownGroups: "不明なグループ",
  boundGroupsTooltip: "{count} 個のグループにバインド済み、クリックして表示",
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
  filterCheckinAvailable: "チェックイン",
  filterEnabled: "ステータス",
  filterEnabledLabel: "状態:",
  filterCheckinLabel: "サインイン:",
  filterEnabledAll: "全て",
  filterEnabledYes: "有効",
  filterEnabledNo: "無効",
  filterCheckinAll: "全て",
  filterCheckinYes: "可能",
  filterCheckinNo: "不可",
  searchPlaceholder: "名前、URL、メモを検索...",
  totalCount: "{count} サイト",
  paginationPrefix: "{total} 件",

  // Messages
  checkinSuccess: "チェックイン成功",
  checkinFailed: "チェックイン失敗",
  siteCreated: "サイトを作成しました",
  siteUpdated: "サイトを更新しました",
  siteDeleted: "サイトを削除しました",

  // Backend check-in messages (for translation mapping)
  backendMsg_checkInFailed: "チェックイン失敗",
  backendMsg_checkInDisabled: "チェックイン無効",
  backendMsg_missingCredentials: "認証情報がありません",
  backendMsg_missingUserId: "ユーザーIDがありません",
  backendMsg_unsupportedAuthType: "サポートされていない認証タイプ",
  backendMsg_anyrouterRequiresCookie: "AnyrouterはCookie認証が必要です",
  backendMsg_cloudflareChallenge: "Cloudflareチャレンジ、ブラウザからCookiesを更新してください",
  backendMsg_browserChallenge:
    "ブラウザチャレンジです。ブラウザからCookieまたはWAF Cookieを更新してください",
  backendMsg_alreadyCheckedIn: "本日チェックイン済み",
  backendMsg_stealthRequiresCookie: "ステルスバイパスにはCookie認証が必要です",
  backendMsg_missingCfCookies:
    "ブラウザWAF Cookieが不足しています。次のいずれかが必要: cf_clearance、acw_tc、cdn_sec_tc、acw_sc__v2、__cf_bm、_cfuvid",

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

  // Balance
  balance: "残高",
  balanceTooltip: "クリックして残高を更新",
  balanceNotSupported: "非対応",
  refreshBalance: "残高更新",
  refreshBalanceTooltip: "すべてのサイトの残高を更新",
  refreshingBalance: "残高を更新中...",
  balanceRefreshed: "残高更新完了",
  balanceNotAvailable: "取得不可",
};
