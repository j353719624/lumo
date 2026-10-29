<script setup lang="ts">
import { ref } from 'vue'
import { applyThemePref, type Theme } from './theme'

const theme = ref<Theme>((document.documentElement.dataset.theme as Theme) || 'dark')
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  applyThemePref(theme.value)
}

const account = ref('')
const password = ref('')
const remember = ref(true)
const autostart = ref(false)
const showPwd = ref(false)
const state = ref<'idle' | 'loading' | 'done'>('idle')
const alertMsg = ref('')
const alertShow = ref(false)
const successShow = ref(false)

function showError(msg: string) {
  alertMsg.value = msg
  alertShow.value = false
  void document.body.offsetWidth
  alertShow.value = true
  successShow.value = false
}

function clearError() { alertShow.value = false }

async function submit() {
  if (state.value !== 'idle') return
  const acc = account.value.trim()
  const pass = password.value

  if (!acc) { showError('请输入账号，可以是邮箱或用户名'); return }
  if (acc.includes('@') && !/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(acc)) {
    showError('邮箱格式看起来不对，请检查 @ 前后的内容'); return
  }
  if (!pass) { showError('请输入密码'); return }
  if (pass.length < 6) { showError('密码至少 6 位'); return }

  clearError()
  state.value = 'loading'
  try {
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ account: acc, password: pass }),
    })
    const d = await res.json()
    if (res.ok) {
      try {
        if (remember.value) localStorage.setItem('lumo.client.account', acc)
        else localStorage.removeItem('lumo.client.account')
        localStorage.setItem('lumo.client.autostart', autostart.value ? '1' : '0')
      } catch { /* ignore */ }
      state.value = 'done'
      successShow.value = true
      setTimeout(() => { location.href = '/' }, 350)
    } else {
      state.value = 'idle'
      showError(d.error || '账号或密码不正确。连续 5 次失败会锁定 10 分钟')
    }
  } catch {
    state.value = 'idle'
    showError('无法连接本地服务，请确认进程正在运行')
  }
}

try {
  const saved = localStorage.getItem('lumo.client.account')
  if (saved) account.value = saved
  if (localStorage.getItem('lumo.client.autostart') === '1') autostart.value = true
} catch { /* ignore */ }
</script>

