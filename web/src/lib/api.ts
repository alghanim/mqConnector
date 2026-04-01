import PocketBase from 'pocketbase'

export const pb = new PocketBase(window.location.origin)

// Auth helper
export async function login(email: string, password: string) {
  return pb.admins.authWithPassword(email, password)
}

export function logout() {
  pb.authStore.clear()
}

export function isAuthenticated() {
  return pb.authStore.isValid
}

export function getAuthToken() {
  return pb.authStore.token
}

// Health
export async function fetchHealth() {
  const resp = await fetch('/api/health', {
    headers: { Authorization: pb.authStore.token },
  })
  return resp.json()
}

// Metrics
export async function fetchMetrics() {
  const resp = await fetch('/api/metrics', {
    headers: { Authorization: pb.authStore.token },
  })
  return resp.json()
}

// DLQ
export async function fetchDLQ(page = 1, perPage = 20) {
  const resp = await fetch(`/api/dlq?page=${page}&perPage=${perPage}`, {
    headers: { Authorization: pb.authStore.token },
  })
  return resp.json()
}

export async function retryDLQ(id: string) {
  const resp = await fetch(`/api/dlq/${id}/retry`, {
    method: 'POST',
    headers: { Authorization: pb.authStore.token },
  })
  return resp.json()
}

export async function deleteDLQ(id: string) {
  const resp = await fetch(`/api/dlq/${id}`, {
    method: 'DELETE',
    headers: { Authorization: pb.authStore.token },
  })
  return resp.json()
}

// MQ Connections
export async function fetchConnections() {
  return pb.collection('MQS').getFullList({ expand: 'type' })
}

// MQ Filters
export async function fetchFilters() {
  return pb.collection('MQ_FILTERS').getFullList({
    expand: 'source,destination,FieldPath',
  })
}

// Templates
export async function fetchTemplates() {
  return pb.collection('Templates').getFullList()
}

// Transforms
export async function fetchTransforms(filterId?: string) {
  const filter = filterId ? `filter_id = "${filterId}"` : ''
  return pb.collection('MQ_TRANSFORMS').getFullList({ filter, sort: 'order' })
}

// Routing Rules
export async function fetchRoutingRules(filterId?: string) {
  const filter = filterId ? `filter_id = "${filterId}"` : ''
  return pb.collection('MQ_ROUTING_RULES').getFullList({
    filter,
    expand: 'destination',
    sort: 'priority',
  })
}

// Pipeline Stages
export async function fetchPipelineStages(filterId?: string) {
  const filter = filterId ? `filter_id = "${filterId}"` : ''
  return pb.collection('MQ_PIPELINE_STAGES').getFullList({
    filter,
    sort: 'stage_order',
  })
}

// Schemas
export async function fetchSchemas() {
  return pb.collection('MQ_SCHEMAS').getFullList()
}

// Scripts
export async function fetchScripts() {
  return pb.collection('MQ_SCRIPTS').getFullList()
}

// Bridge
export async function bridgePublish(connectionId: string, body: string) {
  const resp = await fetch(`/api/bridge/publish/${connectionId}`, {
    method: 'POST',
    headers: {
      Authorization: pb.authStore.token,
      'Content-Type': 'application/json',
    },
    body,
  })
  return resp.json()
}

export async function bridgeConsume(connectionId: string) {
  const resp = await fetch(`/api/bridge/consume/${connectionId}`, {
    method: 'POST',
    headers: { Authorization: pb.authStore.token },
  })
  const contentType = resp.headers.get('content-type') || ''
  if (contentType.includes('json')) return resp.json()
  return resp.text()
}
