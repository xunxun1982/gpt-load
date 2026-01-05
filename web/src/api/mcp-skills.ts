import http from "@/utils/http";

// Service types (must match backend ServiceType constants)
export type MCPServiceType = "stdio" | "sse" | "streamable_http" | "api_bridge";
export type MCPServiceCategory = "search" | "code" | "data" | "utility" | "custom";

// Environment variable definition
export interface EnvVarDefinition {
  name: string;
  description: string;
  required: boolean;
  default_value?: string;
}

// Tool definition
export interface ToolDefinition {
  name: string;
  description: string;
  input_schema: Record<string, unknown>;
}

// Service DTO matching backend MCPServiceDTO
export interface MCPServiceDTO {
  id: number;
  name: string;
  display_name: string;
  description: string;
  category: string;
  icon: string;
  sort: number;
  enabled: boolean;
  type: MCPServiceType;

  // For stdio/sse services
  command?: string;
  args?: string[];
  cwd?: string; // Working directory for stdio

  // For API bridge services
  api_endpoint?: string;
  api_key_name?: string;
  has_api_key: boolean;
  api_key_header?: string;
  api_key_prefix?: string;

  // Environment variables
  required_env_vars?: EnvVarDefinition[];
  default_envs?: Record<string, string>;
  headers?: Record<string, string>;

  // Tools
  tools?: ToolDefinition[];
  tool_count: number;

  // Rate limiting
  rpd_limit: number;

  // MCP endpoint exposure
  mcp_enabled: boolean;
  has_access_token: boolean;

  // Status
  health_status: string;
  last_health_check?: string;

  // Timestamps
  created_at: string;
  updated_at: string;
}

// Service list params
export interface ServiceListParams {
  page?: number;
  page_size?: number;
  search?: string;
  category?: string;
  enabled?: boolean | null;
  type?: string;
}

