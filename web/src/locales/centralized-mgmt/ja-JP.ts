/**
 * Centralized Management i18n - Japanese
 */
export default {
  // Tab label
  tabLabel: "集中管理",

  // Endpoint display
  supportedChannels: "対応チャネル",
  channelHint: "リクエストはグループ/集約に転送され処理されます（チャット、音声、画像、動画など）",
  copyBaseUrl: "ベースURLをコピー",
  baseUrlCopied: "ベースURLをコピーしました",
  endpointCopied: "エンドポイントURLをコピーしました",

  // Model pool
  modelPool: "モデルプール",
  modelName: "モデル名",
  sourceGroups: "ソースグループ",
  groupType: "グループタイプ",
  channelType: "チャネルタイプ",
  healthScore: "健康度",
  effectiveWeight: "有効重み",
  groupCount: "グループ数",
  standardGroup: "標準",
  subGroup: "サブ",
  aggregateGroup: "集約",
  aggregateGroupShort: "集",
  subGroupShort: "子",
  noModels: "利用可能なモデルがありません",
  noEnabledGroups: "有効なグループがありません",
  searchModelPlaceholder: "モデルまたはグループ名を検索...",
  totalModels: "合計 {total} モデル",
  filterAll: "すべて",
  editPriority: "優先度を編集",

  // Priority
  priority: "優先度",
  priorityHint: "0=無効、1-999=優先度（数字が小さいほど優先度が高い）",

  // Hub settings
  hubSettings: "Hub設定",
  maxRetries: "最大リトライ回数",
  maxRetriesHint: "同一優先度内での最大リトライ回数",
  retryDelay: "リトライ遅延",
  healthThreshold: "健康閾値",
  healthThresholdHint: "この閾値を下回るグループはスキップされます",
  enablePriority: "優先度ルーティングを有効化",

  // Access keys
  accessKeys: "アクセスキー",
  accessKeyName: "名前",
  maskedKey: "キー",
  allowedModels: "許可モデル",
  allModels: "全モデル",
  specificModels: "{count} モデル",
  createAccessKey: "アクセスキーを作成",
  editAccessKey: "アクセスキーを編集",
  deleteAccessKey: "アクセスキーを削除",
  confirmDeleteAccessKey: 'アクセスキー "{name}" を削除してもよろしいですか？',
  accessKeyCreated: "アクセスキーを作成しました",
  accessKeyUpdated: "アクセスキーを更新しました",
  accessKeyDeleted: "アクセスキーを削除しました",
  accessKeyToggled: "アクセスキーのステータスを更新しました",
  noAccessKeys: "アクセスキーがありません",
  keyCreatedCopyHint: "キーをコピーして保存してください。このキーは一度だけ表示されます",

  // Access key form
  keyName: "キー名",
  keyNamePlaceholder: "キー名を入力",
  keyValue: "キー値",
  keyValuePlaceholder: "空欄で自動生成、またはカスタムキーを入力",
  keyValueHint: "空欄の場合、hk-プレフィックス付きのキーが自動生成されます",
  allowedModelsMode: "モデル権限",
  allowedModelsModeAll: "全モデルへのアクセスを許可",
  allowedModelsModeSpecific: "特定のモデルのみアクセスを許可",
  selectAllowedModels: "許可するモデルを選択",
  searchModelsPlaceholder: "モデルを検索...",
  selectedModelsCount: "{count} モデルを選択",

  // Panel
  centralizedManagement: "集中管理",
  refreshModelPool: "モデルプールを更新",
  refreshing: "更新中...",
  totalAccessKeys: "合計 {total} キー",
};
