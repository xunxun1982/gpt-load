/**
 * TypeScript type definitions for Hub (Centralized Management) feature.
 * These types match the backend API response structures.
 */

// Model source information - represents a group that provides a model
export interface ModelSource {
  group_id: number;
  group_name: string;
  group_type: "standard" | "aggregate";
  sort: number;
  weight: number;
  health_score: number;
  effective_weight: number;
  enabled: boolean;
}

// Model pool entry - represents a model with all its source groups
export interface ModelPoolEntry {
  model_name: string;
  sources: ModelSource[];
}

// Model group priority DTO - represents a group with priority info
export interface ModelGroupPriority {
  group_id: number;
  group_name: string;
  group_type: "standard" | "aggregate";
  is_child_group: boolean; // True if this is a child group of a standard group
  channel_type: string;
  priority: number;
  health_score: number;
  enabled: boolean;
}

// Model pool entry V2 - represents a model with priority-based groups
export interface ModelPoolEntryV2 {
  model_name: string;
  groups: ModelGroupPriority[];
}

// Hub settings DTO
export interface HubSettings {
  max_retries: number;
  retry_delay: number;
  health_threshold: number;
  enable_priority: boolean;
}

// Hub access key modes
export type AllowedModelsMode = "all" | "specific";

// Hub access key DTO - API response format (with masked key)
export interface HubAccessKey {
  id: number;
  name: string;
  masked_key: string;
  allowed_models: string[];
  allowed_models_mode: AllowedModelsMode;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

// Parameters for creating a new access key
export interface CreateAccessKeyParams {
  name: string;
  key_value?: string; // Optional: auto-generated if not provided
  allowed_models: string[]; // Empty array means all models
  enabled?: boolean; // Default: true
}

// Parameters for updating an existing access key
export interface UpdateAccessKeyParams {
  name?: string;
  allowed_models?: string[];
  enabled?: boolean;
}

// Model pool list parameters (for pagination and filtering)
export interface ModelPoolParams {
  page?: number;
  page_size?: number;
  search?: string;
}

// Model pool paginated response
export interface ModelPoolResponse {
  models: ModelPoolEntry[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Model pool V2 paginated response
export interface ModelPoolV2Response {
  models: ModelPoolEntryV2[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Access key list parameters
export interface AccessKeyListParams {
  page?: number;
  page_size?: number;
  search?: string;
  enabled?: boolean | null;
}

// Access key paginated response
export interface AccessKeyListResponse {
  access_keys: HubAccessKey[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Hub endpoint information
export interface HubEndpointInfo {
  base_url: string;
  chat_completions: string;
  models: string;
  messages: string; // Claude format
  responses: string; // Codex format
}

// Update model group priority params
export interface UpdateModelGroupPriorityParams {
  model_name: string;
  group_id: number;
  priority: number;
}
