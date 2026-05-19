<script setup lang="ts">
import type { Component, CSSProperties } from 'vue'
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  useDialog,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { Database, Layers3, RefreshCw, Search, Server, Settings2 } from 'lucide-vue-next'

import {
  createModelPrice,
  deleteModelPrice,
  getLiteLLMProxySettings,
  listModelPrices,
  syncLitellmModelPrices,
  updateLiteLLMProxySettings,
  updateModelPrice,
} from '@/features/pricing/api/pricingApi'
import type { LiteLLMProxySettingsPayload, ModelPrice, ModelPricePayload } from '@/shared/types/api'
import { formatDateTime, formatInteger } from '@/shared/utils/format'

type PriceTableLayoutProps =
  | { flexHeight: true }
  | { flexHeight: false; maxHeight: string }

const PRICE_TABLE_FALLBACK_MAX_HEIGHT = 'max(240px, calc(100dvh - 360px))'
const priceModalStyle: CSSProperties = { width: 'min(640px, calc(100vw - 32px))' }
const proxyModalStyle: CSSProperties = { width: 'min(460px, calc(100vw - 32px))' }
const proxyModalContentStyle: CSSProperties = { padding: '16px 22px 4px' }
const proxyModalFooterStyle: CSSProperties = { padding: '12px 22px 18px' }
const liteLLMProxyHint =
  'LiteLLM 价格数据从 GitHub 下载；如果当前网络无法访问 GitHub，可以启用代理后再同步。'
const desktopPriceLayoutQuery = window.matchMedia('(min-width: 861px)')
const message = useMessage()
const dialog = useDialog()
const isLoading = ref(false)
const isSyncing = ref(false)
const modalOpen = ref(false)
const proxyModalOpen = ref(false)
const isProxyLoading = ref(false)
const isProxySaving = ref(false)
const editingId = ref<number | null>(null)
const prices = ref<ModelPrice[]>([])
const selectedProvider = ref<string | null>(null)
const searchQuery = ref('')
const isDesktopPriceLayout = ref(desktopPriceLayoutQuery.matches)
const pagination = reactive({
  page: 1,
  pageSize: 20,
  onUpdatePage: updatePricePage,
})
const form = reactive<ModelPricePayload>({
  provider: '',
  model: '',
  input_usd_per_million: 0,
  output_usd_per_million: 0,
  cache_read_usd_per_million: 0,
  cache_creation_usd_per_million: 0,
})
const proxyForm = reactive<LiteLLMProxySettingsPayload>({
  enabled: false,
  proxy_url: '',
})

const providerOptions = computed(() =>
  [...new Set(prices.value.map((price) => price.provider))]
    .sort((a, b) => a.localeCompare(b))
    .map((provider) => ({ label: provider, value: provider })),
)

const filteredPrices = computed(() => {
  return prices.value.filter((price) => {
    if (selectedProvider.value && price.provider !== selectedProvider.value) {
      return false
    }
    return priceMatchesSearch(price)
  })
})

watch([selectedProvider, searchQuery], () => {
  pagination.page = 1
})

function renderSearchIcon() {
  return h(NIcon, { component: Search })
}

function updatePricePage(page: number) {
  pagination.page = page
}

function normalizePriceSearch(value: string) {
  return value.trim().toLowerCase()
}

const normalizedSearchQuery = computed(() => normalizePriceSearch(searchQuery.value))

const filteredPriceCount = computed(() => filteredPrices.value.length)

const totalPriceCount = computed(() => prices.value.length)
const syncedPriceCount = computed(() => prices.value.filter((price) => price.auto_synced).length)
const manualPriceCount = computed(() => prices.value.filter((price) => !price.auto_synced).length)
const priceTableLayoutProps = computed<PriceTableLayoutProps>(() =>
  isDesktopPriceLayout.value
    ? { flexHeight: true }
    : { flexHeight: false, maxHeight: PRICE_TABLE_FALLBACK_MAX_HEIGHT },
)

interface PriceMetricCard {
  key: string
  label: string
  value: string
  footnote: string
  tone: 'teal' | 'blue' | 'purple' | 'orange'
  icon: Component
}

