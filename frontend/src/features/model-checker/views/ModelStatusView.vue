<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { NCard, NSpace, NDataTable, NTag, NButton, NStatistic, useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { TrackedModel } from '@/shared/types/api'
import { getTrackedModels, getModelCheckerStatus } from '../api/modelCheckerApi'

const { t } = useI18n()
const message = useMessage()

const loading = ref(false)
const trackedModels = ref<TrackedModel[]>([])
const stats = ref({
  total: 0,
  available: 0,
  unavailable: 0,
  error: 0,
})

let statusInterval: number | null = null

onMounted(async () => {
  await loadData()
  statusInterval = window.setInterval(loadData, 5000)
})

const loadData = async () => {
  loading.value = true
  try {
    const models = await getTrackedModels()

    trackedModels.value = models

    // Calculate stats
    stats.value = {
      total: models.length,
      available: models.filter(m => m.last_status === 'available').length,
      unavailable: models.filter(m => m.last_status === 'unavailable').length,
      error: models.filter(m => m.last_status === 'error').length,
    }
  } catch (error: any) {
    message.error(error.message || t('加载失败', 'Failed to load'))
  } finally {
    loading.value = false
  }
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
    return time
  }
}

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
    title: () => t('调度表达式', 'Schedule Cron'),
    key: 'schedule_cron',
    width: 140,
    render: (row) => row.schedule_cron || '-',
  },
  {
    title: () => t('最后检查时间', 'Last Checked'),
    key: 'last_checked_at',
    width: 180,
    render: (row) => formatTime(row.last_checked_at),
  },
  {
    title: () => t('最后可用时间', 'Last Available'),
    key: 'last_available_at',
    width: 180,
    render: (row) => formatTime(row.last_available_at),
  },
  {
    title: () => t('下次运行时间', 'Next Run'),
    key: 'next_run_at',
    width: 180,
    render: (row) => formatTime(row.next_run_at),
  },
]

const getRowProps = (row: TrackedModel) => {
  let backgroundColor = 'transparent'
  if (row.last_status === 'available') {
    backgroundColor = 'rgba(34, 197, 94, 0.08)'
  } else if (row.last_status === 'unavailable') {
    backgroundColor = 'rgba(239, 68, 68, 0.08)'
  } else if (row.last_status === 'error') {
    backgroundColor = 'rgba(245, 158, 11, 0.08)'
  }

  return {
    style: {
      backgroundColor,
    },
  }
}
</script>

<script lang="ts">
import { h } from 'vue'
export default { name: 'ModelStatusView' }
</script>

<template>
  <div class="model-status-view">
    <NSpace vertical :size="16">
      <!-- 统计卡片 -->
      <NCard :title="t('概览', 'Overview')">
        <div class="stats-grid">
          <NCard embedded>
            <NStatistic :label="t('总监控模型', 'Total Monitored')" :value="stats.total" />
          </NCard>
          <NCard embedded>
            <NStatistic
              :label="t('正常模型', 'Available Models')"
              :value="stats.available"
              :value-style="{ color: '#18a058' }"
            />
          </NCard>
          <NCard embedded>
            <NStatistic
              :label="t('异常模型', 'Unavailable Models')"
              :value="stats.unavailable"
              :value-style="{ color: '#d03050' }"
            />
          </NCard>
          <NCard embedded>
            <NStatistic
              :label="t('错误模型', 'Error Models')"
              :value="stats.error"
              :value-style="{ color: '#f0a020' }"
            />
          </NCard>
        </div>
      </NCard>

      <!-- 模型状态表格 -->
      <NCard :title="t('模型状态', 'Model Status')">
        <NDataTable
          :columns="columns"
          :data="trackedModels"
          :loading="loading"
          :pagination="false"
          :row-key="(row: TrackedModel) => row.model_id"
          :row-props="getRowProps"
        />
      </NCard>
    </NSpace>
  </div>
</template>

<style scoped>
.model-status-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
}

:deep(.n-data-table) {
  font-size: 14px;
}

:deep(.n-statistic .n-statistic-value) {
  font-size: 28px;
  font-weight: 600;
}
</style>
