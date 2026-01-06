/**
 * MCP Skills i18n - Japanese
 */
export default {
  title: "MCP & Skills",
  subtitle: "MCPサービス、サービスグループ、スキルエクスポートを管理",

  // Tabs
  tabServices: "サービス",
  tabGroups: "グループ",

  // Section titles
  basicInfo: "基本情報",
  connectionSettings: "接続設定",
  apiSettings: "API設定",
  toolsSettings: "ツール設定",

  // Service fields
  name: "名前",
  namePlaceholder: "サービス名を入力（小文字、スペースなし）",
  displayName: "表示名",
  displayNamePlaceholder: "表示名を入力",
  description: "説明",
  descriptionPlaceholder: "サービスの説明を入力",
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

  // Categories
  categorySearch: "検索",
  categoryCode: "コード",
  categoryData: "データ",
  categoryUtility: "ユーティリティ",
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
  serviceInfo: "情報",
  argsCount: "引数",
  envCount: "環境変数",

  // Environment variables
  envVars: "環境変数",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "変数を追加",
  envVarsHint: "有効な変数のみがサービス設定に追加されます",
  envVarEnabled: "有効",
  envVarDisabled: "無効",

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
  groupName: "グループ名",
  groupNamePlaceholder: "グループ名を入力（小文字、スペースなし）",
  groupDisplayName: "表示名",
  groupDescription: "説明",
  services: "サービス",
  serviceCount: "{count} サービス",
  selectServices: "サービスを選択",
  noServicesSelected: "サービス未選択",

  // MCP集約
  aggregationEnabled: "MCP集約",
  aggregationEnabledTooltip: "このグループのMCP集約エンドポイントを有効化",
  aggregationEndpoint: "集約エンドポイント",
  accessToken: "アクセストークン",
  accessTokenPlaceholder: "空欄で自動生成",
  regenerateToken: "再生成",
  copyToken: "トークンをコピー",
  tokenCopied: "トークンをコピーしました",
  tokenRegenerated: "トークンを再生成しました",

  // Skill export
  skillExport: "スキルエクスポート",
  skillExportEndpoint: "スキルエクスポートURL",
  exportAsSkill: "スキルとしてエクスポート",
  skillExported: "スキルをエクスポートしました",

  // Endpoint info
  endpointInfo: "エンドポイント情報",
  mcpConfig: "MCP設定",
  copyConfig: "設定をコピー",
  configCopied: "設定をコピーしました",

  // Templates
  templates: "テンプレート",
  useTemplate: "テンプレートを使用",
  createFromTemplate: "テンプレートから作成",
  templateCreated: "テンプレートからサービスを作成しました",

  // Actions
  createService: "サービスを作成",
  editService: "サービスを編集",
  deleteService: "サービスを削除",
  createGroup: "グループを作成",
  editGroup: "グループを編集",
  deleteGroup: "グループを削除",
  confirmDeleteService: "サービス「{name}」を削除しますか？",
  confirmDeleteGroup: "グループ「{name}」を削除しますか？",

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
  serviceCreated: "サービスを作成しました",
  serviceUpdated: "サービスを更新しました",
  serviceDeleted: "サービスを削除しました",
  groupCreated: "グループを作成しました",
  groupUpdated: "グループを更新しました",
  groupDeleted: "グループを削除しました",

  // Import/Export/Delete
  exportAll: "全てエクスポート",
  importAll: "インポート",
  deleteAll: "全て削除",
  deleteAllWarning:
    "すべての {count} 件のサービスを削除しますか？この操作により、すべてのグループからサービス参照も削除されます。確認するには ",
  deleteAllConfirmText: "削除確認",
  toConfirmDeletion: " と入力してください。",
  deleteAllPlaceholder: "「削除確認」と入力",
  confirmDelete: "削除を確認",
  incorrectConfirmText: "確認テキストが正しくありません",
  deleteAllSuccess: "{count}件のサービスを削除しました",
  deleteAllNone: "削除するサービスがありません",
  exportEncrypted: "暗号化エクスポート",
  exportPlain: "平文エクスポート",
  exportSuccess: "エクスポート成功",
  importSuccess: "インポート成功：{services} サービス、{groups} グループ",
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
  testSuccess: "サービス「{name}」は正常に動作しています",
  testFailed: "テスト失敗：{error}",

  // Custom endpoint
  customEndpointHint: "空欄で公式エンドポイントを使用",

  // JSON import
  importMcpJson: "MCP JSONインポート",
  importMcpJsonBtn: "インポート",
  jsonImportLabel: "MCP JSON設定",
  jsonImportPlaceholder: "MCP JSON設定を貼り付け",
  jsonImportHint:
    "Claude Desktop、Kiro、その他のMCPクライアントで使用される標準MCP JSON形式をサポート。autoApproveやdisabledToolsなどのフィールドは無視されます。",
  jsonImportEmpty: "JSON設定を入力してください",
  jsonImportInvalidFormat: "無効な形式：mcpServersオブジェクトが必要です",
  jsonImportNoServers: "設定にMCPサーバーが見つかりません",
  jsonImportSuccess: "インポート成功：{imported}件インポート、{skipped}件スキップ",
  jsonImportAllSkipped: "すべてのサーバーがスキップされました（{skipped}件）",

  // MCP Endpoint
  mcpEnabled: "MCPエンドポイント",
  mcpEnabledTooltip: "外部アクセス用のMCPエンドポイントを有効にする",
  mcpEndpoint: "MCPエンドポイント",
  serviceEndpointInfo: "サービスエンドポイント情報",
  noMcpEndpoint: "MCPエンドポイントが有効になっていません",
  mcpEndpointNotEnabled: "このサービスのMCPエンドポイントは有効になっていません",
  enableMcpEndpoint: "MCPエンドポイントを有効にする",
  loadingEndpointInfo: "エンドポイント情報を読み込み中...",
  mcpEndpointEnabled: "MCPエンドポイントが有効になりました",
  mcpEndpointDisabled: "MCPエンドポイントが無効になりました",

  // Tool expansion
  expandTools: "ツールを表示",
  collapseTools: "ツールを非表示",
  loadingTools: "ツールを読み込み中...",
  noTools: "ツールがありません",
  refreshTools: "更新",
  toolsFromCache: "キャッシュから",
  toolsFresh: "最新",
  toolsCachedAt: "キャッシュ日時",
  toolsExpiresAt: "有効期限",
  toolsRefreshed: "ツールが正常に更新されました",
  toolsRefreshFailed: "ツールの更新に失敗しました：{error}",
  toolInputSchema: "入力スキーマ",

  // Selection hints
  selectItemHint: "左パネルからサービスまたはグループを選択してください",
  selectServiceHint: "サービスを選択して詳細を表示",
  selectGroupHint: "グループを選択して詳細を表示",
  noMatchingItems: "一致する項目が見つかりません",
  noItems: "サービスまたはグループがありません",
};