const priceMetrics = computed<PriceMetricCard[]>(() => [
  {
    key: 'models',
    label: '价格条目',
    value: formatInteger(totalPriceCount.value),
    footnote: `筛选后 ${formatInteger(filteredPriceCount.value)}`,
    tone: 'teal',
    icon: Layers3,
  },
  {
    key: 'providers',
    label: '服务商',
    value: formatInteger(providerOptions.value.length),
    footnote: '当前价格库',
    tone: 'blue',
    icon: Server,
  },
  {
    key: 'synced',
    label: 'LiteLLM 同步',
    value: formatInteger(syncedPriceCount.value),
    footnote: '自动维护',
    tone: 'purple',
    icon: RefreshCw,
  },
  {
    key: 'manual',
    label: '手动价格',
    value: formatInteger(manualPriceCount.value),
    footnote: '优先保留',
    tone: 'orange',
    icon: Database,
  },
])

function priceMatchesSearch(price: ModelPrice) {
  if (!normalizedSearchQuery.value) {
    return true
  }
  return (
    price.provider.toLowerCase().includes(normalizedSearchQuery.value) ||
    price.model.toLowerCase().includes(normalizedSearchQuery.value)
  )
}

function resetForm() {
  editingId.value = null
  form.provider = ''
  form.model = ''
  form.input_usd_per_million = 0
  form.output_usd_per_million = 0
  form.cache_read_usd_per_million = 0
  form.cache_creation_usd_per_million = 0
}

async function refresh() {
  isLoading.value = true
  try {
    prices.value = await listModelPrices()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载模型价格失败')
  } finally {
    isLoading.value = false
  }
}

function openCreate() {
  resetForm()
  modalOpen.value = true
}

function openEdit(row: ModelPrice) {
  editingId.value = row.id
  form.provider = row.provider
  form.model = row.model
  form.input_usd_per_million = row.input_usd_per_million
  form.output_usd_per_million = row.output_usd_per_million
  form.cache_read_usd_per_million = row.cache_read_usd_per_million
  form.cache_creation_usd_per_million = row.cache_creation_usd_per_million
  modalOpen.value = true
}

async function savePrice() {
  const payload: ModelPricePayload = {
    provider: form.provider.trim(),
    model: form.model.trim(),
    input_usd_per_million: form.input_usd_per_million,
    output_usd_per_million: form.output_usd_per_million,
    cache_read_usd_per_million: form.cache_read_usd_per_million,
    cache_creation_usd_per_million: form.cache_creation_usd_per_million,
  }
  if (!payload.provider || !payload.model) {
    message.error('服务商和模型不能为空')
    return
  }
  try {
    if (editingId.value === null) {
      await createModelPrice(payload)
      message.success('模型价格已创建')
    } else {
      await updateModelPrice(editingId.value, payload)
      message.success('模型价格已更新')
    }
    modalOpen.value = false
    await refresh()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '保存模型价格失败')
  }
}

async function syncPrices() {
  isSyncing.value = true
  try {
    const result = await syncLitellmModelPrices()
    message.success(
      `同步完成：LiteLLM 价格 ${result.imported} 条，手动价格保留 ${result.skipped_manual} 条`,
    )
    await refresh()
  } catch (error) {
    const detail = error instanceof Error ? error.message : '同步模型价格失败'
    message.error(`${detail}。${liteLLMProxyHint}`)
  } finally {
    isSyncing.value = false
  }
}

async function openProxySettings() {
  proxyModalOpen.value = true
  isProxyLoading.value = true
  try {
    const settings = await getLiteLLMProxySettings()
    proxyForm.enabled = settings.enabled
    proxyForm.proxy_url = settings.proxy_url
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载代理配置失败')
  } finally {
    isProxyLoading.value = false
  }
}

async function saveProxySettings() {
  const payload: LiteLLMProxySettingsPayload = {
    enabled: proxyForm.enabled,
    proxy_url: proxyForm.proxy_url.trim(),
  }
  if (payload.enabled && !payload.proxy_url) {
    message.error('启用代理时必须填写代理地址')
    return
  }
  isProxySaving.value = true
  try {
    const saved = await updateLiteLLMProxySettings(payload)
    proxyForm.enabled = saved.enabled
    proxyForm.proxy_url = saved.proxy_url
    proxyModalOpen.value = false
    message.success('代理配置已保存')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '保存代理配置失败')
  } finally {
    isProxySaving.value = false
  }
}

function confirmDelete(row: ModelPrice) {
  dialog.warning({
    title: '删除价格',
    content: `${row.provider} / ${row.model}`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      await deleteModelPrice(row.id)
      message.success('模型价格已删除')
      await refresh()
    },
  })
}

function handleDesktopPriceLayoutChange(event: MediaQueryListEvent) {
  isDesktopPriceLayout.value = event.matches
}

