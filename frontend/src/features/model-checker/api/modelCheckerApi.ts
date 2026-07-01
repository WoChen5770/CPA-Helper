import type {
  ModelCheckerConfig,
  ModelCheckerStatus,
  TrackedModel,
} from '@/shared/types/api'
import { getBasePath } from '@/shared/api/http'

const BASE_URL = `${getBasePath()}/api/model-checker`

export async function getModelCheckerSettings(): Promise<ModelCheckerConfig> {
  const response = await fetch(`${BASE_URL}/settings`, {
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to load settings')
  }
  return response.json()
}

export async function updateModelCheckerSettings(
  payload: Partial<ModelCheckerConfig>
): Promise<ModelCheckerConfig> {
  const response = await fetch(`${BASE_URL}/settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(payload),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to update settings')
  }
  return response.json()
}

export async function getModelCheckerStatus(): Promise<ModelCheckerStatus> {
  const response = await fetch(`${BASE_URL}/status`, {
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to load status')
  }
  return response.json()
}

export async function runModelCheckerOnce(): Promise<void> {
  const response = await fetch(`${BASE_URL}/run-once`, {
    method: 'POST',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to start check')
  }
}

export async function startModelChecker(): Promise<void> {
  const response = await fetch(`${BASE_URL}/start`, {
    method: 'POST',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to start daemon')
  }
}

export async function stopModelChecker(): Promise<void> {
  const response = await fetch(`${BASE_URL}/stop`, {
    method: 'POST',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to stop daemon')
  }
}

export async function clearModelCheckerLogs(): Promise<void> {
  const response = await fetch(`${BASE_URL}/logs/clear`, {
    method: 'POST',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to clear logs')
  }
}

export async function getTrackedModels(): Promise<TrackedModel[]> {
  const response = await fetch(`${BASE_URL}/models`, {
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to load models')
  }
  return response.json()
}

export async function addTrackedModel(payload: {
  model_id: string
  provider: string
  check_interval_minutes?: number
  timeout_seconds?: number
  max_retries?: number
  alert_on_unavailable?: boolean
}): Promise<void> {
  const response = await fetch(`${BASE_URL}/models`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(payload),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to add model')
  }
}

export async function getTrackedModel(modelId: string): Promise<TrackedModel> {
  const response = await fetch(`${BASE_URL}/models/${encodeURIComponent(modelId)}`, {
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to load model')
  }
  return response.json()
}

export async function updateTrackedModel(
  modelId: string,
  payload: {
    enabled?: boolean
    check_interval_minutes?: number
    timeout_seconds?: number
    max_retries?: number
    alert_on_unavailable?: boolean
  }
): Promise<void> {
  const response = await fetch(`${BASE_URL}/models/${encodeURIComponent(modelId)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(payload),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to update model')
  }
}

export async function deleteTrackedModel(modelId: string): Promise<void> {
  const response = await fetch(`${BASE_URL}/models/${encodeURIComponent(modelId)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to delete model')
  }
}

export async function checkTrackedModel(modelId: string): Promise<void> {
  const response = await fetch(`${BASE_URL}/models/${encodeURIComponent(modelId)}/check`, {
    method: 'POST',
    credentials: 'include',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: { message: 'Unknown error' } }))
    throw new Error(error.detail?.message || 'Failed to check model')
  }
}
