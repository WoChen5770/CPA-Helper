<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { NCard, NSpace, NButton, NSwitch, NDataTable, NInput, NInputNumber, NTag, useMessage, useDialog } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { TrackedModel } from '@/shared/types/api'
import {
  getModelCheckerStatus,
  getTrackedModels,
  addTrackedModel,
  updateTrackedModel,
  deleteTrackedModel,
  checkTrackedModel,
  startModelChecker,
  stopModelChecker,
  runModelCheckerOnce,
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
const availableModels = ref<AvailableModel[]>([])
const selectedModelId = ref('')

// 轮询获取状态
let statusInterval: number | null = null

onMounted(async () => {
  await loadTrackedModels()
  await loadAvailableModels()
  await refreshStatus()
  // 每3秒刷新一次状态
  statusInterval = window.setInterval(refreshStatus, 3000)
})

const loadTrackedModels = async () => {
  loading.value = true
  try {
    trackedModels.value = await getTrackedModels()
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
    logs.value = status.logs
  } catch (error: any) {
    // 静默失败，避免频繁弹窗
  } finally {
    statusLoading.value = false
  }
}

const handleStartDaemon = async () => {
  try {
    await startModelChecker()
    message.success(t('Daemon 已启动', 'Daemon started'))
    await refreshStatus()
  } catch (error: any) {
    message.error(error.message || t('启动失败', 'Failed to start'))
  }
}

const handleStopDaemon = async () => {
  try {
    await stopModelChecker()
    message.success(t('Daemon 已停止', 'Daemon stopped'))
    await refreshStatus()
  } catch (error: any) {
    message.error(error.message || t('停止失败', 'Failed to stop'))
  }
}

const handleRunOnce = async () => {
  try {
    await runModelCheckerOnce()
    message.success(t('检查已启动', 'Check started'))
    setTimeout(refreshStatus, 1000)
  } catch (error: any) {
    message.error(error.message || t('启动失败', 'Failed to start'))
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
      check_interval_minutes: model.check_interval_minutes,
      timeout_seconds: model.timeout_seconds,
      max_retries: model.max_retries,
      alert_on_unavailable: model.alert_on_unavailable,
    })
    message.success(t('配置已保存', 'Configuration saved'))
    await loadTrackedModels()
  } catch (error: any) {
    message.error(error.message || t('保存失败', 'Failed to save'))
  }
}

const handleCheckModel = async (modelId: string) => {
  try {
    await checkTrackedModel(modelId)
    message.success(t('检查已启动', 'Check started'))
    setTimeout(refreshStatus, 1000)
  } catch (error: any) {
    message.error(error.message || t('启动失败', 'Failed to start'))
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
    width: 250,
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
    title: () => t('巡检间隔(分钟)', 'Interval (min)'),
    key: 'check_interval_minutes',
    width: 140,
    render: (row) => {
      return h(NInputNumber, {
        value: row.check_interval_minutes,
        min: 1,
        max: 1440,
        size: 'small',
        onUpdateValue: (value: number | null) => {
          if (value) row.check_interval_minutes = value
        },
      })
    },
  },
  {
    title: () => t('超时(秒)', 'Timeout (s)'),
    key: 'timeout_seconds',
    width: 120,
    render: (row) => {
      return h(NInputNumber, {
        value: row.timeout_seconds,
        min: 1,
        max: 300,
        size: 'small',
        onUpdateValue: (value: number | null) => {
          if (value) row.timeout_seconds = value
        },
      })
    },
  },
  {
    title: () => t('最大重试', 'Max Retries'),
    key: 'max_retries',
    width: 100,
    render: (row) => {
      return h(NInputNumber, {
        value: row.max_retries,
        min: 0,
        max: 10,
        size: 'small',
        onUpdateValue: (value: number | null) => {
          if (value !== null) row.max_retries = value
        },
      })
    },
  },
  {
    title: () => t('告警', 'Alert'),
    key: 'alert_on_unavailable',
    width: 80,
    render: (row) => {
      return h(NSwitch, {
        value: row.alert_on_unavailable,
        onUpdateValue: (value: boolean) => {
          row.alert_on_unavailable = value
          handleUpdateModel(row)
        },
      })
    },
  },
  {
    title: () => t('操作', 'Actions'),
    key: 'actions',
    width: 200,
    render: (row) => {
      return h(
        NSpace,
        { size: 'small' },
        {
          default: () => [
            h(
              NButton,
              {
                size: 'small',
                onClick: () => handleUpdateModel(row),
              },
              { default: () => t('保存', 'Save') }
            ),
            h(
              NButton,
              {
                size: 'small',
                onClick: () => handleCheckModel(row.model_id),
              },
              { default: () => t('立即检查', 'Check Now') }
            ),
            h(
              NButton,
              {
                size: 'small',
                type: 'error',
                onClick: () => handleDeleteModel(row.model_id),
              },
              { default: () => t('移除', 'Remove') }
            ),
          ],
        }
      )
    },
  },
]
</script>

