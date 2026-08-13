<script setup lang="ts">
import { onMounted, ref } from 'vue'

const props = defineProps<{ topicId: number }>()

interface FullTopicResponse {
  topic_id: number
  title: string
  content_count: number
  text: string
}

const loading = ref(true)
const error = ref('')
const topic = ref<FullTopicResponse | null>(null)

async function loadTopic() {
  loading.value = true
  error.value = ''
  try {
    const response = await fetch(`/api/topics/${props.topicId}/contents/full`)
    if (!response.ok) {
      const payload = await response.json().catch(() => null) as { error?: string } | null
      throw new Error(payload?.error ?? `HTTP ${response.status}`)
    }
    topic.value = await response.json() as FullTopicResponse
    document.title = `${topic.value.title} · CollyRobot`
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '读取完整内容失败'
  } finally {
    loading.value = false
  }
}

onMounted(loadTopic)
</script>

<template>
  <main class="reader-page">
    <header class="reader-header">
      <a class="reader-back" href="/">← 返回管理台</a>
      <span>COLLYROBOT READER</span>
    </header>
    <article class="reader-document">
      <div v-if="loading" class="reader-state">正在读取完整内容…</div>
      <div v-else-if="error" class="reader-state error"><strong>读取失败</strong><p>{{ error }}</p><button type="button" @click="loadTopic">重新加载</button></div>
      <template v-else-if="topic">
        <header class="reader-title"><p>TOPIC #{{ topic.topic_id }}</p><h1>{{ topic.title }}</h1><span>共 {{ topic.content_count }} 条正文内容</span></header>
        <div v-if="topic.text" class="reader-content">{{ topic.text }}</div>
        <div v-else class="reader-state">该主题暂无正文</div>
      </template>
    </article>
  </main>
</template>
