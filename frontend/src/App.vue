<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { logger } from './services/logger'

type PageKey = 'dashboard' | 'indexer' | 'tasks' | 'logs' | 'settings'

interface SchedulerStatus {
  running: boolean
  active_workers: number
  completed: number
  failed: number
  indexing: boolean
  indexed: number
  queued: number
  limits: {
    workers: number
    sync_concurrency: number
  }
}

type ProxyMode = 'direct' | 'system' | 'manual'
type LogStream = 'indexer' | 'crawler'
type FetchMode = 'incremental' | 'validate' | 'reload'

interface LogTailResponse {
  stream: LogStream
  lines: string[]
}

type TopicStatus = 'waiting' | 'done' | 'failed'

interface Topic {
  id: number
  external_id: string
  title: string
  author_id: string
  url: string
  status: TopicStatus
  last_error?: string
  updated_at: string
}

interface TopicCounts {
  waiting: number
  done: number
  failed: number
}

interface TopicPageResponse {
  items: Topic[]
  counts: TopicCounts
  page: number
  page_size: number
  total: number
  total_pages: number
}

interface PageContent {
  uid: string
  page_no: number
  floor: number
  text: string
}

interface ProxyStatus {
  config: {
    mode: ProxyMode
    url?: string
  }
  effective_proxy?: string
  system_available: boolean
  system_error?: string
}

const navItems: Array<{ key: PageKey; label: string; icon: string }> = [
  { key: 'dashboard', label: '概览', icon: '◫' },
  { key: 'indexer', label: '索引构建', icon: '⌘' },
  { key: 'tasks', label: '抓取任务', icon: '▤' },
  { key: 'logs', label: '运行日志', icon: '≡' },
  { key: 'settings', label: '系统设置', icon: '⚙' },
]

const activePage = ref<PageKey>('dashboard')
const loading = ref(false)
const connectionError = ref('')
const actionMessage = ref('')
const proxyLoading = ref(false)
const proxySaving = ref(false)
const proxyStatus = ref<ProxyStatus>({
  config: { mode: 'direct', url: '' },
  system_available: false,
})
const proxyForm = ref<{ mode: ProxyMode; url: string }>({ mode: 'direct', url: '' })
const limitsForm = ref({ workers: 2, sync_concurrency: 4 })
const limitsSaving = ref(false)
const limitsDirty = ref(false)
const selectedLog = ref<LogStream>('indexer')
const logEntries = ref<string[]>([])
const logLoading = ref(false)
const logError = ref('')
const logRefreshedAt = ref('')
const topicItems = ref<Topic[]>([])
const topicCounts = ref<TopicCounts>({ waiting: 0, done: 0, failed: 0 })
const topicTotal = ref(0)
const topicTotalPages = ref(1)
const topicsLoading = ref(false)
const activeTopicStatus = ref<TopicStatus>('waiting')
const topicPageSize = ref(10)
const topicPages = ref<Record<TopicStatus, number>>({ waiting: 1, done: 1, failed: 1 })
const selectedTopic = ref<Topic | null>(null)
const selectedTopicContents = ref<PageContent[]>([])
const selectedTopicContentTotal = ref(0)
const topicDetailsLoading = ref(false)
const topicDetailsError = ref('')
const fullTopicText = ref('')
const fullTopicLoading = ref(false)
const fullTopicError = ref('')
const readingFullTopic = ref(false)
const fetchMode = ref<FetchMode>('incremental')
let logRefreshTimer: number | undefined
let statusRefreshTimer: number | undefined
const scheduler = ref<SchedulerStatus>({
  running: false,
  active_workers: 0,
  completed: 0,
  failed: 0,
  indexing: false,
  indexed: 0,
  queued: 0,
  limits: { workers: 2, sync_concurrency: 4 },
})

