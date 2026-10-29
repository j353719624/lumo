import { initTheme } from './theme'
import { createApp } from 'vue'
import Login from './Login.vue'

initTheme()
createApp(Login).mount('#app')
