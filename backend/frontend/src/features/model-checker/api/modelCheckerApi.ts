import { apiClient } from '@/shared/api/client'
import type {
  ModelCheckerConfig,
  ModelCheckerStatus,
  TrackedModel,
  ModelCheckRun,
} from '@/shared/types/api'

// Settings
export async function getModelCheckerSettings() {
  return apiClient.get<ModelCheckerConfig>('/api/model-checker/settings')
}

export async function updateModelCheckerSettings(payload: Partial<ModelCheckerConfig>) {
  return apiClient.put<ModelCheckerConfig>('/api/model-checker/settings', payload)
}

// Status
export async function getModelCheckerStatus() {
  return apiClient.get<ModelCheckerStatus>('/api/model-checker/status')
}

// Control
export async function runModelCheckerOnce() {
  return apiClient.post<{ message: string }>('/api/model-checker/run-once', {})
}

export async function startModelChecker() {
  return apiClient.post<{ message: string }>('/api/model-checker/start', {})
}

export async function stopModelChecker() {
  return apiClient.post<{ message: string }>('/api/model-checker/stop', {})
}

// Logs
export async function clearModelCheckerLogs() {
  return apiClient.post<{ message: string }>('/api/model-checker/logs/clear', {})
}

// Models
export async function getTrackedModels() {
  return apiClient.get<TrackedModel[]>('/api/model-checker/models')
}

export async function addTrackedModel(payload: {
  model_id: string
  provider: string
  check_interval_minutes?: number
  timeout_seconds?: number
  max_retries?: number
  alert_on_unavailable?: boolean
}) {
  return apiClient.post<{ message: string }>('/api/model-checker/models', payload)
}

export async function getTrackedModel(modelId: string) {
  return apiClient.get<TrackedModel>(`/api/model-checker/models/${encodeURIComponent(modelId)}`)
}

export async function updateTrackedModel(
  modelId: string,
  payload: {
    enabled?: boolean
    check_interval_minutes?: number
    timeout_seconds?: number
    max_retries?: number
    alert_on_unavailable?: boolean
  },
) {
  return apiClient.put<{ message: string }>(
    `/api/model-checker/models/${encodeURIComponent(modelId)}`,
    payload,
  )
}

export async function deleteTrackedModel(modelId: string) {
  return apiClient.delete<{ message: string }>(
    `/api/model-checker/models/${encodeURIComponent(modelId)}`,
  )
}

export async function checkTrackedModel(modelId: string) {
  return apiClient.post<{ message: string }>(
    `/api/model-checker/models/${encodeURIComponent(modelId)}/check`,
    {},
  )
}

// Runs history
export async function getModelCheckRuns() {
  return apiClient.get<ModelCheckRun[]>('/api/model-checker/runs')
}

export async function getModelCheckRun(runId: number) {
  return apiClient.get<ModelCheckRun>(`/api/model-checker/runs/${runId}`)
}
