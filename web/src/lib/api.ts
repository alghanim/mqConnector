// Thin REST client for the mqConnector backend. Sessions are cookie-based, so
// every fetch sets `credentials: 'include'`. Errors are normalised into a
// single ApiError shape so callers can branch on `.status`.

export interface ApiError {
  status: number;
  message: string;
}

const BASE = '/api';

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  init: RequestInit = {}
): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(init.headers as Record<string, string> | undefined)
  };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  const res = await fetch(`${BASE}${path}`, {
    method,
    credentials: 'include',
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    ...init
  });

  const isJSON = (res.headers.get('content-type') || '').includes('application/json');
  const payload = isJSON ? await res.json().catch(() => ({})) : await res.text();

  if (!res.ok) {
    const message =
      (isJSON && typeof payload === 'object' && payload !== null && 'error' in payload
        ? (payload as { error: string }).error
        : '') ||
      (typeof payload === 'string' ? payload : '') ||
      res.statusText;
    const err: ApiError = { status: res.status, message };
    throw err;
  }
  return payload as T;
}

/**
 * postRaw POSTs an arbitrary body without JSON-encoding it. Use this for
 * endpoints that accept a raw payload (e.g. /samples/extract treats the
 * request body as the sample message). Pass the raw string + the
 * Content-Type the server should see.
 */
async function postRaw<T>(path: string, body: string, contentType: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers: { Accept: 'application/json', 'Content-Type': contentType },
    body
  });
  const isJSON = (res.headers.get('content-type') || '').includes('application/json');
  const payload = isJSON ? await res.json().catch(() => ({})) : await res.text();
  if (!res.ok) {
    const message =
      (isJSON && typeof payload === 'object' && payload !== null && 'error' in payload
        ? (payload as { error: string }).error
        : '') ||
      (typeof payload === 'string' ? payload : '') ||
      res.statusText;
    const err: ApiError = { status: res.status, message };
    throw err;
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  postRaw,
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path)
};

// --- Domain types ---------------------------------------------------------

export type ConnectionType = 'ibm' | 'rabbitmq' | 'kafka';

export interface Connection {
  id?: string;
  name: string;
  type: ConnectionType;
  queue_manager?: string;
  conn_name?: string;
  channel?: string;
  username?: string;
  password?: string;
  queue_name?: string;
  url?: string;
  brokers?: string;
  topic?: string;
  created_at?: string;
  updated_at?: string;
}

export interface Pipeline {
  id?: string;
  name: string;
  source_id: string;
  destination_id: string;
  output_format: 'same' | 'json' | 'xml';
  schema_id?: string;
  filter_paths: string[];
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export type StageType = 'filter' | 'transform' | 'translate' | 'route' | 'script' | 'validate';

export interface Schema {
  id?: string;
  name: string;
  schema_type: 'json_schema' | 'xsd';
  content: string;
  created_at?: string;
  updated_at?: string;
}

export interface Stage {
  id?: string;
  pipeline_id?: string;
  stage_order: number;
  stage_type: StageType;
  stage_config: string;
  enabled: boolean;
}

export type TransformType = 'rename' | 'mask' | 'move' | 'set' | 'delete';

export interface Transform {
  id?: string;
  pipeline_id?: string;
  transform_type: TransformType;
  source_path: string;
  target_path: string;
  mask_pattern: string;
  mask_replace: string;
  set_value: string;
  order: number;
}

export type RoutingOperator = 'eq' | 'neq' | 'contains' | 'regex' | 'gt' | 'lt' | 'exists';

export interface RoutingRule {
  id?: string;
  pipeline_id?: string;
  condition_path: string;
  condition_operator: RoutingOperator;
  condition_value: string;
  destination_id: string;
  priority: number;
  enabled: boolean;
}

export interface DLQEntry {
  id: string;
  pipeline_id?: string;
  source_queue?: string;
  original_msg: string;
  error_reason: string;
  retry_count: number;
  last_retry_at?: string;
  created_at: string;
}

export interface PipelineMetric {
  pipeline_id: string;
  source_queue: string;
  dest_queue: string;
  messages_processed: number;
  messages_failed: number;
  bytes_processed: number;
  last_message_time: string;
  avg_latency_ms: number;
  status: string;
  last_error?: string;
}

export interface Health {
  status: 'healthy' | 'degraded' | 'unhealthy';
  version: string;
  db_status: string;
  uptime: string;
  active_pipelines: number;
  connections?: {
    pipeline_id: string;
    status: string;
    last_error?: string;
    source_queue: string;
    dest_queue: string;
  }[];
}

export interface Me {
  sub: string;
  preferred_username?: string;
  name?: string;
  email?: string;
  roles?: string[];
}

export interface AuditEntry {
  id: string;
  tenant_id: string;
  at: string;
  actor: string;
  actor_sub: string;
  action: string;
  resource: string;
  status: number;
  request_id: string;
  remote_ip: string;
}

// ─── tenants ─────────────────────────────────────────────────────

export type TenantStatus = 'active' | 'suspended' | 'disabled';
export type Role = 'viewer' | 'operator' | 'admin' | 'owner';

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  status: TenantStatus;
  max_pipelines: number;
  max_msgs_per_minute: number;
  created_at: string;
  updated_at: string;
}

export interface Membership {
  tenant_id: string;
  user_sub: string;
  username: string;
  role: Role;
  created_at: string;
  updated_at: string;
}

// Returned by GET /api/v1/tenants — each row pairs a Tenant with the
// caller's role in it and a flag marking the active tenant.
export interface TenantMembership {
  tenant: Tenant;
  role: Role;
  is_active: boolean;
}
