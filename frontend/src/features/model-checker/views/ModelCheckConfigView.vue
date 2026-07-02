<template>
  <div class="model-checker-config-view">
    <NSpace vertical :size="16">
      <!-- 概览卡片 -->
      <NCard>
        <div class="overview-grid">
          <div class="overview-item">
            <div class="overview-label">总模型数</div>
            <div class="overview-value">{{ status.stats.total_models }}</div>
          </div>
          <div class="overview-item success">
            <div class="overview-label">正常</div>
            <div class="overview-value">{{ status.stats.available_models }}</div>
          </div>
          <div class="overview-item warning">
            <div class="overview-label">异常</div>
            <div class="overview-value">{{ status.stats.unavailable_models }}</div>
          </div>
          <div class="overview-item error">
            <div class="overview-label">错误</div>
            <div class="overview-value">{{ status.stats.error_models }}</div>
          </div>
          <div class="overview-item queue">
            <div class="overview-label">队列中</div>
            <div class="overview-value">{{ status.queued_models.length }}</div>
          </div>
        </div>
      </NCard>

      <!-- 全局设置 -->
      <NCard title="全局设置">
        <NSpace vertical :size="12">
          <NForm label-placement="left" label-width="120">
            <NFormItem label="超时时间(秒)">
              <NInputNumber
                v-model:value="settings.timeout_seconds"
                :min="1"
                placeholder="30"
              />
            </NFormItem>
            <NFormItem label="测试 API Key">
              <NInput
                v-model:value="settings.test_api_key"
                placeholder="sk-ant-..."
              />
            </NFormItem>
            <NFormItem label="巡检问题">
              <NInput
                v-model:value="testQuestionsText"
                type="textarea"
                placeholder="每行一个问题，巡检时随机选择&#10;例如：&#10;你好&#10;1+1=?&#10;帮我写一首诗"
                :rows="4"
                @keydown.enter.stop
              />
              <template #feedback>
                <span style="font-size: 12px; color: #999;">
                  每行一个问题，巡检时随机选择其中一个
                </span>
              </template>
            </NFormItem>
          </NForm>

          <NSpace>
            <NButton type="primary" :loading="savingSettings" @click="handleUpdateSettings">
              保存设置
            </NButton>
            <NButton secondary @click="handleClearLogs">
              清除日志
            </NButton>
          </NSpace>
        </NSpace>
      </NCard>

      <!-- 已监控模型列表 -->
      <NCard title="已监控模型">
        <NSpace vertical :size="12">
          <NDataTable
            :columns="columns"
            :data="trackedModels"
            :loading="loading"
            :pagination="false"
            size="small"
          />

          <!-- 添加模型 -->
          <NSpace :size="12">
            <NSelect
              v-model:value="selectedModelId"
              :options="availableModelOptions"
              placeholder="选择模型"
              filterable
              style="width: 400px"
            />
            <NButton type="primary" :disabled="!selectedModelId" @click="handleAddModel">
              添加到监控
            </NButton>
          </NSpace>
        </NSpace>
      </NCard>

      <!-- 实时日志 -->
      <NCard title="实时日志">
        <div class="logs-container">
          <div
            v-for="(log, index) in reversedLogs"
            :key="index"
            :class="['log-line', getLogClass(log)]"
          >
            {{ log }}
          </div>
          <div v-if="status.logs.length === 0" class="empty-logs">
            暂无日志
          </div>
        </div>
      </NCard>
    </NSpace>
  </div>
</template>

