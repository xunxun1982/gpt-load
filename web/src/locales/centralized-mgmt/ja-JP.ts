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
  onlyAggregateGroups: "集約グループのみ",
  onlyAggregateGroupsHint:
    "有効にすると、Hubは集約グループのみにルーティングし、標準グループを無視します",

  // Access keys
  accessKeys: "アクセスキー",
  accessKeyName: "名前",
  maskedKey: "キー",
  keyCopied: "キーをコピーしました",
  keyNameCopied: "名前をコピーしました",
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
  keyOnlyShownOnce:
    "キーは作成時に一度だけ表示されます。閉じた後は再度表示できません。コピーしたことを確認してください。",

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
  onlyAggregateGroupsActive: "🔒 集約グループのみモードが有効（Hub設定で変更可能）",
  refreshModelPool: "モデルプールを更新",
  refreshing: "更新中...",
  totalAccessKeys: "合計 {total} キー",

  // Usage statistics
  usageCount: "使用回数",
  lastUsedAt: "最終使用",
  neverUsed: "未使用",
  justNow: "たった今",
  minutesAgo: "{n} 分前",
  hoursAgo: "{n} 時間前",
  daysAgo: "{n} 日前",
  monthsAgo: "{n} ヶ月前",
  yearsAgo: "{n} 年前",

  // Batch operations
  batchOperations: "一括操作",
  batchDelete: "一括削除",
  batchEnable: "一括有効化",
  batchDisable: "一括無効化",
  selectedKeys: "{count} キーを選択",
  confirmBatchDelete: "選択した {count} 個のアクセスキーを削除してもよろしいですか？",
  batchDeleteSuccess: "{count} 個のアクセスキーを削除しました",
  batchEnableSuccess: "{count} 個のアクセスキーを有効化しました",
  batchDisableSuccess: "{count} 個のアクセスキーを無効化しました",
  selectAtLeastOne: "少なくとも1つのアクセスキーを選択してください",

  // Custom models
  customModels: "カスタムモデル",
  customModelNames: "カスタムモデル名",
  customModelNamesHint: "集約グループのカスタムモデル名を追加、1行に1つ",
  addCustomModel: "モデルを追加",
  editCustomModels: "カスタムモデルを編集",
  noCustomModels: "カスタムモデルなし",
  customModelsUpdated: "カスタムモデルを更新しました",
  aggregateGroupName: "集約グループ",
  modelCount: "{count} モデル",
  customModelBadge: "カスタム",
  customModelTooltip: "これはユーザー定義のカスタムモデル名です",
};
