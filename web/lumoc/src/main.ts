import { initTheme } from './theme'
import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

initTheme()
createApp(App).mount('#app')
