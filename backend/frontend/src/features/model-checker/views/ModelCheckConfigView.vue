<template>
  <div class="model-checker-config-view">
    <NSpace vertical :size="16">
      <!-- 全局控制卡片 -->
      <NCard title="全局设置">
        <NSpace vertical :size="12">
          <NSpace align="center">
            <span>Daemon 状态：</span>
            <NTag :type="status.daemon_running ? 'success' : 'default'">
              {{ status.daemon_running ? '运行中' : '已停止' }}
            </NTag>
          </NSpace>

          <NSpace>
            <NButton
              type="primary"
              :loading="loading"
              @click="handleRunOnce"
            >
              立即检查所有模型
            </NButton>
            <NButton
              v-if="!status.daemon_running"
              type="success"
              :loading="loading"
              @click="handleStartDaemon"
            >
              启动 Daemon
            </NButton>
            <NButton
              v-else
              type="warning"
              :loading="loading"
              @click="handleStopDaemon"
            >
              停止 Daemon
            </NButton>
            <NButton
              secondary
              @click="handleClearLogs"
            >
              清除日志
            </NButton>
          </NSpace>

          <!-- 实时日志 -->
          <NCard title="实时日志" size="small">
            <div class="logs-container">
              <div v-for="(log, index) in status.logs" :key="index" class="log-line">
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
        />
      </NCard>
    </NSpace>

    <!-- 添加模型对话框 -->
    <NModal v-model:show="showAddModel" preset="dialog" title="添加模型到监控">
      <NForm ref="formRef" :model="formModel" label-placement="left" label-width="140">
        <NFormItem label="模型 ID" path="model_id" required>
          <NInput v-model:value="formModel.model_id" placeholder="例如: claude-3-5-sonnet-20241022" />
        </NFormItem>
        <NFormItem label="Provider" path="provider">
          <NInput v-model:value="formModel.provider" placeholder="例如: anthropic" />
        </NFormItem>
        <NFormItem label="巡检间隔(分钟)" path="check_interval_minutes">
          <NInputNumber v-model:value="formModel.check_interval_minutes" :min="1" placeholder="60" />
        </NFormItem>
        <NFormItem label="超时时间(秒)" path="timeout_seconds">
          <NInputNumber v-model:value="formModel.timeout_seconds" :min="1" placeholder="30" />
        </NFormItem>
        <NFormItem label="最大重试次数" path="max_retries">
          <NInputNumber v-model:value="formModel.max_retries" :min="0" :max="10" placeholder="2" />
        </NFormItem>
        <NFormItem label="告警开关" path="alert_on_unavailable">
          <NSwitch v-model:value="formModel.alert_on_unavailable" />
        </NFormItem>
      </NForm>

      <template #action>
        <NSpace>
          <NButton @click="showAddModel = false">取消</NButton>
          <NButton type="primary" @click="handleAddModel">添加</NButton>
        </NSpace>
      </template>
    </NModal>

    <!-- 编辑模型对话框 -->
    <NModal v-model:show="showEditModel" preset="dialog" title="编辑模型配置">
      <NForm ref="editFormRef" :model="editFormModel" label-placement="left" label-width="140">
        <NFormItem label="启用" path="enabled">
          <NSwitch v-model:value="editFormModel.enabled" />
        </NFormItem>
        <NFormItem label="巡检间隔(分钟)" path="check_interval_minutes">
          <NInputNumber v-model:value="editFormModel.check_interval_minutes" :min="1" />
        </NFormItem>
        <NFormItem label="超时时间(秒)" path="timeout_seconds">
          <NInputNumber v-model:value="editFormModel.timeout_seconds" :min="1" />
        </NFormItem>
        <NFormItem label="最大重试次数" path="max_retries">
          <NInputNumber v-model:value="editFormModel.max_retries" :min="0" :max="10" />
        </NFormItem>
        <NFormItem label="告警开关" path="alert_on_unavailable">
          <NSwitch v-model:value="editFormModel.alert_on_unavailable" />
        </NFormItem>
      </NForm>

      <template #action>
        <NSpace>
          <NButton @click="showEditModel = false">取消</NButton>
          <NButton type="primary" @click="handleUpdateModel">保存</NButton>
        </NSpace>
      </template>
    </NModal>
  </div>
</template>

<script setup lang="ts">
import { ref, h, onMounted, onUnmounted } from 'vue'
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
import type { TrackedModel, ModelCheckerStatus } from '@/shared/types/api'
import * as api from '../api/modelCheckerApi'

const message = useMessage()

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

const showAddModel = ref(false)
const formModel = ref({
  model_id: '',
  provider: '',
  check_interval_minutes: 60,
  timeout_seconds: 30,
  max_retries: 2,
  alert_on_unavailable: true,
})

const showEditModel = ref(false)
const editFormModel = ref<{
  model_id: string
  enabled: boolean
  check_interval_minutes: number
  timeout_seconds: number
  max_retries: number
  alert_on_unavailable: boolean
}>({
  model_id: '',
  enabled: true,
  check_interval_minutes: 60,
  timeout_seconds: 30,
  max_retries: 2,
  alert_on_unavailable: true,
})