<script setup lang="ts">
import { ref, h, onMounted, onUnmounted, computed } from 'vue'
import {
  NSpace,
  NCard,
  NButton,
  NTag,
  NDataTable,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSwitch,
  NSelect,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import type { TrackedModel, ModelCheckerStatus, ModelCheckerConfig, AvailableModel } from '@/shared/types/api'
import { useI18n } from '@/shared/i18n'
import {
  getModelCheckerSettings,
  updateModelCheckerSettings,
  getModelCheckerStatus,
  getTrackedModels,
  addTrackedModel,
  updateTrackedModel,
  deleteTrackedModel,
  checkTrackedModel,
  clearModelCheckerLogs,
} from '../api/modelCheckerApi'
import { listAvailableModels } from '@/features/models/api/availableModelsApi'

const message = useMessage()
const { errorText } = useI18n()

const loading = ref(false)
const savingSettings = ref(false)
const status = ref<ModelCheckerStatus>({
  running: false,
  running_modes: [],
  daemon_running: false,
  state: 'idle',
  detail: '',
  mode: null,
  last_started_at: null,
  last_finished_at: null,
  stats: {
    total_models: 0,
    available_models: 0,
    unavailable_models: 0,
    newly_available: 0,
    newly_unavailable: 0,
    error_models: 0,
  },
  logs: [],
  queued_models: [],
})
const trackedModels = ref<TrackedModel[]>([])
const availableModels = ref<AvailableModel[]>([])
const selectedModelId = ref('')
const settings = ref<ModelCheckerConfig>({
  timeout_seconds: 30,
  test_api_key: '',
  test_questions: [],
})
const cronDrafts = ref<Record<string, string>>({})
const savingCronModelId = ref<string | null>(null)
const checkingModelIds = ref<Record<string, boolean>>({})

const availableModelOptions = computed(() => {
  const trackedIds = new Set(trackedModels.value.map(m => m.model_id))
  return availableModels.value
    .filter(m => !trackedIds.has(m.id))
    .map(m => ({
      label: m.id,
      value: m.id,
    }))
})

const testQuestionsText = computed({
  get: () => (settings.value.test_questions || []).join('\n'),
  set: (val: string) => {
    settings.value.test_questions = val
      .split('\n')
      .map(line => line.trim())
      .filter(line => line.length > 0)
  },
})

const reversedLogs = computed(() => [...status.value.logs].reverse())

// Compute real-time status for each model
function getModelRuntimeStatus(modelId: string): { text: string; type: 'info' | 'success' | 'warning' | 'error' | 'default'; loading?: boolean } {
  // Priority 1: Checking
  if (status.value.running_modes.includes(modelId)) {
    return { text: '巡检中', type: 'info', loading: true }
  }

  // Priority 2: In queue
  const queueIndex = status.value.queued_models.indexOf(modelId)
  if (queueIndex !== -1) {
    return { text: `队列中 (第${queueIndex + 1}位)`, type: 'warning' }
  }

  // Priority 3: Last status
  const model = trackedModels.value.find(m => m.model_id === modelId)
  if (!model || !model.last_status) {
    return { text: '-', type: 'default' }
  }

  const statusMap = {
    available: { text: '正常', type: 'success' as const },
    unavailable: { text: '异常', type: 'warning' as const },
    error: { text: '错误', type: 'error' as const },
  }
  const config = statusMap[model.last_status as keyof typeof statusMap]
  return config || { text: model.last_status, type: 'default' }
}

const columns: DataTableColumns<TrackedModel> = [
  {
    title: '模型 ID',
    key: 'model_id',
    width: 180,
  },
  {
    title: '状态',
    key: 'status',
    width: 140,
    render: (row) => {
      const runtimeStatus = getModelRuntimeStatus(row.model_id)
      if (runtimeStatus.loading) {
        return h('div', { style: 'display: flex; align-items: center; gap: 6px;' }, [
          h('span', { class: 'status-spinner' }, '⟳'),
          h(NTag, { type: runtimeStatus.type, size: 'small' }, { default: () => runtimeStatus.text }),
        ])
      }
      return h(NTag, {
        type: runtimeStatus.type,
        size: 'small',
      }, { default: () => runtimeStatus.text })
    },
  },
  {
    title: 'Cron',
    key: 'schedule_cron',
    width: 220,
    render: (row) => h(NInput, {
      value: cronDrafts.value[row.model_id] ?? row.schedule_cron,
      size: 'small',
      disabled: savingCronModelId.value === row.model_id,
      placeholder: '例如: 0 */6 * * *',
      onUpdateValue: (value: string) => updateCronDraft(row.model_id, value),
      onBlur: () => {
        void handleSaveCron(row)
      },
      onKeydown: (event: KeyboardEvent) => {
        if (event.key === 'Enter') {
          event.preventDefault()
          void handleSaveCron(row)
          return
        }
        if (event.key === 'Escape') {
          resetCronDraft(row)
        }
      },
    }),
  },
  {
    title: '最后巡检',
    key: 'last_checked_at',
    width: 170,
    render: (row) => formatDateTime(row.last_checked_at),
  },
  {
    title: '最近可用时间',
    key: 'last_available_at',
    width: 170,
    render: (row) => formatDateTime(row.last_available_at),
  },
  {
    title: '下次巡检时间',
    key: 'next_run_at',
    width: 170,
    render: (row) => formatDateTime(row.next_run_at),
  },
  {
    title: '启用',
    key: 'enabled',
    width: 80,
    render: (row) => h(NSwitch, {
      value: row.enabled,
      onUpdateValue: (val) => handleToggleEnabled(row.model_id, val),
    }),
  },
  {
    title: '操作',
    key: 'actions',
    width: 170,
    render: (row) => h(NSpace, { size: 'small' }, {
      default: () => [
        h(NButton, {
          size: 'small',
          loading: checkingModelIds.value[row.model_id] === true,
          disabled: checkingModelIds.value[row.model_id] === true,
          onClick: () => {
            void handleCheck(row.model_id)
          },
        }, { default: () => '立即巡检' }),
        h(NButton, {
          size: 'small',
          type: 'error',
          onClick: () => {
            void handleDelete(row.model_id)
          },
        }, { default: () => '移除' }),
      ],
    }),
  },
]

function formatDateTime(value: string | null | undefined): string {
  if (!value) {
    return '-'
  }
  return new Date(value).toLocaleString('zh-CN')
}

function getLogClass(log: string): string {
  // 优先按状态码判断
  const statusCodeMatch = log.match(/响应状态:\s*(\d+)/)
  if (statusCodeMatch && statusCodeMatch[1]) {
    const code = parseInt(statusCodeMatch[1], 10)
    if (code >= 200 && code < 300) {
      return 'log-success'
    }
    if (code >= 400 && code < 500) {
      return 'log-warning'
    }
    if (code >= 500 && code < 600) {
      return 'log-error'
    }
  }

  // 回退到文本判断
  if (log.includes('状态: 正常') || log.includes('available')) {
    return 'log-success'
  }
  if (log.includes('状态: 异常') || log.includes('unavailable')) {
    return 'log-warning'
  }
  if (log.includes('状态: 错误') || log.includes('error') || log.includes('失败')) {
    return 'log-error'
  }
  return ''
}

function syncCronDrafts(models: TrackedModel[]) {
  const nextDrafts: Record<string, string> = {}
  models.forEach((model) => {
    nextDrafts[model.model_id] = cronDrafts.value[model.model_id] ?? model.schedule_cron
  })
  cronDrafts.value = nextDrafts
}

function updateCronDraft(modelId: string, value: string) {
  cronDrafts.value = {
    ...cronDrafts.value,
    [modelId]: value,
  }
}

function resetCronDraft(row: TrackedModel) {
  updateCronDraft(row.model_id, row.schedule_cron)
}

let pollTimer: number | null = null

async function loadData() {
  loading.value = true
  try {
    const [statusRes, modelsRes] = await Promise.all([
      getModelCheckerStatus(),
      getTrackedModels(),
    ])
    status.value = statusRes
    trackedModels.value = modelsRes
    syncCronDrafts(modelsRes)
  } catch (error) {
    message.error(errorText(error, '加载数据失败', 'Failed to load model checker data'))
  } finally {
    loading.value = false
  }
}

async function loadInitialData() {
  loading.value = true
  try {
    const [statusRes, modelsRes, settingsRes, availableRes] = await Promise.all([
      getModelCheckerStatus(),
      getTrackedModels(),
      getModelCheckerSettings(),
      listAvailableModels(),
    ])
    status.value = statusRes
    trackedModels.value = modelsRes
    syncCronDrafts(modelsRes)
    settings.value = settingsRes
    availableModels.value = availableRes.models
  } catch (error) {
    message.error(errorText(error, '加载数据失败', 'Failed to load model checker data'))
  } finally {
    loading.value = false
  }
}

async function handleUpdateSettings() {
  savingSettings.value = true
  try {
    const updated = await updateModelCheckerSettings({
      timeout_seconds: settings.value.timeout_seconds,
      test_api_key: settings.value.test_api_key,
      test_questions: settings.value.test_questions || [],
    })
    settings.value = updated
    message.success('设置已保存')
  } catch (error) {
    message.error(errorText(error, '保存失败', 'Failed to save settings'))
  } finally {
    savingSettings.value = false
  }
}

async function handleClearLogs() {
  try {
    await clearModelCheckerLogs()
    message.success('日志已清除')
    await loadData()
  } catch (error) {
    message.error(errorText(error, '清除失败', 'Failed to clear logs'))
  }
}

async function handleAddModel() {
  if (!selectedModelId.value) {
    message.error('请选择模型')
    return
  }
  try {
    // Find the selected model to get provider info
    const selectedModel = availableModels.value.find(m => m.id === selectedModelId.value)

    // Try to get provider from price info, or extract from model id, or default to empty
    let provider = ''
    if (selectedModel?.price?.provider) {
      provider = selectedModel.price.provider
    } else {
      // Try to extract from model id (e.g., "anthropic/claude-3-5-sonnet" -> "anthropic")
      const parts = selectedModelId.value.split('/')
      if (parts.length > 1 && parts[0]) {
        provider = parts[0]
      }
    }

    await addTrackedModel({
      model_id: selectedModelId.value,
      provider: provider,
      schedule_cron: '0 */6 * * *',
    })
    message.success('模型已添加到监控')
    selectedModelId.value = ''
    await loadData()

    // Reload available models to update the dropdown
    try {
      const availableRes = await listAvailableModels()
      availableModels.value = availableRes.models
    } catch {
      // Silently fail if available models reload fails
    }
  } catch (error) {
    message.error(errorText(error, '添加失败', 'Failed to add model'))
  }
}

async function handleSaveCron(row: TrackedModel) {
  const draft = (cronDrafts.value[row.model_id] ?? row.schedule_cron).trim()
  if (!draft) {
    message.error('Cron 表达式不能为空')
    resetCronDraft(row)
    return
  }
  if (draft === row.schedule_cron) {
    resetCronDraft(row)
    return
  }

  savingCronModelId.value = row.model_id
  try {
    await updateTrackedModel(row.model_id, {
      schedule_cron: draft,
    })
    message.success('Cron 已更新')
    await loadData()
  } catch (error) {
    resetCronDraft(row)
    message.error(errorText(error, 'Cron 更新失败', 'Failed to update cron'))
  } finally {
    savingCronModelId.value = null
  }
}

async function handleToggleEnabled(modelId: string, enabled: boolean) {
  try {
    await updateTrackedModel(modelId, { enabled })
    message.success(enabled ? '已启用' : '已停用')
    await loadData()
  } catch (error) {
    message.error(errorText(error, '操作失败', 'Failed to update model status'))
  }
}

async function handleCheck(modelId: string) {
  checkingModelIds.value = {
    ...checkingModelIds.value,
    [modelId]: true,
  }
  try {
    await checkTrackedModel(modelId)
    message.success('已开始巡检')
    await loadData()
  } catch (error) {
    message.error(errorText(error, '巡检启动失败', 'Failed to start inspection'))
  } finally {
    checkingModelIds.value = {
      ...checkingModelIds.value,
      [modelId]: false,
    }
  }
}

async function handleDelete(modelId: string) {
  try {
    await deleteTrackedModel(modelId)
    message.success('模型已从监控移除')
    await loadData()
  } catch (error) {
    message.error(errorText(error, '移除失败', 'Failed to remove model'))
  }
}

onMounted(() => {
  void loadInitialData()
  pollTimer = window.setInterval(() => {
    void loadData()
  }, 10000)
})

onUnmounted(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
  }
})
</script>