const activeLabel = computed(() => navItems.find((item) => item.key === activePage.value)?.label ?? '')
const completionRate = computed(() => {
  const total = scheduler.value.completed + scheduler.value.failed
  return total === 0 ? 0 : Math.round((scheduler.value.completed / total) * 100)
})
const schedulerState = computed(() => (scheduler.value.running ? '运行中' : '已停止'))
const topicSections = computed(() => [
  { key: 'waiting' as const, label: '等待抓取', description: '已完成索引，等待抓取指令', count: topicCounts.value.waiting },
  { key: 'done' as const, label: '已完成', description: '正文已完整持久化', count: topicCounts.value.done },
  { key: 'failed' as const, label: '抓取失败', description: '可在抓取任务页重试', count: topicCounts.value.failed },
])
const activeTopicSection = computed(() => topicSections.value.find((section) => section.key === activeTopicStatus.value) ?? topicSections.value[0])
const activeTopicPageCount = computed(() => topicTotalPages.value)
const activeTopicPage = computed(() => topicPages.value[activeTopicStatus.value])
const activeTopicRange = computed(() => {
  if (topicTotal.value === 0) return '0–0'
  const start = (activeTopicPage.value - 1) * topicPageSize.value + 1
  const end = Math.min(start + topicItems.value.length - 1, topicTotal.value)
  return `${start}–${end}`
})

function selectTopicStatus(status: TopicStatus) {
  activeTopicStatus.value = status
  void loadTopics()
}

function changeTopicPage(page: number) {
  topicPages.value[activeTopicStatus.value] = Math.min(Math.max(1, page), activeTopicPageCount.value)
  void loadTopics()
}

function changeTopicPageSize() {
  topicPages.value = { waiting: 1, done: 1, failed: 1 }
  void loadTopics()
}

async function viewTopic(topic: Topic) {
  selectedTopic.value = topic
  selectedTopicContents.value = []
  selectedTopicContentTotal.value = 0
  topicDetailsError.value = ''
  readingFullTopic.value = false
  fullTopicText.value = ''
  fullTopicError.value = ''
  topicDetailsLoading.value = true
  try {
    const result = await request<{ topic_id: number; contents: PageContent[]; total: number }>(`/api/topics/${topic.id}/contents`)
    selectedTopicContents.value = result.contents
    selectedTopicContentTotal.value = result.total
  } catch (reason) {
    topicDetailsError.value = reason instanceof Error ? reason.message : '读取主题正文失败'
    logger.error('读取主题正文失败', { topicId: topic.id, reason })
  } finally {
    topicDetailsLoading.value = false
  }
}

async function readFullTopic() {
  if (!selectedTopic.value) return
  readingFullTopic.value = true
  fullTopicText.value = ''
  fullTopicError.value = ''
  fullTopicLoading.value = true
  try {
    const result = await request<{ topic_id: number; content_count: number; text: string }>(`/api/topics/${selectedTopic.value.id}/contents/full`)
    fullTopicText.value = result.text
  } catch (reason) {
    fullTopicError.value = reason instanceof Error ? reason.message : '读取完整内容失败'
    logger.error('读取完整主题正文失败', { topicId: selectedTopic.value.id, reason })
  } finally {
    fullTopicLoading.value = false
  }
}

function showTopicPreview() {
  readingFullTopic.value = false
}

function closeTopicDetails() {
  selectedTopic.value = null
  selectedTopicContents.value = []
  selectedTopicContentTotal.value = 0
  topicDetailsError.value = ''
  readingFullTopic.value = false
  fullTopicText.value = ''
  fullTopicError.value = ''
}

async function request<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(url, options)
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { error?: string } | null
    throw new Error(payload?.error ?? `HTTP ${response.status}`)
  }
  return response.status === 204 ? (undefined as T) : response.json() as Promise<T>
}

async function refreshStatus(source: 'initial' | 'manual' | 'poll' = 'manual') {
  loading.value = true
  connectionError.value = ''
  try {
    scheduler.value = await request<SchedulerStatus>('/api/scheduler')
    if (!limitsDirty.value) limitsForm.value = { ...scheduler.value.limits }
    if (source !== 'poll') logger.info('调度器状态已刷新', { source, running: scheduler.value.running })
  } catch (reason) {
    connectionError.value = reason instanceof Error ? reason.message : '未知错误'
    logger.error('获取调度器状态失败', { source, reason })
  } finally {
    loading.value = false
  }
}