const columns: DataTableColumns<ModelPrice> = [
  { title: '服务商', key: 'provider', width: 150, ellipsis: { tooltip: true } },
  { title: '模型', key: 'model', width: 360, ellipsis: { tooltip: true } },
  { title: '输入 ($/MTok)', key: 'input_usd_per_million', width: 125 },
  { title: '输出 ($/MTok)', key: 'output_usd_per_million', width: 125 },
  { title: '缓存读 ($/MTok)', key: 'cache_read_usd_per_million', width: 125 },
  { title: '缓存写 ($/MTok)', key: 'cache_creation_usd_per_million', width: 125 },
  {
    title: '来源',
    key: 'source',
    width: 96,
    render: (row) =>
      h(
        NTag,
        { size: 'small', type: row.auto_synced ? 'info' : 'default', bordered: false },
        { default: () => (row.auto_synced ? 'LiteLLM' : '手动') },
      ),
  },
  {
    title: '更新',
    key: 'updated_at',
    width: 140,
    render: (row) => formatDateTime(row.updated_at),
  },
  {
    title: '',
    key: 'actions',
    width: 124,
    fixed: 'right',
    render: (row) =>
      h(
        NSpace,
        { size: 4 },
        {
          default: () => [
            h(
              NButton,
              { size: 'small', quaternary: true, onClick: () => openEdit(row) },
              { default: () => '编辑' },
            ),
            h(
              NButton,
              { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDelete(row) },
              { default: () => '删除' },
            ),
          ],
        },
      ),
  },
]

onMounted(() => {
  desktopPriceLayoutQuery.addEventListener('change', handleDesktopPriceLayoutChange)
  void refresh()
})

onBeforeUnmount(() => {
  desktopPriceLayoutQuery.removeEventListener('change', handleDesktopPriceLayoutChange)
})
</script>

<template>
  <section class="page price-page">
    <div class="page-header">
      <div>
        <h1 class="page-title">模型价格</h1>
        <p class="page-subtitle">单位为 USD / 百万 Token，历史费用按当前价格实时重算</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isSyncing" @click="syncPrices">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          同步 LiteLLM
        </NButton>
        <NButton secondary :disabled="isSyncing" @click="openProxySettings">
          <template #icon>
            <NIcon :component="Settings2" />
          </template>
          代理配置
        </NButton>
        <NButton type="primary" @click="openCreate">新增价格</NButton>
      </NSpace>
    </div>

    <div class="metric-grid price-metrics">
      <div v-for="metric in priceMetrics" :key="metric.key" class="metric-card" :class="`is-${metric.tone}`">
        <div class="metric-icon" aria-hidden="true">
          <component :is="metric.icon" :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ metric.label }}</div>
        <div class="metric-value">{{ metric.value }}</div>
        <div class="metric-footnote">{{ metric.footnote }}</div>
      </div>
    </div>

    <section class="panel table-panel price-table-panel">
      <div class="table-toolbar">
        <NSpace class="price-toolbar-layout" justify="space-between" align="center">
          <NSpace class="price-filters" align="center" :size="8">
            <span class="filter-label">服务商</span>
            <NSelect
              v-model:value="selectedProvider"
              class="provider-filter"
              :options="providerOptions"
              clearable
              filterable
              placeholder="全部服务商"
            />
            <NInput
              v-model:value="searchQuery"
              class="price-search"
              clearable
              placeholder="搜索服务商或模型"
              :render-prefix="renderSearchIcon"
            />
          </NSpace>
          <span class="result-count">共 {{ filteredPriceCount }} / {{ totalPriceCount }} 条</span>
        </NSpace>
      </div>
      <NDataTable
        class="price-table"
        v-bind="priceTableLayoutProps"
        size="small"
        :loading="isLoading"
        :columns="columns"
        :data="filteredPrices"
        :pagination="pagination"
        :scroll-x="1370"
      />
    </section>

    <NModal
      v-model:show="modalOpen"
      preset="card"
      :title="editingId === null ? '新增价格' : '编辑价格'"
      :style="priceModalStyle"
      class="price-modal"
    >
      <NForm :model="form" label-placement="top">
        <div class="form-grid">
          <NFormItem label="服务商">
            <NInput v-model:value="form.provider" />
          </NFormItem>
          <NFormItem label="模型">
            <NInput v-model:value="form.model" />
          </NFormItem>
          <NFormItem label="输入价格">
            <NInputNumber v-model:value="form.input_usd_per_million" :min="0" />
          </NFormItem>
          <NFormItem label="输出价格">
            <NInputNumber v-model:value="form.output_usd_per_million" :min="0" />
          </NFormItem>
          <NFormItem label="缓存读价格">
            <NInputNumber v-model:value="form.cache_read_usd_per_million" :min="0" />
          </NFormItem>
          <NFormItem label="缓存写价格">
            <NInputNumber v-model:value="form.cache_creation_usd_per_million" :min="0" />
          </NFormItem>
        </div>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton @click="modalOpen = false">取消</NButton>
          <NButton type="primary" @click="savePrice">保存</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="proxyModalOpen"
      preset="card"
      title="LiteLLM 代理配置"
      :style="proxyModalStyle"
      :content-style="proxyModalContentStyle"
      :footer-style="proxyModalFooterStyle"
      class="proxy-modal"
    >
      <NForm :model="proxyForm" label-placement="top">
        <div class="proxy-form">
          <p class="proxy-hint">{{ liteLLMProxyHint }}</p>
          <div class="proxy-switch-row">
            <span class="proxy-switch-label">使用代理</span>
            <NSwitch
              v-model:value="proxyForm.enabled"
              :disabled="isProxyLoading || isProxySaving"
              aria-label="使用代理"
            />
          </div>
          <NFormItem label="代理地址">
            <NInput
              v-model:value="proxyForm.proxy_url"
              :disabled="!proxyForm.enabled || isProxyLoading || isProxySaving"
              placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:1080"
            />
          </NFormItem>
        </div>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isProxySaving" @click="proxyModalOpen = false">取消</NButton>
          <NButton type="primary" :loading="isProxySaving" @click="saveProxySettings">保存</NButton>
        </NSpace>
      </template>
    </NModal>
  </section>