// Service list result
export interface ServiceListResult {
  services: MCPServiceDTO[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Create service request
export interface CreateServiceRequest {
  name: string;
  display_name: string;
  description: string;
  category: string;
  icon: string;
  sort: number;
  enabled: boolean;
  type: MCPServiceType;
  command?: string;
  args?: string[];
  cwd?: string; // Working directory for stdio
  api_endpoint?: string;
  api_key_name?: string;
  api_key_value?: string;
  api_key_header?: string;
  api_key_prefix?: string;
  required_env_vars?: EnvVarDefinition[];
  default_envs?: Record<string, string>;
  headers?: Record<string, string>;
  tools?: ToolDefinition[];
  rpd_limit?: number;
  mcp_enabled?: boolean;
  access_token?: string;
}

// Update service request (partial)
export type UpdateServiceRequest = Partial<CreateServiceRequest>;

// Group DTO matching backend MCPServiceGroupDTO
export interface MCPServiceGroupDTO {
  id: number;
  name: string;
  display_name: string;
  description: string;
  service_ids: number[];
  service_count: number;
  services?: MCPServiceDTO[];
  enabled: boolean;
  aggregation_enabled: boolean;
  aggregation_endpoint?: string;
  has_access_token: boolean;
  skill_export_endpoint?: string;
  total_tool_count: number;
  created_at: string;
  updated_at: string;
}

// Group list params
export interface GroupListParams {
  page?: number;
  page_size?: number;
  search?: string;
  enabled?: boolean | null;
}

// Group list result
export interface GroupListResult {
  groups: MCPServiceGroupDTO[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Create group request
export interface CreateGroupRequest {
  name: string;
  display_name: string;
  description: string;
  service_ids: number[];
  enabled: boolean;
  aggregation_enabled: boolean;
  access_token?: string;
}

// Update group request (partial)
export type UpdateGroupRequest = Partial<CreateGroupRequest>;

// API bridge template
export interface APIBridgeTemplate {
  id: string;
  name: string;
  display_name: string;
  description: string;
  category: string;
  icon: string;
  api_endpoint: string;
  api_key_name: string;
  api_key_header: string;
  api_key_prefix: string;
  tools: ToolDefinition[];
}

// Group endpoint info
export interface GroupEndpointInfo {
  group_id: number;
  group_name: string;
  aggregation_endpoint: string;
  skill_export_url: string;
  mcp_config_json: string;
}

// Service endpoint info
export interface ServiceEndpointInfo {
  service_id: number;
  service_name: string;
  service_type: string;
  mcp_endpoint: string;
  api_endpoint?: string;
  mcp_config_json: string;
}

// Export/Import types
export interface MCPServiceExportInfo {
  name: string;
  display_name: string;
  description: string;
  category: string;
  icon: string;
  sort: number;
  enabled: boolean;
  type: string;
  command?: string;
  args?: string[];
  cwd?: string; // Working directory for stdio
  api_endpoint?: string;
  api_key_name?: string;
  api_key_value?: string;
  api_key_header?: string;
  api_key_prefix?: string;
  required_env_vars?: EnvVarDefinition[];
  default_envs?: Record<string, string>;
  headers?: Record<string, string>;
  tools?: ToolDefinition[];
  rpd_limit: number;
}

export interface MCPServiceGroupExportInfo {
  name: string;
  display_name: string;
  description: string;
  service_names: string[];
  enabled: boolean;
  aggregation_enabled: boolean;
}

export interface MCPSkillsExportData {
  version: string;
  exported_at: string;
  services: MCPServiceExportInfo[];
  groups: MCPServiceGroupExportInfo[];
}

export interface MCPSkillsImportResult {
  services_imported: number;
  services_skipped: number;
  groups_imported: number;
  groups_skipped: number;
}

// Service test result
export interface ServiceTestResult {
  service_id: number;
  service_name: string;
  service_type: string;
  success: boolean;
  message?: string;
  error?: string;
  response?: Record<string, unknown>;
  tested_at: string;
}

// MCP servers JSON import types
export interface MCPServerConfig {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string; // Working directory for stdio
  url?: string;
  headers?: Record<string, string>;
  type?: string;
  disabled?: boolean;
  autoApprove?: string[];
  disabledTools?: string[];
}

export interface MCPServersConfig {
  mcpServers: Record<string, MCPServerConfig>;
}

export interface MCPServersImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

// Service tools result with cache info
export interface ServiceToolsResult {
  service_id: number;
  service_name: string;
  server_name?: string;
  server_version?: string;
  description?: string;
  tools: ToolDefinition[];
  tool_count: number;
  from_cache: boolean;
  cached_at?: string;
  expires_at?: string;
}

// API client
export const mcpSkillsApi = {
  // ============ Services ============

  // List services (non-paginated)
  async listServices(): Promise<MCPServiceDTO[]> {
    const res = await http.get("/mcp-skills/services");
    return res.data || [];
  },

  // List services (paginated)
  async listServicesPaginated(params: ServiceListParams = {}): Promise<ServiceListResult> {
    const query = new URLSearchParams();
    if (params.page) {
      query.set("page", params.page.toString());
    }
    if (params.page_size) {
      query.set("page_size", params.page_size.toString());
    }
    if (params.search) {
      query.set("search", params.search);
    }
    if (params.category) {
      query.set("category", params.category);
    }
    if (params.type) {
      query.set("type", params.type);
    }
    if (params.enabled !== undefined && params.enabled !== null) {
      query.set("enabled", params.enabled.toString());
    }
    const res = await http.get(`/mcp-skills/services?${query.toString()}`);
    return res.data;
  },

  // Get service by ID
  async getService(id: number): Promise<MCPServiceDTO> {
    const res = await http.get(`/mcp-skills/services/${id}`);
    return res.data;
  },

  // Create service
  async createService(payload: CreateServiceRequest): Promise<MCPServiceDTO> {
    const res = await http.post("/mcp-skills/services", payload);
    return res.data;
  },

  // Update service
  async updateService(id: number, payload: UpdateServiceRequest): Promise<MCPServiceDTO> {
    const res = await http.put(`/mcp-skills/services/${id}`, payload);
    return res.data;
  },

  // Delete service
  deleteService(id: number): Promise<void> {
    return http.delete(`/mcp-skills/services/${id}`);
  },

  // Delete all services (including those in groups)
  async deleteAllServices(): Promise<{ deleted: number }> {
    const res = await http.delete("/mcp-skills/services/all");
    return res.data;
  },

  // Count all services
  async countAllServices(): Promise<number> {
    const res = await http.get("/mcp-skills/services/count");
    return res.data?.count || 0;
  },

  // Toggle service enabled
  async toggleService(id: number): Promise<MCPServiceDTO> {
    const res = await http.post(`/mcp-skills/services/${id}/toggle`);
    return res.data;
  },

  // Toggle service MCP endpoint enabled
  async toggleServiceMCP(id: number): Promise<MCPServiceDTO> {
    const res = await http.post(`/mcp-skills/services/${id}/toggle-mcp`);
    return res.data;
  },

  // Get service endpoint info
  async getServiceEndpointInfo(id: number): Promise<ServiceEndpointInfo> {
    const res = await http.get(`/mcp-skills/services/${id}/endpoint-info`);
    return res.data;
  },

  // Regenerate service access token
  async regenerateServiceAccessToken(id: number): Promise<{ access_token: string }> {
    const res = await http.post(`/mcp-skills/services/${id}/regenerate-token`);
    return res.data;
  },

  // Get API bridge templates
  async getTemplates(): Promise<APIBridgeTemplate[]> {
    const res = await http.get("/mcp-skills/templates");
    return res.data || [];
  },

  // Create service from template
  async createFromTemplate(
    templateId: string,
    apiKeyValue: string,
    customEndpoint?: string
  ): Promise<MCPServiceDTO> {
    const res = await http.post("/mcp-skills/services/from-template", {
      template_id: templateId,
      api_key_value: apiKeyValue,
      custom_endpoint: customEndpoint,
    });
    return res.data;
  },

  // Test service connection
  async testService(
    id: number,
    toolName?: string,
    args?: Record<string, unknown>
  ): Promise<ServiceTestResult> {
    const res = await http.post(`/mcp-skills/services/${id}/test`, {
      tool_name: toolName,
      arguments: args,
    });
    return res.data;
  },

  // ============ Groups ============

  // List groups (non-paginated)
  async listGroups(): Promise<MCPServiceGroupDTO[]> {
    const res = await http.get("/mcp-skills/groups");
    return res.data || [];
  },

  // List groups (paginated)
  async listGroupsPaginated(params: GroupListParams = {}): Promise<GroupListResult> {
    const query = new URLSearchParams();
    if (params.page) {
      query.set("page", params.page.toString());
    }
    if (params.page_size) {
      query.set("page_size", params.page_size.toString());
    }
    if (params.search) {
      query.set("search", params.search);
    }
    if (params.enabled !== undefined && params.enabled !== null) {
      query.set("enabled", params.enabled.toString());
    }
    const res = await http.get(`/mcp-skills/groups?${query.toString()}`);
    return res.data;
  },

  // Get group by ID
  async getGroup(id: number): Promise<MCPServiceGroupDTO> {
    const res = await http.get(`/mcp-skills/groups/${id}`);
    return res.data;
  },

  // Create group
  async createGroup(payload: CreateGroupRequest): Promise<MCPServiceGroupDTO> {
    const res = await http.post("/mcp-skills/groups", payload);
    return res.data;
  },

  // Update group
  async updateGroup(id: number, payload: UpdateGroupRequest): Promise<MCPServiceGroupDTO> {
    const res = await http.put(`/mcp-skills/groups/${id}`, payload);
    return res.data;
  },

  // Delete group
  deleteGroup(id: number): Promise<void> {
    return http.delete(`/mcp-skills/groups/${id}`);
  },

  // Toggle group enabled
  async toggleGroup(id: number): Promise<MCPServiceGroupDTO> {
    const res = await http.post(`/mcp-skills/groups/${id}/toggle`);
    return res.data;
  },

  // Add services to group
  async addServicesToGroup(groupId: number, serviceIds: number[]): Promise<MCPServiceGroupDTO> {
    const res = await http.post(`/mcp-skills/groups/${groupId}/services`, {
      service_ids: serviceIds,
    });
    return res.data;
  },

  // Remove services from group
  async removeServicesFromGroup(
    groupId: number,
    serviceIds: number[]
  ): Promise<MCPServiceGroupDTO> {
    const res = await http.delete(`/mcp-skills/groups/${groupId}/services`, {
      data: { service_ids: serviceIds },
    });
    return res.data;
  },

  // Get group endpoint info
  async getGroupEndpointInfo(groupId: number): Promise<GroupEndpointInfo> {
    const res = await http.get(`/mcp-skills/groups/${groupId}/endpoint-info`);
    return res.data;
  },

  // Regenerate access token
  async regenerateAccessToken(groupId: number): Promise<{ access_token: string }> {
    const res = await http.post(`/mcp-skills/groups/${groupId}/regenerate-token`);
    return res.data;
  },

  // Get access token
  async getAccessToken(groupId: number): Promise<{ access_token: string }> {
    const res = await http.get(`/mcp-skills/groups/${groupId}/access-token`);
    return res.data;
  },

  // Export group as skill (returns blob)
  async exportGroupAsSkill(groupId: number): Promise<Blob> {
    const res = await http.get(`/mcp-skills/groups/${groupId}/export`, {
      responseType: "blob",
    });
    return res as unknown as Blob;
  },

  // ============ Import/Export ============

  // Export all MCP skills data
  async exportAll(mode: "plain" | "encrypted" = "encrypted"): Promise<MCPSkillsExportData> {
    const res = await http.get("/mcp-skills/export", { params: { mode } });
    return res.data;
  },

  // Import MCP skills data
  async importAll(
    data: MCPSkillsExportData,
    mode?: "plain" | "encrypted"
  ): Promise<MCPSkillsImportResult> {
    const params: Record<string, string> = {};
    if (mode) {
      params.mode = mode;
    }
    const res = await http.post("/mcp-skills/import", data, { params });
    return res.data;
  },

  // Import MCP servers from standard JSON format (Claude Desktop, Kiro, etc.)
  async importMCPServers(config: MCPServersConfig): Promise<MCPServersImportResult> {
    const res = await http.post("/mcp-skills/import-mcp-json", config);
    return res.data;
  },

  // Get service tools with caching support
  async getServiceTools(id: number, forceRefresh = false): Promise<ServiceToolsResult> {
    const params = forceRefresh ? { refresh: "true" } : {};
    const res = await http.get(`/mcp-skills/services/${id}/tools`, { params });
    return res.data;
  },

  // Force refresh service tools cache
  async refreshServiceTools(id: number): Promise<ServiceToolsResult> {
    const res = await http.post(`/mcp-skills/services/${id}/tools/refresh`);
    return res.data;
  },
};