async function saveSchedulerLimits() {
  if (!Number.isInteger(limitsForm.value.workers) || limitsForm.value.workers < 0 || !Number.isInteger(limitsForm.value.sync_concurrency) || limitsForm.value.sync_concurrency < 1) {
    actionMessage.value = 'Worker 上限需为不小于 0 的整数，单主题分页并发需为不小于 1 的整数'
    return
  }
  limitsSaving.value = true
  actionMessage.value = ''
  try {
    const limits = await request<SchedulerStatus['limits']>('/api/scheduler/limits', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(limitsForm.value),
    })
    scheduler.value.limits = limits
    limitsForm.value = { ...limits }
    limitsDirty.value = false
    actionMessage.value = '抓取并发配置已应用；正在处理的主题将在下一次领取任务时使用新分页并发上限'
    logger.info('抓取并发配置已更新', limits)
  } catch (reason) {
    actionMessage.value = reason instanceof Error ? reason.message : '保存抓取并发配置失败'
    logger.error('保存抓取并发配置失败', { reason })
  } finally {
    limitsSaving.value = false
  }
}

async function controlIndex(action: 'start' | 'cancel') {
  actionMessage.value = ''
  try {
    const path = action === 'start' ? '/api/scheduler/index' : '/api/scheduler/index/cancel'
    const result = await request<SchedulerStatus | { status: string }>(path, { method: 'POST' })
    actionMessage.value = action === 'start' ? '索引任务已启动；新主题将保持“等待抓取”状态' : '已请求中断当前索引任务，已发现主题会保留'
    logger.info('索引控制操作完成', { action, result })
    await refreshStatus('manual')
    await loadTopics()
  } catch (reason) {
    actionMessage.value = reason instanceof Error ? reason.message : '操作失败'
    logger.error('索引控制操作失败', { action, reason })
  }
}

async function queueTopics(mode: 'waiting' | 'failed') {
  actionMessage.value = ''
  try {
    const path = mode === 'waiting' ? 'queue/waiting' : 'retry/failed'
    const result = await request<{ queued: number; status: SchedulerStatus }>(`/api/scheduler/${path}?mode=${fetchMode.value}`, { method: 'POST' })
    scheduler.value = result.status
    actionMessage.value = mode === 'waiting'
      ? `已将 ${result.queued} 个等待主题加入抓取队列`
      : `已将 ${result.queued} 个失败主题恢复并加入抓取队列`
    logger.info('主题抓取指令已提交', { source: mode, fetchMode: fetchMode.value, queued: result.queued })
  } catch (reason) {
    actionMessage.value = reason instanceof Error ? reason.message : '提交抓取指令失败'
    logger.error('主题抓取指令失败', { mode, reason })
  }
}

async function fetchTopic(topic: Topic, mode: FetchMode) {
	actionMessage.value = ''
	try {
		const result = await request<{ queued: number; status: SchedulerStatus }>(`/api/scheduler/topics/${topic.id}/fetch?mode=${mode}`, { method: 'POST' })
		scheduler.value = result.status
		actionMessage.value = `主题“${topic.title}”已按 ${mode} 模式加入队列`
		logger.info('单主题抓取指令已提交', { topicId: topic.id, fetchMode: mode })
	} catch (reason) {
		actionMessage.value = reason instanceof Error ? reason.message : '提交单主题抓取失败'
		logger.error('单主题抓取指令失败', { topicId: topic.id, fetchMode: mode, reason })
	}
}

async function loadTopics() {
  topicsLoading.value = true
  try {
    const status = activeTopicStatus.value
    const result = await request<TopicPageResponse>(`/api/topics?status=${status}&page=${topicPages.value[status]}&page_size=${topicPageSize.value}`)
    topicItems.value = result.items
    topicCounts.value = result.counts
    topicTotal.value = result.total
    topicTotalPages.value = result.total_pages
    topicPages.value[status] = result.page
  } catch (reason) {
    logger.error('读取索引主题失败', { reason })
  } finally {
    topicsLoading.value = false
  }
}

async function loadProxySettings() {
  proxyLoading.value = true
  try {
    proxyStatus.value = await request<ProxyStatus>('/api/settings/proxy')
    proxyForm.value = {
      mode: proxyStatus.value.config.mode,
      url: proxyStatus.value.config.url ?? '',
    }
  } catch (reason) {
    actionMessage.value = reason instanceof Error ? reason.message : '读取代理配置失败'
    logger.error('读取代理配置失败', { reason })
  } finally {
    proxyLoading.value = false
  }
}

