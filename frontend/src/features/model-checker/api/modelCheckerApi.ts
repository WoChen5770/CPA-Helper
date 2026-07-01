import type {
  ModelCheckerConfig,
  ModelCheckerStatus,
  TrackedModel,
} from '@/shared/types/api'
import { apiClient } from '@/shared/api/apiClient'

export async function getModelCheckerSettings(): Promise<ModelCheckerConfig> {
  return apiClient.get<ModelCheckerConfig>('/model-checker/settings')
}

export async function updateModelCheckerSettings(
  payload: Partial<ModelCheckerConfig>
): Promise<ModelCheckerConfig> {
  return apiClient.put<ModelCheckerConfig>('/model-checker/settings', payload)
}

export async function getModelCheckerStatus(): Promise<ModelCheckerStatus> {
  return apiClient.get<ModelCheckerStatus>('/model-checker/status')
}

export async function clearModelCheckerLogs(): Promise<void> {
  return apiClient.post<void>('/model-checker/logs/clear')
}

export async function getTrackedModels(): Promise<TrackedModel[]> {
  return apiClient.get<TrackedModel[]>('/model-checker/models')
}

export async function addTrackedModel(payload: {
  model_id: string
  provider: string
  schedule_cron?: string
}): Promise<void> {
  return apiClient.post<void>('/model-checker/models', payload)
}

export async function getTrackedModel(modelId: string): Promise<TrackedModel> {
  return apiClient.get<TrackedModel>(`/model-checker/models/${encodeURIComponent(modelId)}`)
}

export async function updateTrackedModel(
  modelId: string,
  payload: {
    enabled?: boolean
    schedule_cron?: string
  }
): Promise<void> {
  return apiClient.put<void>(`/model-checker/models/${encodeURIComponent(modelId)}`, payload)
}

export async function deleteTrackedModel(modelId: string): Promise<void> {
  return apiClient.delete(`/model-checker/models/${encodeURIComponent(modelId)}`)
}

export async function startModelSchedule(modelId: string): Promise<void> {
  return apiClient.post<void>(`/model-checker/models/${encodeURIComponent(modelId)}/start`)
}

export async function stopModelSchedule(modelId: string): Promise<void> {
  return apiClient.post<void>(`/model-checker/models/${encodeURIComponent(modelId)}/stop`)
}