const columns: DataTableColumns<TrackedModel> = [
  {
    title: '模型 ID',
    key: 'model_id',
  },
  {
    title: 'Provider',
    key: 'provider',
  },
  {
    title: '状态',
    key: 'enabled',
    render: (row) => h(NSwitch, {
      value: row.enabled,
      onUpdateValue: (val) => handleToggleEnabled(row.model_id, val),
    }),
  },
  {
    title: '巡检间隔',
    key: 'check_interval_minutes',
    render: (row) => `${row.check_interval_minutes} 分钟`,
  },
  {
    title: '超时',
    key: 'timeout_seconds',
    render: (row) => `${row.timeout_seconds} 秒`,
  },
  {
    title: '重试',
    key: 'max_retries',
  },
  {
    title: '告警',
    key: 'alert_on_unavailable',
    render: (row) => h(NTag, {
      type: row.alert_on_unavailable ? 'success' : 'default',
      size: 'small',
    }, { default: () => row.alert_on_unavailable ? '开启' : '关闭' }),
  },
  {
    title: '操作',
    key: 'actions',
    render: (row) => h(NSpace, {}, {
      default: () => [
        h(NButton, {
          size: 'small',
          onClick: () => handleEdit(row),
        }, { default: () => '编辑' }),
        h(NButton, {
          size: 'small',
          onClick: () => handleCheck(row.model_id),
        }, { default: () => '立即检查' }),
        h(NButton, {
          size: 'small',
          type: 'error',
          onClick: () => handleDelete(row.model_id),
        }, { default: () => '移除' }),
      ],
    }),
  },
]

let pollTimer: number | null = null

async function loadData() {
  try {
    const [statusRes, modelsRes] = await Promise.all([
      api.getModelCheckerStatus(),
      api.getTrackedModels(),
    ])
    status.value = statusRes
    trackedModels.value = modelsRes
  } catch (err: any) {
    message.error(err.message || '加载数据失败')
  }
}

async function handleRunOnce() {
  loading.value = true
  try {
    await api.runModelCheckerOnce()
    message.success('已启动检查')
    loadData()
  } catch (err: any) {
    message.error(err.message || '启动失败')
  } finally {
    loading.value = false
  }
}

async function handleStartDaemon() {
  loading.value = true
  try {
    await api.startModelChecker()
    message.success('Daemon 已启动')
    loadData()
  } catch (err: any) {
    message.error(err.message || '启动失败')
  } finally {
    loading.value = false
  }
}

async function handleStopDaemon() {
  loading.value = true
  try {
    await api.stopModelChecker()
    message.success('Daemon 已停止')
    loadData()
  } catch (err: any) {
    message.error(err.message || '停止失败')
  } finally {
    loading.value = false
  }
}

async function handleClearLogs() {
  try {
    await api.clearModelCheckerLogs()
    message.success('日志已清除')
    loadData()
  } catch (err: any) {
    message.error(err.message || '清除失败')
  }
}

async function handleAddModel() {
  if (!formModel.value.model_id) {
    message.error('请填写模型 ID')
    return
  }
  try {
    await api.addTrackedModel(formModel.value)
    message.success('模型已添加到监控')
    showAddModel.value = false
    formModel.value = {
      model_id: '',
      provider: '',
      check_interval_minutes: 60,
      timeout_seconds: 30,
      max_retries: 2,
      alert_on_unavailable: true,
    }
    loadData()
  } catch (err: any) {
    message.error(err.message || '添加失败')
  }
}

function handleEdit(row: TrackedModel) {
  editFormModel.value = {
    model_id: row.model_id,
    enabled: row.enabled,
    check_interval_minutes: row.check_interval_minutes,
    timeout_seconds: row.timeout_seconds,
    max_retries: row.max_retries,
    alert_on_unavailable: row.alert_on_unavailable,
  }
  showEditModel.value = true
}

async function handleUpdateModel() {
  try {
    await api.updateTrackedModel(editFormModel.value.model_id, {
      enabled: editFormModel.value.enabled,
      check_interval_minutes: editFormModel.value.check_interval_minutes,
      timeout_seconds: editFormModel.value.timeout_seconds,
      max_retries: editFormModel.value.max_retries,
      alert_on_unavailable: editFormModel.value.alert_on_unavailable,
    })
    message.success('配置已更新')
    showEditModel.value = false
    loadData()
  } catch (err: any) {
    message.error(err.message || '更新失败')
  }
}

async function handleToggleEnabled(modelId: string, enabled: boolean) {
  try {
    await api.updateTrackedModel(modelId, { enabled })
    message.success(enabled ? '已启用' : '已停用')
    loadData()
  } catch (err: any) {
    message.error(err.message || '操作失败')
  }
}

async function handleCheck(modelId: string) {
  try {
    await api.checkTrackedModel(modelId)
    message.success('已启动检查')
    loadData()
  } catch (err: any) {
    message.error(err.message || '启动失败')
  }
}

async function handleDelete(modelId: string) {
  try {
    await api.deleteTrackedModel(modelId)
    message.success('模型已从监控移除')
    loadData()
  } catch (err: any) {
    message.error(err.message || '移除失败')
  }
}

onMounted(() => {
  loadData()
  pollTimer = window.setInterval(() => {
    loadData()
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

.logs-container {
  max-height: 300px;
  overflow-y: auto;
  font-family: monospace;
  font-size: 12px;
  background-color: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
}

.log-line {
  margin-bottom: 4px;
  white-space: nowrap;
}

.empty-logs {
  color: #999;
  text-align: center;
  padding: 20px;
}
</style>