async function saveProxySettings() {
  proxySaving.value = true
  actionMessage.value = ''
  try {
    proxyStatus.value = await request<ProxyStatus>('/api/settings/proxy', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(proxyForm.value),
    })
    proxyForm.value = {
      mode: proxyStatus.value.config.mode,
      url: proxyStatus.value.config.url ?? '',
    }
    actionMessage.value = '代理配置已生效，后续创建的抓取请求将使用新配置'
    logger.info('代理配置已更新', { mode: proxyForm.value.mode })
  } catch (reason) {
    actionMessage.value = reason instanceof Error ? reason.message : '保存代理配置失败'
    logger.error('保存代理配置失败', { mode: proxyForm.value.mode, reason })
  } finally {
    proxySaving.value = false
  }
}

// 日志文件由后端按日期切分；管理端仅轮询最后一段内容，避免长日志影响页面性能。
async function refreshLogTail() {
  if (activePage.value !== 'logs') return
  logLoading.value = true
  logError.value = ''
  try {
    const result = await request<LogTailResponse>(`/api/logs/${selectedLog.value}/tail?lines=160`)
    logEntries.value = result.lines
    logRefreshedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (reason) {
    logError.value = reason instanceof Error ? reason.message : '读取日志失败'
    logger.error('读取工作流日志失败', { stream: selectedLog.value, reason })
  } finally {
    logLoading.value = false
  }
}

function selectLog(stream: LogStream) {
  selectedLog.value = stream
  void refreshLogTail()
}

function selectPage(key: PageKey) {
  activePage.value = key
  actionMessage.value = ''
  logger.info('切换管理页面', { page: key })
  if (key === 'settings') void loadProxySettings()
  if (key === 'logs') void refreshLogTail()
  if (key === 'indexer') void loadTopics()
}

onMounted(() => {
  void refreshStatus('initial')
  void loadProxySettings()
  statusRefreshTimer = window.setInterval(() => {
    void refreshStatus('poll')
    if (activePage.value === 'indexer') void loadTopics()
  }, 2000)
  logRefreshTimer = window.setInterval(() => void refreshLogTail(), 2000)
})

onUnmounted(() => {
  if (logRefreshTimer !== undefined) window.clearInterval(logRefreshTimer)
  if (statusRefreshTimer !== undefined) window.clearInterval(statusRefreshTimer)
})
</script>

<template>
  <main class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="brand-mark"><i></i><i></i><i></i></span>
        <span>Colly<span>Robot</span></span>
      </div>

      <div class="workspace-label">WORKSPACE</div>
      <nav class="navigation" aria-label="主导航">
        <button
          v-for="item in navItems"
          :key="item.key"
          class="nav-item"
          :class="{ active: activePage === item.key }"
          type="button"
          @click="selectPage(item.key)"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          <span>{{ item.label }}</span>
        </button>
      </nav>

      <div class="sidebar-footer">
        <div class="service-state">
          <span class="state-dot" :class="{ stopped: !scheduler.running }"></span>
          <div>
            <strong>{{ schedulerState }}</strong>
            <small>API · localhost:8080</small>
          </div>
        </div>
        <button class="profile" type="button" @click="selectPage('settings')">
          <span class="avatar">CR</span>
          <span><strong>本地管理员</strong><small>开发环境</small></span>
          <span class="chevron">›</span>
        </button>
      </div>
    </aside>

    <section class="content-area">
      <header class="topbar">
        <div>
          <div class="breadcrumb">管理控制台 / <span>{{ activeLabel }}</span></div>
          <h1>{{ activeLabel }}</h1>
        </div>
        <div class="top-actions">
          <button class="icon-button" type="button" aria-label="刷新状态" @click="refreshStatus('manual')">↻</button>
          <button class="icon-button" type="button" aria-label="通知">◌</button>
        </div>
      </header>

      <div v-if="connectionError" class="connection-warning">
        <span>!</span> 无法连接后端：{{ connectionError }}
        <button type="button" @click="refreshStatus('manual')">重试</button>
      </div>

      <div v-if="actionMessage" class="action-message">{{ actionMessage }}</div>

      <section v-if="activePage === 'dashboard'" class="page dashboard-page">
        <div class="hero-panel">
          <div>
            <p class="eyebrow">CRAWLER OPERATIONS</p>
            <h2>把论坛内容采集<br />变成一条可控的流水线。</h2>
            <p class="hero-copy">索引、调度、分页抓取与 UID 增量更新状态，都在一个工作台中掌握。</p>
          </div>
          <div class="hero-orbit" aria-hidden="true">
            <span class="orbit-ring ring-one"></span><span class="orbit-ring ring-two"></span>
            <span class="orbit-core">{{ scheduler.active_workers }}</span>
            <span class="orbit-label">WORKERS</span>
          </div>
        </div>

        <div class="metric-grid">
          <article class="metric-card"><span>调度状态</span><strong>{{ schedulerState }}</strong><small :class="{ danger: !scheduler.running }">{{ scheduler.running ? 'Worker 正在待命，等待入队指令' : '调度器已停止' }}</small></article>
          <article class="metric-card"><span>活跃 Worker</span><strong>{{ scheduler.active_workers }}</strong><small>上限 {{ scheduler.limits.workers }} 个主题</small></article>
          <article class="metric-card"><span>待处理主题</span><strong>{{ scheduler.queued }}</strong><small>来自内存任务队列</small></article>
          <article class="metric-card accent"><span>已发现主题</span><strong>{{ scheduler.indexed }}</strong><small>本次运行累计索引</small></article>
        </div>

        <div class="dashboard-columns">
          <article class="panel activity-panel">
            <div class="panel-heading"><div><p class="eyebrow">LIVE FLOW</p><h3>抓取流水线</h3></div><span class="live-pill"><i></i> 实时</span></div>
            <div class="pipeline">
              <div class="pipeline-step done"><span>01</span><div><strong>论坛索引</strong><small>{{ scheduler.indexing ? '正在递归访问列表页' : '等待新的索引任务' }}</small></div><b>{{ scheduler.indexing ? '进行中' : '就绪' }}</b></div>
              <div class="pipeline-line"></div>
              <div class="pipeline-step"><span>02</span><div><strong>内存队列</strong><small>已缓存 {{ scheduler.queued }} 个待抓取主题</small></div><b>队列</b></div>
              <div class="pipeline-line"></div>
              <div class="pipeline-step"><span>03</span><div><strong>内容抓取</strong><small>Colly 分页并发与 UID 去重</small></div><b>{{ scheduler.active_workers }} Worker</b></div>
            </div>
          </article>
          <article class="panel summary-panel">
            <div class="panel-heading"><div><p class="eyebrow">RUN SUMMARY</p><h3>本次运行</h3></div><button type="button" class="text-button" @click="selectPage('tasks')">查看任务</button></div>
            <div class="donut" :style="{ '--progress': `${completionRate * 3.6}deg` }"><div><strong>{{ completionRate }}%</strong><small>完成率</small></div></div>
            <div class="summary-list"><span><i class="green"></i> 已完成 <b>{{ scheduler.completed }}</b></span><span><i class="red"></i> 失败 <b>{{ scheduler.failed }}</b></span></div>
          </article>
        </div>
      </section>

      <section v-else-if="activePage === 'indexer'" class="page">
        <div class="section-intro"><p class="eyebrow">INDEX PIPELINE</p><h2>{{ scheduler.indexing ? '索引中' : '空闲中' }}</h2><p>{{ scheduler.indexing ? '正在按列表页构建主题索引；已发现的主题会持续保存为“等待抓取”。' : '索引器当前空闲。开始索引后，发现的新主题将保存为“等待抓取”，由抓取任务页显式入队。' }}</p></div>
        <div class="index-actions"><button class="primary-button" type="button" :disabled="scheduler.indexing || !scheduler.running" @click="controlIndex('start')">开始索引</button><button class="secondary-button" type="button" :disabled="!scheduler.indexing" @click="controlIndex('cancel')">中断索引</button></div>
        <div v-if="topicsLoading && topicItems.length === 0" class="settings-loading">正在读取已索引主题…</div>
        <article v-else class="panel topic-group" :class="activeTopicSection.key">
          <div class="topic-tabs" role="tablist" aria-label="主题状态">
            <button v-for="section in topicSections" :key="section.key" class="topic-tab" :class="{ active: activeTopicStatus === section.key }" type="button" role="tab" :aria-selected="activeTopicStatus === section.key" @click="selectTopicStatus(section.key)">{{ section.label }} <b>{{ section.count }}</b></button>
          </div>
          <div class="panel-heading topic-group-heading"><div><h3>{{ activeTopicSection.label }}</h3><p>{{ activeTopicSection.description }}</p></div><b>{{ activeTopicSection.count }}</b></div>
          <div v-if="topicTotal === 0" class="topic-empty">暂无主题</div>
          <template v-else><div class="topic-list"><div v-for="topic in topicItems" :key="topic.id" class="topic-item"><div><strong>{{ topic.title }}</strong><small>#{{ topic.external_id }} · {{ topic.author_id }}</small><small v-if="topic.last_error" class="topic-error">{{ topic.last_error }}</small></div><div class="topic-actions"><template v-if="activeTopicSection.key === 'done'"><button class="topic-fetch-button incremental" type="button" :disabled="!scheduler.running" @click="fetchTopic(topic, 'incremental')">增量抓取</button><button class="topic-fetch-button validate" type="button" :disabled="!scheduler.running" @click="fetchTopic(topic, 'validate')">校验抓取</button><button class="topic-fetch-button reload" type="button" :disabled="!scheduler.running" @click="fetchTopic(topic, 'reload')">重新拉取</button></template><button class="view-topic-button" type="button" @click="viewTopic(topic)">查看主题</button></div></div></div><div class="topic-pagination"><span>显示 {{ activeTopicRange }}，共 {{ topicTotal }} 条</span><label>每页<select v-model.number="topicPageSize" @change="changeTopicPageSize"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option></select>条</label><div class="pagination-buttons"><button type="button" :disabled="activeTopicPage <= 1 || topicsLoading" @click="changeTopicPage(activeTopicPage - 1)">上一页</button><b>{{ activeTopicPage }} / {{ activeTopicPageCount }}</b><button type="button" :disabled="activeTopicPage >= activeTopicPageCount || topicsLoading" @click="changeTopicPage(activeTopicPage + 1)">下一页</button></div></div></template>
        </article>
      </section>

      <section v-else-if="activePage === 'tasks'" class="page">
        <div class="section-intro"><p class="eyebrow">TOPIC WORKERS</p><h2>抓取任务</h2><p>正文按 Topic + UID 去重，可选择增量、校验更新或完全重新拉取。</p></div>
        <article class="panel table-panel"><div class="panel-heading"><h3>抓取操作</h3><div class="button-row task-actions"><label class="task-mode">抓取模式<select v-model="fetchMode"><option value="incremental">默认 · 仅增量</option><option value="validate">校验 · 更新变动正文</option><option value="reload">重新拉取 · 先删除旧正文</option></select></label><button class="primary-button" type="button" :disabled="!scheduler.running" @click="queueTopics('waiting')">开始抓取等待项</button><button class="secondary-button" type="button" :disabled="!scheduler.running" @click="queueTopics('failed')">重试失败项</button></div></div><div class="task-table-header"><span>主题</span><span>状态</span><span>进度</span><span>更新时间</span></div><div class="task-row"><span><b>等待抓取主题</b><small>使用当前抓取模式加入队列</small></span><span><i class="status-chip queued">队列 {{ scheduler.queued }}</i></span><span><div class="progress-track"><i :style="{ width: `${Math.min(scheduler.queued * 8, 100)}%` }"></i></div></span><span>运行期</span></div></article>
      </section>

      <section v-else-if="activePage === 'logs'" class="page">
        <div class="section-intro"><p class="eyebrow">OBSERVABILITY</p><h2>运行日志</h2><p>索引构建与小说内容抓取分别写入独立的按日切分日志，可在此实时查看工作状态。</p></div>
        <article class="panel log-viewer">
          <div class="panel-heading log-viewer-heading">
            <div class="log-tabs" role="tablist" aria-label="工作流日志">
              <button class="log-tab" :class="{ active: selectedLog === 'indexer' }" type="button" role="tab" @click="selectLog('indexer')">索引构建</button>
              <button class="log-tab" :class="{ active: selectedLog === 'crawler' }" type="button" role="tab" @click="selectLog('crawler')">内容抓取</button>
            </div>
            <div class="log-viewer-actions"><span class="live-pill"><i></i> 每 2 秒刷新</span><button class="text-button" type="button" @click="refreshLogTail">{{ logLoading ? '刷新中…' : '立即刷新' }}</button></div>
          </div>
          <div class="log-meta"><code>{{ selectedLog }}-YYYY-MM-DD.log</code><span v-if="logRefreshedAt">上次读取：{{ logRefreshedAt }}</span></div>
          <div class="log-console" aria-live="polite">
            <p v-if="logError" class="log-placeholder error">读取日志失败：{{ logError }}</p>
            <p v-else-if="logLoading && logEntries.length === 0" class="log-placeholder">正在读取日志…</p>
            <p v-else-if="logEntries.length === 0" class="log-placeholder">暂无日志。启动索引或调度器后，此处会显示实时工作状态。</p>
            <p v-for="(entry, index) in logEntries" v-else :key="`${index}-${entry}`" class="log-line" :class="{ error: entry.includes('ERROR'), warning: entry.includes('WARN') }">{{ entry }}</p>
          </div>
        </article>
      </section>

      <section v-else class="page">
        <div class="section-intro"><p class="eyebrow">SYSTEM PREFERENCES</p><h2>系统设置</h2><p>统一管理抓取网络出口与并发资源上限。</p></div>
        <article class="panel proxy-panel">
          <div class="panel-heading">
            <div><p class="eyebrow">NETWORK ROUTING</p><h3>抓取代理</h3></div>
            <span class="proxy-state" :class="{ enabled: proxyStatus.config.mode !== 'direct' }">{{ proxyStatus.config.mode === 'direct' ? '直连' : '代理已启用' }}</span>
          </div>
          <p class="panel-description">代理统一应用于论坛索引、首页探测和正文分页。修改后，新创建的抓取请求会立即采用该配置。</p>

          <div v-if="proxyLoading" class="settings-loading">正在读取代理配置…</div>
          <form v-else class="proxy-form" @submit.prevent="saveProxySettings">
            <div class="proxy-modes">
              <label class="proxy-option" :class="{ selected: proxyForm.mode === 'direct' }">
                <input v-model="proxyForm.mode" type="radio" value="direct" />
                <span><strong>不使用代理</strong><small>后端直接连接目标网站</small></span>
              </label>
              <label class="proxy-option" :class="{ selected: proxyForm.mode === 'system' }">
                <input v-model="proxyForm.mode" type="radio" value="system" />
                <span><strong>Windows 系统代理</strong><small>读取当前用户的 Internet Settings 静态代理</small></span>
              </label>
              <label class="proxy-option" :class="{ selected: proxyForm.mode === 'manual' }">
                <input v-model="proxyForm.mode" type="radio" value="manual" />
                <span><strong>手动配置</strong><small>支持 HTTP、HTTPS、SOCKS5 与账号密码</small></span>
              </label>
            </div>

            <div v-if="proxyForm.mode === 'manual'" class="form-field">
              <label for="proxy-url">代理地址</label>
              <input id="proxy-url" v-model.trim="proxyForm.url" required type="text" spellcheck="false" placeholder="http://127.0.0.1:7890" />
              <small>示例：<code>http://127.0.0.1:7890</code> 或 <code>socks5://user:password@127.0.0.1:1080</code></small>
            </div>

            <div v-if="proxyForm.mode === 'system'" class="system-proxy-note" :class="{ warning: !proxyStatus.system_available }">
              <strong>{{ proxyStatus.system_available ? '已检测到 Windows 系统代理' : '未检测到可用的 Windows 系统代理' }}</strong>
              <small>{{ proxyStatus.system_available ? proxyStatus.effective_proxy : (proxyStatus.system_error ?? '请先在 Windows 设置中启用静态代理') }}</small>
            </div>

            <div class="proxy-actions">
              <button class="primary-button" type="submit" :disabled="proxySaving">{{ proxySaving ? '正在保存…' : '保存并应用' }}</button>
              <span v-if="proxyStatus.effective_proxy && proxyForm.mode !== 'direct'">当前出口：<code>{{ proxyStatus.effective_proxy }}</code></span>
            </div>
          </form>
        </article>
        <article class="panel config-panel concurrency-panel">
          <div class="panel-heading"><div><p class="eyebrow">RESOURCE LIMITS</p><h3>抓取并发配置</h3></div><span class="proxy-state enabled">运行时生效</span></div>
          <p class="panel-description">调整全局可同时处理的主题数，以及单个主题内部的分页请求并发。Worker 上限设为 0 可暂停领取新的主题。</p>
          <form class="limits-form" @submit.prevent="saveSchedulerLimits">
            <label class="limit-field"><span><strong>全局 Worker 上限</strong><small>同一时间最多处理多少个主题</small></span><input v-model.number="limitsForm.workers" min="0" step="1" type="number" @input="limitsDirty = true" /></label>
            <label class="limit-field"><span><strong>单主题分页并发</strong><small>同一论坛主题内同时最多请求多少页</small></span><input v-model.number="limitsForm.sync_concurrency" min="1" step="1" type="number" @input="limitsDirty = true" /></label>
            <div class="limits-actions"><button class="primary-button" type="submit" :disabled="limitsSaving">{{ limitsSaving ? '正在应用…' : '保存并应用' }}</button><small>当前：{{ scheduler.limits.workers }} 个 Worker · 单主题 {{ scheduler.limits.sync_concurrency }} 页并发</small></div>
          </form>
        </article>
      </section>
    </section>
    <div v-if="selectedTopic" class="topic-dialog-backdrop" role="presentation" @click.self="closeTopicDetails">
      <section class="topic-dialog" role="dialog" aria-modal="true" aria-labelledby="topic-dialog-title">
        <div class="topic-dialog-heading"><div><p class="eyebrow">TOPIC DETAIL</p><h2 id="topic-dialog-title">{{ selectedTopic.title }}</h2></div><button class="dialog-close" type="button" aria-label="关闭主题详情" @click="closeTopicDetails">×</button></div>
        <dl class="topic-detail-grid"><div><dt>本地 ID</dt><dd>{{ selectedTopic.id }}</dd></div><div><dt>来源 ID</dt><dd>{{ selectedTopic.external_id }}</dd></div><div><dt>作者</dt><dd>{{ selectedTopic.author_id }}</dd></div><div><dt>状态</dt><dd>{{ selectedTopic.status }}</dd></div><div><dt>更新时间</dt><dd>{{ new Date(selectedTopic.updated_at).toLocaleString() }}</dd></div><div v-if="selectedTopic.last_error" class="topic-detail-error"><dt>最近错误</dt><dd>{{ selectedTopic.last_error }}</dd></div></dl>
        <div v-if="!readingFullTopic" class="topic-content-section"><div class="topic-content-heading"><h3>正文内容预览</h3><span>前 {{ selectedTopicContents.length }} 条 / 共 {{ selectedTopicContentTotal }} 条</span></div><div v-if="topicDetailsLoading" class="topic-content-empty">正在读取正文…</div><div v-else-if="topicDetailsError" class="topic-content-empty error">{{ topicDetailsError }}</div><div v-else-if="selectedTopicContents.length === 0" class="topic-content-empty">该主题暂无已保存的 PageContent</div><div v-else class="topic-content-list"><article v-for="content in selectedTopicContents" :key="content.uid"><header><b>{{ content.uid }}</b><span>第 {{ content.page_no }} 页 · {{ content.floor }} 楼</span></header><p>{{ content.text }}</p></article></div></div>
        <div v-else class="full-topic-reader"><div class="topic-content-heading"><h3>完整内容</h3><button class="text-button" type="button" @click="showTopicPreview">返回预览</button></div><div v-if="fullTopicLoading" class="topic-content-empty">正在组合完整正文…</div><div v-else-if="fullTopicError" class="topic-content-empty error">{{ fullTopicError }}</div><div v-else-if="!fullTopicText" class="topic-content-empty">该主题暂无正文</div><article v-else>{{ fullTopicText }}</article></div>
        <div class="topic-dialog-actions"><button class="secondary-button" type="button" @click="closeTopicDetails">关闭</button><button v-if="!readingFullTopic" class="primary-button" type="button" :disabled="topicDetailsLoading || selectedTopicContentTotal === 0" @click="readFullTopic">阅读完整内容</button></div>
      </section>
    </div>
  </main>
</template>
