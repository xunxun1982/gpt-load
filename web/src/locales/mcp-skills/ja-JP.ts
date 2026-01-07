/**
 * MCP Skills i18n - Japanese
 */
export default {
  title: "MCP & Skills",
  subtitle: "MCP、MCP集約、Skillsエクスポートを管理",

  // Tabs
  tabServices: "MCP",
  tabGroups: "MCP集約",

  // Section titles
  basicInfo: "基本情報",
  connectionSettings: "接続設定",
  apiSettings: "API設定",
  toolsSettings: "ツール設定",

  // Service fields
  name: "名前",
  namePlaceholder: "名前を入力（小文字、スペースなし）",
  displayName: "表示名",
  displayNamePlaceholder: "表示名を入力",
  description: "説明",
  descriptionPlaceholder: "説明を入力",
  category: "カテゴリ",
  icon: "アイコン",
  iconPlaceholder: "絵文字アイコンを入力",
  sort: "並び順",
  sortTooltip: "数字が小さいほど上に表示",
  enabled: "有効",
  type: "タイプ",

  // Service types
  typeStdio: "Stdio",
  typeSse: "SSE",
  typeStreamableHttp: "Streamable HTTP",
  typeApiBridge: "API Bridge",

  // Categories - matching backend ServiceCategory enum in models.go
  categorySearch: "検索",
  categoryFetch: "フェッチ",
  categoryAI: "AI",
  categoryUtility: "ユーティリティ",
  categoryStorage: "ストレージ",
  categoryDatabase: "データベース",
  categoryFilesystem: "ファイルシステム",
  categoryBrowser: "ブラウザ",
  categoryCommunication: "通信",
  categoryDevelopment: "開発ツール",
  categoryCloud: "クラウド",
  categoryMonitoring: "監視",
  categoryProductivity: "生産性",
  categoryCustom: "カスタム",

  // Stdio/SSE fields
  command: "コマンド",
  commandPlaceholder: "例：uvx, npx, python",
  args: "引数",
  argsPlaceholder: "引数を入力（1行に1つ）",
  cwd: "作業ディレクトリ",
  cwdPlaceholder: "オプション、コマンド実行ディレクトリ",
  url: "URL",
  urlPlaceholder: "https://example.com/sse-endpoint",

  // Service info display
  serviceInfo: "MCP情報",
  argsCount: "引数",
  envCount: "環境変数",

  // Environment variables
  envVars: "環境変数",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "変数を追加",
  envVarsHint: "有効な変数のみが設定に追加されます",
  envVarEnabled: "有効",
  envVarDisabled: "無効",

  // Status
  disabled: "無効",

  // API Bridge fields
  apiEndpoint: "APIエンドポイント",
  apiEndpointPlaceholder: "https://api.example.com",
  apiKeyName: "APIキー名",
  apiKeyNamePlaceholder: "例：EXA_API_KEY",
  apiKeyValue: "APIキー",
  apiKeyValuePlaceholder: "APIキーを入力",
  apiKeyValueEditHint: "空欄で既存のキーを維持",
  apiKeyHeader: "認証ヘッダー",
  apiKeyHeaderPlaceholder: "例：Authorization, x-api-key",
  apiKeyPrefix: "認証プレフィックス",
  apiKeyPrefixPlaceholder: "例：Bearer",
  hasApiKey: "APIキー設定済み",
  noApiKey: "APIキーなし",

  // Tools
  tools: "ツール",
  toolName: "ツール名",
  toolDescription: "ツール説明",
  toolCount: "{count} ツール",
  totalTools: "合計",
  uniqueTools: "ユニーク",
  addTool: "ツールを追加",
  editTool: "ツールを編集",
  deleteTool: "ツールを削除",
  inputSchema: "入力スキーマ",
  inputSchemaPlaceholder: "JSONスキーマを入力",

  // Rate limiting
  rpdLimit: "日次制限",
  rpdLimitTooltip: "1日あたりのリクエスト制限（0 = 無制限）",

  // Health status
  healthStatus: "ヘルス",
  healthHealthy: "正常",
  healthUnhealthy: "異常",
  healthUnknown: "不明",

  // Group fields
  groupName: "集約名",
  groupNamePlaceholder: "集約名を入力（小文字、スペースなし）",
  groupDisplayName: "表示名",
  groupDescription: "説明",
  services: "MCPサービス",
  serviceCount: "{count} MCPサービス",
  selectServices: "MCPサービスを選択",
  noServicesSelected: "MCPサービス未選択",
  noServices: "サービスがありません",

  // Service weights for smart routing
  serviceWeights: "サービス重み",
  serviceWeightsHint:
    "重みが高いほど、smart_executeで選択される確率が高くなります。デフォルトは100",
  weight: "重み",
  weightHint:
    "重みが高いほど優先度が高くなります。エラー率の高いサービスは自動的に優先度が下がります",
  weightPlaceholder: "1-1000",
  errorRate: "エラー率",
  totalCalls: "総呼び出し",

  // Tool aliases for smart routing
  toolAliases: "ツールエイリアス",
  toolAliasesHint:
    "左側：統一名（重複可）。右側：実際のツール名（カンマ区切り）。smart_executeはすべてのエイリアスツールにマッチします",
  canonicalName: "統一名",
  aliasesPlaceholder: "ツール名1, ツール名2, ...",
  addToolAlias: "ツールエイリアスを追加",
  viewToolDescriptions: "ツール説明を表示",
  originalDescriptions: "元の説明",
  noMatchingTools: "一致するツールが見つかりません",
  unifiedDescription: "統一説明（オプション、トークン節約）",
  unifiedDescriptionPlaceholder: "統一説明を入力すると、元の説明を置き換えます",

  // MCP集約
  aggregationEnabled: "集約エンドポイントを有効化",
  aggregationEnabledTooltip:
    "有効にすると、集約エンドポイントURLからこのグループ内のすべてのMCPツールにアクセスできます",
  aggregationEndpoint: "集約エンドポイント",
  accessToken: "アクセストークン",
  accessTokenPlaceholder: "空欄で自動生成",
  accessTokenSetPlaceholder: "設定済み、空欄で維持",
  accessTokenAlreadySet:
    "アクセストークンは設定済みです。空欄のままにすると既存のトークンが維持されます。",
  regenerateToken: "再生成",
  copyToken: "トークンをコピー",
  tokenCopied: "トークンをコピーしました",
  tokenRegenerated: "トークンを再生成しました",
  generate: "生成",
  tokenGenerated: "アクセストークンを生成しました",

  // Skill export
  skillExport: "Skillsエクスポート",
  skillExportEndpoint: "SkillsエクスポートURL",
  exportAsSkill: "Skillsとしてエクスポート",
  skillExported: "Skillsをエクスポートしました",

  // Endpoint info
  endpointInfo: "エンドポイント情報",
  mcpConfig: "MCP設定",
  copyConfig: "設定をコピー",
  configCopied: "設定をコピーしました",
  copyFailed: "コピーに失敗しました",

  // Templates
  templates: "テンプレート",
  useTemplate: "テンプレートを使用",
  createFromTemplate: "テンプレートから作成",
  templateCreated: "テンプレートからMCPを作成しました",

  // Actions
  createService: "MCPを作成",
  editService: "MCPを編集",
  deleteService: "MCPを削除",
  createGroup: "MCP集約を作成",
  editGroup: "MCP集約を編集",
  deleteGroup: "MCP集約を削除",
  confirmDeleteService: "MCP「{name}」を削除しますか？",
  confirmDeleteGroup: "MCP集約「{name}」を削除しますか？",

  // Filter & Search
  filterEnabled: "ステータス",
  filterEnabledAll: "全て",
  filterEnabledYes: "有効",
  filterEnabledNo: "無効",
  filterCategory: "カテゴリ",
  filterCategoryAll: "全カテゴリ",
  filterType: "タイプ",
  filterTypeAll: "全タイプ",
  searchPlaceholder: "名前、説明を検索...",
  totalCount: "{count} 件",

  // Messages
  serviceCreated: "MCPを作成しました",
  serviceUpdated: "MCPを更新しました",
  serviceDeleted: "MCPを削除しました",
  groupCreated: "MCP集約を作成しました",
  groupUpdated: "MCP集約を更新しました",
  groupDeleted: "MCP集約を削除しました",

  // Import/Export/Delete
  exportAll: "全てエクスポート",
  importAll: "インポート",
  deleteAll: "全て削除",
  deleteAllWarning:
    "すべての {count} 件のMCPを削除しますか？この操作により、すべてのMCP集約からの参照も削除されます。確認するには ",
  deleteAllConfirmText: "削除確認",
  toConfirmDeletion: " と入力してください。",
  deleteAllPlaceholder: "「削除確認」と入力",
  confirmDelete: "削除を確認",
  incorrectConfirmText: "確認テキストが正しくありません",
  deleteAllSuccess: "{count}件のMCPを削除しました",
  deleteAllNone: "削除するMCPがありません",
  exportEncrypted: "暗号化エクスポート",
  exportPlain: "平文エクスポート",
  exportSuccess: "エクスポート成功",
  importSuccess: "インポート成功：{services} MCP、{groups} MCP集約",
  importFailed: "インポート失敗：{error}",
  importInvalidFormat: "インポートファイル形式が無効です",
  importInvalidJSON: "JSON形式が無効です",

  // Validation
  nameRequired: "名前を入力してください",
  nameDuplicate: "名前「{name}」は既に存在します",
  displayNameRequired: "表示名を入力してください",
  typeRequired: "タイプを選択してください",
  commandRequired: "Stdio/SSEタイプにはコマンドが必要です",
  apiEndpointRequired: "API BridgeタイプにはAPIエンドポイントが必要です",
  invalidJson: "JSON形式が無効です",

  // Test
  test: "テスト",
  testSuccess: "MCP「{name}」は正常に動作しています",
  testFailed: "テスト失敗：{error}",

  // Custom endpoint
  customEndpointHint: "空欄で公式エンドポイントを使用",

  // JSON import
  importMcpJson: "MCP JSONインポート",
  importMcpJsonBtn: "インポート",
  jsonImportFromFile: "ファイルからインポート",
  selectFile: "ファイル選択",
  fileReadError: "ファイルの読み込みに失敗しました",
  jsonImportLabel: "またはJSON設定を貼り付け",
  jsonImportPlaceholder: "MCP JSON設定を貼り付け",
  jsonImportHint:
    "Claude Desktop、Kiro、その他のMCPクライアントで使用される標準MCP JSON形式をサポート。autoApproveやdisabledToolsなどのフィールドは無視されます。",
  jsonImportEmpty: "JSON設定を入力してください",
  jsonImportInvalidFormat: "無効な形式：mcpServersオブジェクトが必要です",
  jsonImportNoServers: "設定にMCPが見つかりません",
  jsonImportSuccess: "インポート成功：{imported}件インポート、{skipped}件スキップ",
  jsonImportAllSkipped: "すべてのMCPがスキップされました（{skipped}件）",

  // MCP Endpoint
  mcpEnabled: "MCPエンドポイント",
  mcpEnabledTooltip: "外部アクセス用のMCPエンドポイントを有効にする",
  mcpEndpoint: "MCPエンドポイント",
  serviceEndpointInfo: "MCPエンドポイント情報",
  noMcpEndpoint: "MCPエンドポイントが有効になっていません",
  mcpEndpointNotEnabled: "このMCPのエンドポイントは有効になっていません",
  enableMcpEndpoint: "MCPエンドポイントを有効にする",
  loadingEndpointInfo: "エンドポイント情報を読み込み中...",
  mcpEndpointEnabled: "MCPエンドポイントが有効になりました",
  mcpEndpointDisabled: "MCPエンドポイントが無効になりました",

  // Tool expansion
  expandTools: "ツールを表示",
  collapseTools: "ツールを非表示",
  loadingTools: "ツールを読み込み中...",
  noTools: "ツールがありません",
  noEnabledServices: "有効なサービスがありません",
  refreshTools: "更新",
  toolsFromCache: "キャッシュから",
  toolsFresh: "最新",
  toolsCachedAt: "キャッシュ日時",
  toolsExpiresAt: "有効期限",
  toolsRefreshed: "ツールが正常に更新されました",
  toolsRefreshFailed: "ツールの更新に失敗しました：{error}",
  toolInputSchema: "入力スキーマ",

  // Selection hints
  selectItemHint: "左パネルからMCPまたはMCP集約を選択してください",
  selectServiceHint: "MCPを選択して詳細を表示",
  selectGroupHint: "MCP集約を選択して詳細を表示",
  noMatchingItems: "一致する項目が見つかりません",
  noItems: "MCPまたはMCP集約がありません",
};