<style scoped>
.model-checker-config-view {
  padding: 16px;
}

/* 概览样式 */
.overview-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
}

.overview-item {
  padding: 20px;
  border-radius: 8px;
  background: linear-gradient(135deg, #f5f5f5 0%, #e8e8e8 100%);
  border-left: 4px solid #d0d0d0;
  transition: all 0.3s ease;
}

.overview-item:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.overview-item.success {
  background: linear-gradient(135deg, #f0fdf4 0%, #dcfce7 100%);
  border-left-color: #22c55e;
}

.overview-item.warning {
  background: linear-gradient(135deg, #fefce8 0%, #fef08a 100%);
  border-left-color: #eab308;
}

.overview-item.error {
  background: linear-gradient(135deg, #fef2f2 0%, #fecaca 100%);
  border-left-color: #ef4444;
}

.overview-item.queue {
  background: linear-gradient(135deg, #eff6ff 0%, #dbeafe 100%);
  border-left-color: #3b82f6;
}

.overview-label {
  font-size: 14px;
  color: #666;
  margin-bottom: 8px;
  font-weight: 500;
}

.overview-value {
  font-size: 32px;
  font-weight: 700;
  color: #333;
}

.overview-item.success .overview-value {
  color: #16a34a;
}

.overview-item.warning .overview-value {
  color: #ca8a04;
}

.overview-item.error .overview-value {
  color: #dc2626;
}

.overview-item.queue .overview-value {
  color: #2563eb;
}

/* 日志样式 */
.logs-container {
  max-height: 300px;
  overflow-y: auto;
  font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
  font-size: 12px;
  background-color: #1e1e1e;
  color: #d4d4d4;
  padding: 12px;
  border-radius: 4px;
}

.log-line {
  margin-bottom: 4px;
  white-space: nowrap;
  line-height: 1.6;
}

.log-line.log-success {
  color: #4ade80;
}

.log-line.log-warning {
  color: #fbbf24;
}

.log-line.log-error {
  color: #f87171;
}

.empty-logs {
  color: #999;
  text-align: center;
  padding: 20px;
}

/* 状态加载动画 */
@keyframes spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

.status-spinner {
  display: inline-block;
  animation: spin 1s linear infinite;
  font-size: 16px;
  color: #3b82f6;
}
</style>
