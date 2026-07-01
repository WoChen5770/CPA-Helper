<script setup lang="ts">
import { ref, onMounted, computed, h } from 'vue'
import {
  NCard,
  NSpace,
  NButton,
  NSwitch,
  NDataTable,
  NInput,
  NInputNumber,
  NSelect,
  NTag,
  NStatistic,
  useMessage,
  useDialog
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { TrackedModel, ModelCheckerConfig } from '@/shared/types/api'
import {
  getModelCheckerSettings,
  updateModelCheckerSettings,
  getModelCheckerStatus,
  getTrackedModels,
  addTrackedModel,
  updateTrackedModel,
  deleteTrackedModel,
  startModelSchedule,
  stopModelSchedule,
  clearModelCheckerLogs,
} from '../api/modelCheckerApi'
import { listAvailableModels } from '@/features/models/api/availableModelsApi'
import type { AvailableModel } from '@/shared/types/api'

const { t } = useI18n()
const message = useMessage()
const dialog = useDialog()

const loading = ref(false)
const statusLoading = ref(false)
const daemonRunning = ref(false)
const logs = ref<string[]>([])
const trackedModels = ref<TrackedModel[]>([])
const stats = ref({
  total: 0,
  available: 0,
  unavailable: 0,
  error: 0,
})
const globalConfig = ref<ModelCheckerConfig>({
  timeout_seconds: 30,
  test_api_key: '',
})
const availableModels = ref<AvailableModel[]>([])
const selectedModelId = ref('')

let statusInterval: number | null = null

onMounted(async () => {
  await loadGlobalConfig()
  await loadTrackedModels()
  await loadAvailableModels()
  await refreshStatus()
  statusInterval = window.setInterval(refreshStatus, 10000)
})

const loadGlobalConfig = async () => {
  try {
    globalConfig.value = await getModelCheckerSettings()
  } catch (error: any) {
    message.error(error.message || t('加载全局配置失败', 'Failed to load global config'))
  }
}

const handleSaveGlobalConfig = async () => {
  try {
    await updateModelCheckerSettings(globalConfig.value)
    message.success(t('全局配置已保存', 'Global config saved'))
  } catch (error: any) {
    message.error(error.message || t('保存失败', 'Failed to save'))
  }
}

const loadTrackedModels = async () => {
  loading.value = true
  try {
    trackedModels.value = await getTrackedModels()

    // Calculate stats
    stats.value = {
      total: trackedModels.value.length,
      available: trackedModels.value.filter(m => m.last_status === 'available').length,
      unavailable: trackedModels.value.filter(m => m.last_status === 'unavailable').length,
      error: trackedModels.value.filter(m => m.last_status === 'error').length,
    }
  } catch (error: any) {
    message.error(error.message || t('加载失败', 'Failed to load'))
  } finally {
    loading.value = false
  }
}

const loadAvailableModels = async () => {
  try {
    const response = await listAvailableModels()
    availableModels.value = response.models
  } catch (error: any) {
    message.error(error.message || t('加载可用模型失败', 'Failed to load available models'))
  }
}

const refreshStatus = async () => {
  statusLoading.value = true
  try {
    const status = await getModelCheckerStatus()
    daemonRunning.value = status.daemon_running
    logs.value = status.logs.slice().reverse() // 倒序显示日志
  } catch (error: any) {
    // 静默失败
  } finally {
    statusLoading.value = false
  }
}

const handleClearLogs = async () => {
  try {
    await clearModelCheckerLogs()
    message.success(t('日志已清除', 'Logs cleared'))
    logs.value = []
  } catch (error: any) {
    message.error(error.message || t('清除失败', 'Failed to clear'))
  }
}

const handleAddModel = async () => {
  if (!selectedModelId.value) {
    message.warning(t('请选择模型', 'Please select a model'))
    return
  }

  const model = availableModels.value.find(m => m.id === selectedModelId.value)
  if (!model) return

  try {
    await addTrackedModel({
      model_id: model.id,
      provider: model.owner || 'unknown',
      schedule_cron: '0 * * * *',
    })
    message.success(t('模型已添加到监控', 'Model added to monitoring'))
    selectedModelId.value = ''
    await loadTrackedModels()
  } catch (error: any) {
    message.error(error.message || t('添加失败', 'Failed to add'))
  }
}

const handleUpdateModel = async (model: TrackedModel) => {
  try {
    await updateTrackedModel(model.model_id, {
      enabled: model.enabled,
      schedule_cron: model.schedule_cron,
    })
    message.success(t('配置已保存', 'Configuration saved'))
    await loadTrackedModels()
  } catch (error: any) {
    message.error(error.message || t('保存失败', 'Failed to save'))
  }
}

const handleStartSchedule = async (modelId: string) => {
  try {
    await startModelSchedule(modelId)
    message.success(t('调度已启动', 'Schedule started'))
    await refreshStatus()
  } catch (error: any) {
    message.error(error.message || t('启动失败', 'Failed to start'))
  }
}

const handleStopSchedule = async (modelId: string) => {
  try {
    await stopModelSchedule(modelId)
    message.success(t('调度已停止', 'Schedule stopped'))
    await refreshStatus()
  } catch (error: any) {
    message.error(error.message || t('停止失败', 'Failed to stop'))
  }
}

const handleDeleteModel = (modelId: string) => {
  dialog.warning({
    title: t('确认删除', 'Confirm Delete'),
    content: t('确定要从监控列表移除此模型吗？', 'Are you sure to remove this model from monitoring?'),
    positiveText: t('确定', 'Confirm'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      try {
        await deleteTrackedModel(modelId)
        message.success(t('模型已从监控移除', 'Model removed from monitoring'))
        await loadTrackedModels()
      } catch (error: any) {
        message.error(error.message || t('删除失败', 'Failed to delete'))
      }
    },
  })
}

const getLogClass = (log: string) => {
  if (log.includes('状态: 正常')) {
    return 'log-success'
  } else if (log.includes('状态: 异常')) {
    return 'log-warning'
  } else if (log.includes('状态: 错误')) {
    return 'log-error'
  }
  return ''
}

const getStatusType = (status: string | null) => {
  switch (status) {
    case 'available':
      return 'success'
    case 'unavailable':
      return 'error'
    case 'error':
      return 'warning'
    default:
      return 'default'
  }
}

const getStatusText = (status: string | null) => {
  switch (status) {
    case 'available':
      return t('正常', 'Available')
    case 'unavailable':
      return t('异常', 'Unavailable')
    case 'error':
      return t('错误', 'Error')
    default:
      return status || t('未知', 'Unknown')
  }
}

const formatTime = (time: string | null) => {
  if (!time) return t('从未', 'Never')
  try {
    return new Date(time).toLocaleString()
  } catch {
    return t('无效', 'Invalid')
  }
}

const availableModelOptions = computed(() => {
  const trackedIds = new Set(trackedModels.value.map(m => m.model_id))
  return availableModels.value
    .filter(m => !trackedIds.has(m.id))
    .map(m => ({
      label: m.id,
      value: m.id,
    }))
})

const columns: DataTableColumns<TrackedModel> = [
  {
    title: () => t('模型 ID', 'Model ID'),
    key: 'model_id',
    width: 280,
    ellipsis: {
      tooltip: true,
    },
  },
  {
    title: () => t('Provider', 'Provider'),
    key: 'provider',
    width: 120,
  },
  {
    title: () => t('状态', 'Status'),
    key: 'last_status',
    width: 100,
    render: (row) => {
      return h(NTag, {
        type: getStatusType(row.last_status),
        size: 'small',
      }, {
        default: () => getStatusText(row.last_status),
      })
    },
  },
  {
    title: () => t('启用', 'Enabled'),
    key: 'enabled',
    width: 80,
    render: (row) => {
      return h(NSwitch, {
        value: row.enabled,
        onUpdateValue: (value: boolean) => {
          row.enabled = value
          handleUpdateModel(row)
        },
      })
    },
  },
  {
    title: () => t('Cron 表达式', 'Schedule Cron'),
    key: 'schedule_cron',
    width: 140,
    render: (row) => {
      return h(NInput, {
        value: row.schedule_cron,
        size: 'small',
        onUpdateValue: (value: string) => {
          row.schedule_cron = value
        },
        onBlur: () => handleUpdateModel(row),
      })
    },
  },
  {
    title: () => t('最后可用时间', 'Last Available'),
    key: 'last_available_at',
    width: 180,
    render: (row) => formatTime(row.last_available_at),
  },
  {
    title: () => t('最后巡检时间', 'Last Checked'),
    key: 'last_checked_at',
    width: 180,
    render: (row) => formatTime(row.last_checked_at),
  },
  {
    title: () => t('下次巡检时间', 'Next Run'),
    key: 'next_run_at',
    width: 180,
    render: (row) => formatTime(row.next_run_at),
  },
  {
    title: () => t('操作', 'Actions'),
    key: 'actions',
    width: 100,
    render: (row) => {
      return h(
        NButton,
        {
          size: 'small',
          type: 'error',
          onClick: () => handleDeleteModel(row.model_id),
        },
        { default: () => t('移除', 'Remove') }
      )
    },
  },
]
</script>

<script lang="ts">
export default { name: 'ModelCheckConfigView' }
</script>

<template>
  <div class="model-check-config-view">
    <NSpace vertical :size="16">
      <!-- 概览 -->
      <NCard :title="t('概览', 'Overview')">
        <div style="display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px">
          <NCard :bordered="false">
            <NStatistic
              :label="t('总监控模型', 'Total Models')"
              :value="stats.total"
            />
          </NCard>
          <NCard :bordered="false" style="background: #f0f9ff">
            <NStatistic
              :label="t('正常模型', 'Available Models')"
              :value="stats.available"
            />
          </NCard>
          <NCard :bordered="false" style="background: #fef2f2">
            <NStatistic
              :label="t('异常模型', 'Unavailable Models')"
              :value="stats.unavailable"
            />
          </NCard>
          <NCard :bordered="false" style="background: #fffbeb">
            <NStatistic
              :label="t('错误模型', 'Error Models')"
              :value="stats.error"
            />
          </NCard>
        </div>
      </NCard>

      <!-- 全局配置 -->
      <NCard :title="t('全局配置', 'Global Configuration')">
        <NSpace vertical :size="12">
          <div class="config-item">
            <label>{{ t('测试 API Key', 'Test API Key') }}</label>
            <NInput
              v-model:value="globalConfig.test_api_key"
              type="text"
              :placeholder="t('用于测试模型可用性的专用 Key', 'Dedicated key for testing model availability')"
              style="width: 400px"
            />
          </div>
          <div class="config-item">
            <label>{{ t('超时时间 (秒)', 'Timeout (seconds)') }}</label>
            <NInputNumber
              v-model:value="globalConfig.timeout_seconds"
              :min="1"
              :max="300"
              style="width: 150px"
            />
          </div>
          <NButton type="primary" @click="handleSaveGlobalConfig">
            {{ t('保存配置', 'Save Configuration') }}
          </NButton>
        </NSpace>
      </NCard>

      <!-- 监控模型 -->
      <NCard :title="t('监控模型', 'Monitored Models')">
        <NSpace vertical :size="16">
          <NDataTable
            :columns="columns"
            :data="trackedModels"
            :loading="loading"
            :pagination="false"
          />

          <!-- 添加模型表单 -->
          <div style="padding-top: 16px; border-top: 1px solid #e0e0e0">
            <NSpace align="center">
              <span style="font-weight: 500">{{ t('添加模型', 'Add Model') }}:</span>
              <NSelect
                v-model:value="selectedModelId"
                :options="availableModelOptions"
                :placeholder="t('选择模型', 'Select model')"
                filterable
                style="width: 400px"
              />
              <NButton type="primary" @click="handleAddModel">
                {{ t('添加', 'Add') }}
              </NButton>
            </NSpace>
          </div>
        </NSpace>
      </NCard>

      <!-- 日志 -->
      <NCard :title="t('日志', 'Logs')">
        <NSpace vertical :size="12">
          <NButton size="small" @click="handleClearLogs">
            {{ t('清除日志', 'Clear Logs') }}
          </NButton>
          <div class="logs-container">
            <div v-for="(log, index) in logs" :key="index" class="log-line" :class="getLogClass(log)">
              {{ log }}
            </div>
            <div v-if="logs.length === 0" class="log-line empty">
              {{ t('暂无日志', 'No logs') }}
            </div>
          </div>
        </NSpace>
      </NCard>
    </NSpace>
  </div>
</template>

<style scoped>
.model-check-config-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
}

.config-item {
  display: flex;
  align-items: center;
  gap: 12px;
}

.config-item label {
  min-width: 180px;
  font-weight: 500;
}

.logs-container {
  max-height: 400px;
  overflow-y: auto;
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 13px;
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
}

.log-line {
  padding: 2px 0;
  color: #333;
}

.log-line.empty {
  color: #999;
  font-style: italic;
}

/* 日志颜色 - 正常：绿色 */
.log-line.log-success {
  color: #18a058;
}

/* 日志颜色 - 异常：黄色/橙色 */
.log-line.log-warning {
  color: #f0a020;
}

/* 日志颜色 - 错误：红色 */
.log-line.log-error {
  color: #d03050;
}
</style>