<script lang="ts">
import { h } from 'vue'
export default { name: 'ModelCheckConfigView' }
</script>

<template>
  <div class="model-check-config-view">
    <NSpace vertical :size="16">
      <!-- 全局控制 -->
      <NCard :title="t('全局设置', 'Global Settings')">
        <NSpace :size="12">
          <NButton
            :type="daemonRunning ? 'error' : 'primary'"
            :loading="statusLoading"
            @click="daemonRunning ? handleStopDaemon() : handleStartDaemon()"
          >
            {{ daemonRunning ? t('停止 Daemon', 'Stop Daemon') : t('启动 Daemon', 'Start Daemon') }}
          </NButton>
          <NButton @click="handleRunOnce">
            {{ t('立即检查所有模型', 'Check All Models') }}
          </NButton>
          <NTag :type="daemonRunning ? 'success' : 'default'">
            {{ daemonRunning ? t('Daemon 运行中', 'Daemon Running') : t('Daemon 已停止', 'Daemon Stopped') }}
          </NTag>
        </NSpace>
      </NCard>

      <!-- 日志区域 -->
      <NCard :title="t('日志', 'Logs')">
        <template #header-extra>
          <NButton size="small" @click="handleClearLogs">
            {{ t('清除日志', 'Clear Logs') }}
          </NButton>
        </template>
        <div class="logs-container">
          <div v-if="logs.length === 0" class="logs-empty">
            {{ t('暂无日志', 'No logs') }}
          </div>
          <div v-else class="logs-content">
            <div v-for="(log, index) in logs" :key="index" class="log-line">
              {{ log }}
            </div>
          </div>
        </div>
      </NCard>

      <!-- 添加模型 -->
      <NCard :title="t('添加到监控', 'Add to Monitoring')">
        <NSpace :size="12">
          <select v-model="selectedModelId" class="model-select">
            <option value="">{{ t('选择模型', 'Select Model') }}</option>
            <option v-for="opt in availableModelOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
          <NButton type="primary" :disabled="!selectedModelId" @click="handleAddModel">
            {{ t('添加', 'Add') }}
          </NButton>
        </NSpace>
      </NCard>

      <!-- 已监控模型列表 -->
      <NCard :title="t('已监控模型', 'Monitored Models')">
        <NDataTable
          :columns="columns"
          :data="trackedModels"
          :loading="loading"
          :pagination="false"
          :row-key="(row: TrackedModel) => row.model_id"
        />
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

.logs-container {
  background-color: #1e1e1e;
  color: #d4d4d4;
  padding: 12px;
  border-radius: 4px;
  font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
  font-size: 13px;
  max-height: 400px;
  overflow-y: auto;
}

.logs-empty {
  text-align: center;
  color: #888;
  padding: 20px 0;
}

.logs-content {
  white-space: pre-wrap;
  word-break: break-word;
}

.log-line {
  padding: 2px 0;
  line-height: 1.5;
}

.model-select {
  min-width: 300px;
  padding: 6px 12px;
  border: 1px solid #d9d9d9;
  border-radius: 3px;
  font-size: 14px;
  background-color: white;
  cursor: pointer;
}

.model-select:focus {
  outline: none;
  border-color: #18a058;
}

:deep(.n-data-table) {
  font-size: 14px;
}
</style>