</template>

<style scoped>
.price-modal {
  width: min(640px, calc(100vw - 24px));
}

.proxy-modal {
  width: min(520px, calc(100vw - 24px));
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.proxy-form {
  display: grid;
  gap: 14px;
}

.proxy-hint {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid rgba(8, 145, 178, 0.22);
  border-radius: var(--cpa-radius);
  background: rgba(8, 145, 178, 0.08);
  color: var(--cpa-text-muted);
  font-size: 13px;
  line-height: 1.55;
}

.proxy-switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 34px;
  padding: 8px 10px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface-raised);
}

.proxy-switch-label {
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 600;
}

.proxy-form :deep(.n-form-item) {
  margin-bottom: 0;
}

.price-metrics {
  grid-template-columns: repeat(4, minmax(150px, 1fr));
}

.price-table-panel,
.price-table {
  min-width: 0;
  min-height: 0;
}

.price-table-panel {
  overflow: hidden;
}

.table-toolbar {
  padding: 14px 16px;
  border: 1px solid var(--cpa-border);
  border-bottom: 0;
  border-radius: var(--cpa-radius) var(--cpa-radius) 0 0;
  background: var(--cpa-surface-raised);
  box-shadow: var(--cpa-shadow-hairline);
}

.price-toolbar-layout {
  width: 100%;
  min-width: 0;
}

.price-table :deep(.n-data-table-wrapper) {
  border-radius: 0 0 var(--cpa-radius) var(--cpa-radius);
}

.filter-label,
.result-count {
  color: var(--cpa-text-muted);
  font-size: 13px;
  white-space: nowrap;
}

.provider-filter {
  width: 220px;
}

.price-filters {
  min-width: 0;
  max-width: 100%;
}

.price-search {
  width: 280px;
}

@media (min-width: 861px) {
  .price-page {
    grid-template-rows: auto auto minmax(0, 1fr);
    height: calc(100dvh - 60px);
    min-height: 0;
    overflow: hidden;
  }

  .price-table-panel {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    min-height: 0;
  }

  .price-table {
    height: 100%;
    min-height: 0;
  }

  .price-table :deep(.n-data-table-wrapper),
  .price-table :deep(.n-data-table-base-table),
  .price-table :deep(.n-data-table-base-table-body) {
    min-height: 0;
  }
}

@media (max-width: 980px) {
  .table-toolbar {
    padding: 12px;
  }

  .provider-filter {
    width: min(200px, calc(100vw - 32px));
  }

  .price-search {
    width: min(240px, calc(100vw - 32px));
  }
}

@media (max-width: 620px) {
  .price-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .form-grid {
    grid-template-columns: 1fr;
  }

  .price-toolbar-layout {
    display: grid !important;
    gap: 8px !important;
  }

  .price-filters {
    display: grid !important;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px !important;
    width: 100%;
  }

  .filter-label {
    display: none;
  }

  .provider-filter,
  .price-search {
    width: 100%;
  }

  .result-count {
    justify-self: start;
  }
}
</style>
