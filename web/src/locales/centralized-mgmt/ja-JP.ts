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
  priorityHint: "1-999=優先度（数字が小さいほど優先度が高い）、1000=システム内部予約値",
  prioritySortHint:
    "同じ優先度内では、健康度が高いほど選択確率が高くなります。例：priority=10はpriority=100より優先されます",
  priorityColumnHint: "数値が小さい=優先度が高い（1=最高、999=最低、1000=システム予約）",
  priorityExplanationHint:
    "💡 グループタグの数字（例：:20、:100）は優先度です。数値が小さいほど優先度が高くなります。同じ優先度では健康度加重ランダム選択",

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

  // Routing logic
  routingLogic: "ルーティングロジック（順次実行）",
  routingStep1:
    "① パス → フォーマット検出（Chat/Claude/Gemini/Image/Audio）。不明な形式は OpenAI にフォールバック",
  routingStep2: "② リクエストからモデル名を抽出",
  routingStep3: "③ アクセス制御：キー権限の検証",
  routingStep4: "④ モデル可用性：有効なグループにモデルが存在するか確認",
  routingStep5:
    "⑤ グループ選択フィルタ：健康閾値 + 有効状態 + チャネル互換性 + CCサポート（Claude）+ 集約前提条件（リクエストサイズ制限など）",
  routingStep6: "⑥ チャネル優先度：ネイティブチャネル > 互換チャネル",
  routingStep7: "⑦ グループ選択：最小priority値（小さいほど高優先度）→ 健康度加重ランダム",
  routingStep8: "⑧ パス書き換えと転送：/hub/v1/* → /proxy/グループ名/v1/*",
};
