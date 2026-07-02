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
                @blur="handleUpdateSettings"
              />
            </NFormItem>
            <NFormItem label="测试 API Key">
              <NInput
                v-model:value="settings.test_api_key"
                placeholder="sk-ant-..."
                @blur="handleUpdateSettings"
              />
            </NFormItem>
            <NFormItem label="巡检问题">
              <NInput
                v-model:value="testQuestionsText"
                type="textarea"
                placeholder="每行一个问题，巡检时随机选择&#10;例如：&#10;你好&#10;1+1=?&#10;帮我写一首诗"
                :rows="4"
                @blur="handleUpdateSettings"
              />
              <template #feedback>
                <span style="font-size: 12px; color: #999;">
                  每行一个问题，巡检时随机选择其中一个
                </span>
              </template>
            </NFormItem>
          </NForm>

          <NSpace>
            <NButton secondary @click="handleClearLogs">
              清除日志
            </NButton>
          </NSpace>

          <!-- 实时日志 -->
          <NCard title="实时日志" size="small">
            <div class="logs-container">
              <div
                v-for="(log, index) in status.logs"
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
      </NCard>

      <!-- 已监控模型列表 -->
      <NCard title="已监控模型">
        <template #header-extra>
          <NButton type="primary" size="small" @click="showAddModel = true">
            添加模型
          </NButton>
        </template>

        <NDataTable
          :columns="columns"
          :data="trackedModels"
          :loading="loading"
          :pagination="false"
          size="small"
        />
      </NCard>
    </NSpace>

    <!-- 添加模型对话框 -->
    <NModal v-model:show="showAddModel" preset="dialog" title="添加模型到监控">
      <NForm :model="formModel" label-placement="left" label-width="140">
        <NFormItem label="模型 ID" path="model_id" required>
          <NInput v-model:value="formModel.model_id" placeholder="例如: claude-3-5-sonnet-20241022" />
        </NFormItem>
        <NFormItem label="Provider" path="provider">
          <NInput v-model:value="formModel.provider" placeholder="例如: anthropic" />
        </NFormItem>
        <NFormItem label="Cron 表达式" path="schedule_cron">
          <NInput v-model:value="formModel.schedule_cron" placeholder="例如: 0 */6 * * * (每6小时)" />
        </NFormItem>
      </NForm>

      <template #action>
        <NSpace>
          <NButton @click="showAddModel = false">取消</NButton>
          <NButton type="primary" @click="handleAddModel">添加</NButton>
        </NSpace>
      </template>
    </NModal>
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
  NModal,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSwitch,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import type { TrackedModel, ModelCheckerStatus, ModelCheckerConfig } from '@/shared/types/api'
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

const message = useMessage()
const { errorText } = useI18n()

const loading = ref(false)
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
})
const trackedModels = ref<TrackedModel[]>([])
const settings = ref<ModelCheckerConfig>({
  timeout_seconds: 30,
  test_api_key: '',
  test_questions: [],
})
const cronDrafts = ref<Record<string, string>>({})
const savingCronModelId = ref<string | null>(null)
const checkingModelIds = ref<Record<string, boolean>>({})

const testQuestionsText = computed({
  get: () => (settings.value.test_questions || []).join('\n'),
  set: (val: string) => {
    settings.value.test_questions = val
      .split('\n')
      .map(line => line.trim())
      .filter(line => line.length > 0)
  },
})

const showAddModel = ref(false)
const formModel = ref({
  model_id: '',
  provider: '',
  schedule_cron: '0 */6 * * *',
})

const columns: DataTableColumns<TrackedModel> = [
  {
    title: '模型 ID',
    key: 'model_id',
    width: 180,
  },
  {
    title: 'Provider',
    key: 'provider',
    width: 100,
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
    title: '最新状态',
    key: 'last_status',
    width: 100,
    render: (row) => {
      if (!row.last_status) return '-'
      const statusMap = {
        available: { text: '正常', type: 'success' as const },
        unavailable: { text: '异常', type: 'warning' as const },
        error: { text: '错误', type: 'error' as const },
      }
      const config = statusMap[row.last_status as keyof typeof statusMap] || { text: row.last_status, type: 'default' as const }
      return h(NTag, { type: config.type, size: 'small' }, { default: () => config.text })
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
    const [statusRes, modelsRes, settingsRes] = await Promise.all([
      getModelCheckerStatus(),
      getTrackedModels(),
      getModelCheckerSettings(),
    ])
    status.value = statusRes
    trackedModels.value = modelsRes
    syncCronDrafts(modelsRes)
    settings.value = settingsRes
  } catch (error) {
    message.error(errorText(error, '加载数据失败', 'Failed to load model checker data'))
  } finally {
    loading.value = false
  }
}

async function handleUpdateSettings() {
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
  if (!formModel.value.model_id) {
    message.error('请填写模型 ID')
    return
  }
  try {
    await addTrackedModel(formModel.value)
    message.success('模型已添加到监控')
    showAddModel.value = false
    formModel.value = {
      model_id: '',
      provider: '',
      schedule_cron: '0 */6 * * *',
    }
    await loadData()
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
  void loadData()
  pollTimer = window.setInterval(() => {
    void loadData()
  }, 3000)
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
</style>