<template>
  <button class="themefab" type="button" :title="theme === 'dark' ? '日间模式' : '黑夜模式'" @click="toggleTheme">
    <svg v-if="theme === 'dark'" width="16" height="16" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.4"/><path d="M8 1.2V3M8 13V14.8M1.2 8H3M13 8H14.8M3.2 3.2L4.5 4.5M11.5 11.5L12.8 12.8M12.8 3.2L11.5 4.5M4.5 11.5L3.2 12.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
    <svg v-else width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6A5.8 5.8 0 1 0 13.4 9.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
  </button>
  <div class="backdrop"></div>
  <main class="wrap">
    <section class="window">
      <!-- 左：品牌 -->
      <aside class="brandside">
        <svg class="mark" width="46" height="46" viewBox="0 0 72 72" fill="none" aria-hidden="true">
          <path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#5A7BFF" stroke-width="6.2" stroke-linecap="round"/>
          <path d="M10 42H62" stroke="#35E0C8" stroke-width="4.2" stroke-linecap="round"/>
          <circle cx="10" cy="42" r="3.8" fill="#35E0C8"/>
          <circle cx="62" cy="42" r="3.8" fill="#35E0C8"/>
        </svg>
        <div class="brandname">LUMO</div>
        <div class="brandcn">隧光 · 客户端</div>
        <span class="hairline"></span>
        <div class="brandtag">把内网服务带在身边</div>

        <div class="pitch">
          <h1>连接，<br>像在本地一样。</h1>
          <p>一条隧道，两端点亮：为每台设备建一套配置，端口映射、密钥与压缩收在同一张表单里。</p>
        </div>

        <div class="feats">
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <rect x="2" y="2" width="12" height="5" rx="1.2" stroke="currentColor" stroke-width="1.4"/>
                <rect x="2" y="9" width="12" height="5" rx="1.2" stroke="currentColor" stroke-width="1.4"/>
              </svg>
            </span>
            <div><b>本地引擎</b><span>单进程托管全部连接，开机自启</span></div>
          </div>
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path d="M8 2.2L13 4.1V8.2C13 11.1 10.9 13.1 8 14.1C5.1 13.1 3 11.1 3 8.2V4.1L8 2.2Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                <path d="M6.1 8L7.5 9.4L10 6.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
              </svg>
            </span>
            <div><b>安全访问</b><span>sealed-tcp / sealed-udp / PUNCH 密钥直连</span></div>
          </div>
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path d="M2.2 5.4H13.8M2.2 10.6H13.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                <circle cx="6" cy="5.4" r="1.7" fill="currentColor" stroke="currentColor" stroke-width="1.4"/>
                <circle cx="10.2" cy="10.6" r="1.7" fill="currentColor" stroke="currentColor" stroke-width="1.4"/>
              </svg>
            </span>
            <div><b>多种协议</b><span>TCP / UDP / HTTP(S) / MUX</span></div>
          </div>
        </div>

        <div class="brandfoot">
          <span class="pill"><span class="dot"></span>引擎就绪</span>
          <span class="sep"></span>
          <span>登录后即可管理全部隧道</span>
        </div>
      </aside>

      <!-- 右：表单 -->
      <div class="formside">
        <h1>登录</h1>

        <form @submit.prevent="submit" novalidate>
          <div class="field">
            <label for="acc">账号</label>
            <div class="ctrl">
              <span class="lead">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="5.4" r="2.7" stroke="currentColor" stroke-width="1.4"/>
                  <path d="M3 13.4C3.5 11.2 5.5 9.8 8 9.8C10.5 9.8 12.5 11.2 13 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                </svg>
              </span>
              <input id="acc" v-model="account" type="text" placeholder="邮箱或用户名"
                     autocomplete="username" spellcheck="false" autofocus>
            </div>
          </div>

          <div class="field">
            <label for="pwd">密码</label>
            <div class="ctrl has-eye">
              <span class="lead">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                  <rect x="3" y="7.2" width="10" height="6.6" rx="1.8" stroke="currentColor" stroke-width="1.4"/>
                  <path d="M5.7 7.2V5.4C5.7 4.1 6.7 3.1 8 3.1C9.3 3.1 10.3 4.1 10.3 5.4V7.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                  <circle cx="8" cy="10.5" r="1" fill="currentColor"/>
                </svg>
              </span>
              <input id="pwd" v-model="password" :type="showPwd ? 'text' : 'password'"
                     placeholder="请输入密码" autocomplete="current-password">
              <button class="eye" type="button" :aria-pressed="showPwd ? 'true' : 'false'" aria-label="显示密码" @click="showPwd = !showPwd">
                <svg class="on" width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <path d="M1.6 8C3.4 4.9 5.6 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C12.6 11.1 10.4 12.6 8 12.6C5.6 12.6 3.4 11.1 1.6 8Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                  <circle cx="8" cy="8" r="2.1" fill="currentColor"/>
                </svg>
                <svg class="off" width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <path d="M2.6 5.5C2 6.2 1.7 7 1.6 8C3.4 11.1 5.6 12.6 8 12.6C9 12.6 10 12.3 10.9 11.7M6.2 3.7C6.8 3.5 7.4 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C14 8.7 13.5 9.3 13 9.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
                  <path d="M2.6 2.6L13.4 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                </svg>
              </button>
            </div>
          </div>

          <div class="opts">
            <label class="opt">
              <input type="checkbox" v-model="remember">
              <span class="sw"></span>
              <span>记住账号</span>
            </label>
            <label class="opt">
              <input type="checkbox" v-model="autostart">
              <span class="sw"></span>
              <span>开机自启</span>
            </label>
          </div>

          <div class="alert" :class="{ show: alertShow }" role="alert" aria-live="polite">
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6.4" stroke="#FF5F73" stroke-width="1.4"/>
              <path d="M8 4.9V8.7" stroke="#FF5F73" stroke-width="1.6" stroke-linecap="round"/>
              <circle cx="8" cy="11.1" r=".95" fill="#FF5F73"/>
            </svg>
            <span>{{ alertMsg || '账号或密码不正确' }}</span>
          </div>

          <div class="success" :class="{ show: successShow }">
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6.4" stroke="#3DDC97" stroke-width="1.4"/>
              <path d="M5.2 8.2L7.1 10.1L10.8 6.2" stroke="#3DDC97" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            <span>登录成功</span>
            <a href="/">进入客户端 →</a>
          </div>

          <button class="submit" type="submit" :data-state="state" :disabled="state !== 'idle'">
            <span class="spinner" aria-hidden="true"></span>
            <span class="label">{{ state === 'loading' ? '正在验证…' : state === 'done' ? '已登录' : '登录' }}</span>
          </button>
        </form>

        <div class="or">或</div>

        <button class="alt" type="button" @click="showError('访问令牌登录：粘贴服务端生成的 token 即可，无需密码')">
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
            <circle cx="5.6" cy="10.4" r="2.6" stroke="currentColor" stroke-width="1.4"/>
            <path d="M7.5 8.5L12.6 3.4M10.6 5.4L12.2 7M12 4L13.6 5.6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
          </svg>
          使用访问令牌登录
        </button>
      </div>
    </section>

    <div class="foot">
      <span class="ver">v0.1.0</span>
      <span class="sep"></span>
      <a href="#" @click.prevent="showError('当前已是最新版本 v0.1.0')">检查更新</a>
      <span class="sep"></span>
      <a href="#" @click.prevent="showError('帮助文档与反馈入口，正式版会打开浏览器')">帮助与反馈</a>
    </div>
    <div class="hint">账号与密码由 <b>lumoc ui -user</b> 启动参数指定</div>
  </main>
</template>

<style src="./login.css"></style>
