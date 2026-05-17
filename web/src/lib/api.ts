// Thin REST client for the mqConnector backend. Sessions are cookie-based, so
// every fetch sets `credentials: 'include'`. Errors are normalised into a
// single ApiError shape so callers can branch on `.status`.

export interface ApiError {
  status: number;
  message: string;
}

const BASE = '/api';

// readCookie reads a non-HttpOnly cookie value from the browser by
// name. Returns null when the cookie is absent (e.g. before login or
// in the rare case where document.cookie isn't available — SSR /
// jsdom). Used for the CSRF double-submit token.
function readCookie(name: string): string | null {
  if (typeof document === 'undefined') return null;
  const target = name + '=';
  for (const c of document.cookie.split(';')) {
    const trimmed = c.trim();
    if (trimmed.startsWith(target)) return trimmed.slice(target.length);
  }
  return null;
}

const CSRF_COOKIE = 'mqc_csrf';
const CSRF_HEADER = 'X-CSRF-Token';
const STATE_CHANGING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

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
  // CSRF double-submit: echo the server-issued cookie in a header.
  // The server compares them in constant time; missing or mismatched
  // tokens get a 403 from requireCSRF before the handler runs.
  if (STATE_CHANGING.has(method.toUpperCase())) {
    const tok = readCookie(CSRF_COOKIE);
    if (tok) headers[CSRF_HEADER] = tok;
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
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'Content-Type': contentType
  };
  const tok = readCookie(CSRF_COOKIE);
  if (tok) headers[CSRF_HEADER] = tok;
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers,
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

export type ConnectionType =
  | 'ibm'
  | 'rabbitmq'
  | 'kafka'
  | 'mqtt'
  | 'nats'
  | 'amqp10';

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
  // Phase 22 — MQTT / NATS / AMQP 1.0
  client_id?: string;
  stream_name?: string;
  consumer_name?: string;
  qos?: number;
  // Kafka consumer-group override. Empty = auto-derive from brokers+topic.
  group_id?: string;
  // Broker TLS (Phase 17)
  tls_ca_file?: string;
  tls_cert_file?: string;
  tls_key_file?: string;
  tls_insecure_skip_verify?: boolean;
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
  hash?: string;
  prev_hash?: string;
}

export interface AuditDiff {
  audit_id: string;
  before: string;
  after: string;
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

// ─── api tokens ──────────────────────────────────────────────────

export interface APIToken {
  id: string;
  tenant_id: string;
  user_sub: string;
  name: string;
  prefix: string;        // first 8 chars of the user-visible secret
  role: Role;
  created_at: string;
  expires_at?: string | null;
  last_used_at?: string | null;
  revoked_at?: string | null;
}

// Response from POST /api/v1/tokens — `secret` is shown exactly once.
export interface APITokenCreateResponse {
  token: APIToken;
  secret: string;
  warning: string;
}

// ─── webhooks ────────────────────────────────────────────────────

export interface Webhook {
  id: string;
  tenant_id: string;
  name: string;
  url: string;
  secret: string;        // returned on list — the receiver needs it to verify HMAC
  events: string;        // "*" or "type1,type2"
  enabled: boolean;
  last_status: number;
  last_error?: string;
  last_attempt_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ─── config bundle (import/export) ───────────────────────────────

// Shape returned from GET /api/v1/config/export and accepted by the
// import endpoint. The UI doesn't need to round-trip every field —
// we display a summary on import dry-run and let the operator
// confirm or cancel.
export interface ConfigBundle {
  version: number;
  exported_at: string;
  tenant_slug: string;
  connections: Array<{ name: string; type: string }>;
  schemas?: Array<{ name: string }>;
  scripts?: Array<{ name: string }>;
  pipelines: Array<{ name: string; source_connection: string; dest_connection: string }>;
}

export interface ConfigImportResult {
  status: string;
  connections: number;
  pipelines: number;
  dry_run?: boolean;
}
