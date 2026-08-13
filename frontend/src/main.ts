import { createApp } from 'vue'
import App from './App.vue'
import TopicReader from './TopicReader.vue'
import './style.css'

const readerMatch = window.location.pathname.match(/^\/reader\/(\d+)\/?$/)
createApp(readerMatch ? TopicReader : App, readerMatch ? { topicId: Number(readerMatch[1]) } : undefined).mount('#app')
