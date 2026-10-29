<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { clock, formatBytes, formatRate, locale, setLocale, t } from 'lumo-shared'
import { applyThemePref, getThemePref, type Theme, type ThemePref } from './theme'
import {
  apiCfg, health, host, logs as fetchLogs, proxies as fetchProxies, serverInfo,
  type LogEntry, type ProxyItem, type ServerInfo,
} from './api'

/* ---------------- 状态 ---------------- */
const PAGE_SIZE = 6
const MAX_EVENTS = 200
const MAX_SAMPLES = 60

const theme = ref<Theme>((document.documentElement.dataset.theme as Theme) || 'dark')
const themePref = ref<ThemePref>(getThemePref())
function setThemePref(p: ThemePref) {
  themePref.value = p
  theme.value = applyThemePref(p)
}
function toggleTheme() {
  setThemePref(theme.value === 'dark' ? 'light' : 'dark')
}

const info = ref<ServerInfo | null>(null)
const proxyList = ref<ProxyItem[]>([])
const events = ref<LogEntry[]>([])
const samplesUp = ref<number[]>([])
const samplesDown = ref<number[]>([])
const connected = ref(false)
const booted = ref(false)
const healthOk = ref<boolean | null>(null)
const prevTotals = ref<{ in: number; out: number } | null>(null)

const filter = ref({ type: 'all', q: '', onlineOnly: false, page: 1 })
const logLevel = ref<'ALL' | 'INFO' | 'WARN' | 'ERROR'>('ALL')
const logLive = ref(true)

let pollTimer: number | undefined

/* ---------------- 派生数据 ---------------- */
const clientNames = computed(() =>
  [...new Set(proxyList.value.map((p) => p.client).filter((c) => c && c !== '—'))])

const onlineCount = computed(() => proxyList.value.filter((p) => p.online).length)

const typeCounts = computed(() => {
  const counts: Record<string, number> = { all: proxyList.value.length }
  for (const p of proxyList.value) counts[p.type] = (counts[p.type] || 0) + 1
  return counts
})

const proxyTypeParts = computed(() =>
  Object.entries(info.value?.proxyTypeCount || {})
    .filter(([, v]) => v > 0)
    .map(([k, v]) => `${k.toUpperCase()} ${v}`))

const filteredProxies = computed(() => {
  const { type, q, onlineOnly } = filter.value
  const kw = q.trim().toLowerCase()
  return proxyList.value.filter((p) => {
    if (type !== 'all' && p.type !== type) return false
    if (onlineOnly && !p.online) return false
    if (kw && !`${p.name} ${p.label} ${p.client} ${p.entry}`.toLowerCase().includes(kw)) return false
    return true
  })
})

const pageCount = computed(() => Math.max(1, Math.ceil(filteredProxies.value.length / PAGE_SIZE)))
const page = computed(() => Math.min(filter.value.page, pageCount.value))
const pageSlice = computed(() =>
  filteredProxies.value.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE))
const pageInfo = computed(() =>
  t('pager.info', String(filteredProxies.value.length), String(page.value), String(pageCount.value)))
const pagerPages = computed(() => Array.from({ length: Math.min(pageCount.value, 5) }, (_, i) => i + 1))

const visibleLogs = computed(() =>
  events.value.filter((e) => logLevel.value === 'ALL' || e.level === logLevel.value))

const rateUp = ref('↑ —')
const rateDown = ref('↓ —')

const sparkPaths = computed(() => {
  const W = 372, H = 54
  const max = Math.max(...samplesDown.value, ...samplesUp.value, 1)
  const draw = (arr: number[]) => {
    if (arr.length < 2) return { line: '', fill: '' }
    const step = W / (arr.length - 1)
    const d = arr.map((v, i) => {
      const x = (i * step).toFixed(1)
      const y = (H - 2 - (v / max) * (H - 8)).toFixed(1)
      return `${i ? 'L' : 'M'}${x} ${y}`
    }).join('')
    return { line: d, fill: `${d}L${W} ${H}L0 ${H}Z` }
  }
  return { down: draw(samplesDown.value), up: draw(samplesUp.value) }
})

const uptimeDays = computed(() => info.value?.uptimeStr || '—')
const peakConns = ref(0)
watch(() => info.value?.curConns, (v) => {
  if (v && v > peakConns.value) peakConns.value = v
})

/* ---------------- 事件流 ---------------- */
function pushEvent(level: string, msg: string) {
  events.value.push({ time: clock(), level, msg })
  if (events.value.length > MAX_EVENTS) events.value.shift()
}

function diffEvents(next: ProxyItem[]) {
  if (!info.value) return
  const prev = new Map(proxyList.value.map((p) => [p.name, p]))
  const seen = new Set<string>()
  for (const p of next) {
    seen.add(p.name)
    const old = prev.get(p.name)
    if (!old) pushEvent('INFO', `代理上线 ${p.label || p.name} · ${p.type.toUpperCase()} · ${p.entry}`)
    else if (old.online !== p.online) {
      pushEvent(p.online ? 'INFO' : 'WARN',
        p.online ? `代理恢复 ${p.label || p.name}` : `代理离线 ${p.label || p.name}（心跳超时或客户端退出）`)
    }
  }
  for (const [name, old] of prev) {
    if (!seen.has(name)) pushEvent('WARN', `代理移除 ${name} · ${old.type}`)
  }
  const prevClients = new Set(clientNames.value)
  for (const c of clientNamesOf(next)) if (!prevClients.has(c)) pushEvent('INFO', `客户端接入 ${c} · ${host()}`)
  for (const c of clientNames.value) if (!clientNamesOf(next).includes(c)) pushEvent('WARN', `客户端断开 ${c}`)
}

const clientNamesOf = (list: ProxyItem[]) =>
  [...new Set(list.map((p) => p.client).filter((c) => c && c !== '—'))]

/* ---------------- 轮询 ---------------- */
async function poll() {
  let nextInfo: ServerInfo
  let nextProxies: ProxyItem[]
  try {
    nextInfo = await serverInfo()
    nextProxies = await fetchProxies(host())
    connected.value = true
    healthOk.value = await health()
  } catch (err: any) {
    if (err?.status === 401) { location.href = '/login'; return }
    connected.value = false
    healthOk.value = null
    booted.value = true
    pushEvent('ERROR', `拉取失败：${err?.message || err}（${apiCfg.base || location.origin}）`)
    return
  }

  // 实时速率：由累计流量差分得到
  if (info.value && prevTotals.value) {
    const dt = apiCfg.interval / 1000
    const dIn = Math.max(0, (nextInfo.totalIn - (info.value.totalIn || 0)) / dt)
    const dOut = Math.max(0, (nextInfo.totalOut - (info.value.totalOut || 0)) / dt)
    samplesUp.value.push(dIn)
    samplesDown.value.push(dOut)
    if (samplesUp.value.length > MAX_SAMPLES) samplesUp.value.shift()
    if (samplesDown.value.length > MAX_SAMPLES) samplesDown.value.shift()
    rateUp.value = '↑ ' + formatRate(dIn)
    rateDown.value = '↓ ' + formatRate(dOut)
  }
  prevTotals.value = { in: nextInfo.totalIn, out: nextInfo.totalOut }

  diffEvents(nextProxies)
  info.value = nextInfo
  proxyList.value = nextProxies

  // 服务端运行事件（环形缓冲）
  const remote = await fetchLogs('ALL', 80)
  events.value = mergeEvents(remote, events.value)
  booted.value = true
}

function mergeEvents(remote: LogEntry[], local: LogEntry[]): LogEntry[] {
  // 服务端事件优先；本地差分事件（连接失败提示等）按时间穿插
  const merged = [...remote.map((e) => ({ ...e, time: normTime(e.time) }))]
  const known = new Set(merged.map((e) => e.time + e.msg))
  for (const e of local) {
    if (!known.has(e.time + e.msg) && (e.level === 'ERROR' || e.level === 'WARN')) merged.push(e)
  }
  merged.sort((a, b) => (a.time < b.time ? -1 : 1))
  return merged.slice(-MAX_EVENTS)
}

function normTime(t: string): string {
  // Go 时间戳 → HH:MM:SS
  const d = new Date(t)
  return isNaN(d.getTime()) ? t : d.toTimeString().slice(0, 8)
}

/* ---------------- 交互 ---------------- */
const menu = ref<{ x: number; y: number; proxy: ProxyItem | null }>({ x: 0, y: 0, proxy: null })

/* ---------------- 服务配置（可编辑） ---------------- */
const srvCfg = ref({ bindPort: null as number | null, kcpPort: 0, vhostHttpPort: 0, vhostHttpsPort: 0, authToken: '', subdomainHost: '', allowPortsStr: '', webPort: 0, webPassword: '', revision: 0 })
const showToken = ref(false)
const tokenGenerated = ref(false)
const tokenCopying = ref(false)
const authMode = computed(() => (info.value as any)?.authMode || 'token')
const authFp = computed(() => (info.value as any)?.authFp || '')

async function generateToken() {
  try {
    const r = await fetch('/api/token/new')
    const d = await r.json()
    if (d.token) {
      srvCfg.value.authToken = d.token
      tokenGenerated.value = true
      showToken.value = true
      showToast('令牌已生成：保存前请先复制，保存后将不再显示')
    } else {
      showToast(d.error || '生成失败', true)
    }
  } catch (e: any) {
    showToast('生成失败：' + (e?.message || e), true)
  }
}

async function copyToken() {
  const v = srvCfg.value.authToken
  if (!v) return
  tokenCopying.value = true
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(v)
    } else {
      const ta = document.createElement('textarea')
      ta.value = v
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    showToast('令牌已复制（保存后将无法再复制）')
  } finally {
    setTimeout(() => { tokenCopying.value = false }, 600)
  }
}
const savedCfg = ref(null)
const cfgLoaded = ref(false)
const savingCfg = ref(false)

watch(info, (v) => {
  if (!v || cfgLoaded.value) return
  cfgLoaded.value = true
  srvCfg.value.bindPort = v.bindPort
  srvCfg.value.kcpPort = v.kcpPort || 0
  srvCfg.value.revision = (v as any).revision || 0
  srvCfg.value.vhostHttpPort = v.vhostHttpPort
  srvCfg.value.vhostHttpsPort = v.vhostHttpsPort
  srvCfg.value.subdomainHost = v.subdomainHost
  srvCfg.value.allowPortsStr = v.allowPortsStr
  if ((v as any).webPort) srvCfg.value.webPort = (v as any).webPort
})

const clone = (o: any) => JSON.parse(JSON.stringify(o))

async function saveServerConfig() {
  savingCfg.value = true
  savedCfg.value = clone(srvCfg.value)
  try {
    const res = await fetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...srvCfg.value, webPort: undefined, webPassword: undefined }),
      // 409 = 页面过期（他人已保存），提示刷新
    })
    const d = await res.json()
    if (res.ok) {
      // 令牌仅用于“写入新值”：保存后立即从表单移除，且服务端已不保存令牌本身
      srvCfg.value.authToken = ''
      tokenGenerated.value = false
      showToken.value = false
      if (savedCfg.value) (savedCfg.value as any).authToken = ''
      showToast(d.restart ? '已保存服务' : '已保存（token/面板密码热生效）')
      if (d.restart) setTimeout(() => location.reload(), 1500)
    } else {
      showToast(d.error || '保存失败', true)
      if (res.status === 409) setTimeout(() => location.reload(), 1800)
    }
  } catch (e: any) {
    showToast('保存失败：' + (e?.message || e), true)
  } finally {
    savingCfg.value = false
  }
}

function resetCfg() {
  if (savedCfg.value) { srvCfg.value = clone(savedCfg.value); showToast('已还原') }
  else { showToast('没有可还原的配置', true) }
}

function logout() {
  fetch('/logout').then(() => { location.href = '/login' })
}

/* ---------------- 语言切换 ---------------- */
const langOpen = ref(false)
function pickLang(l: 'zh' | 'en') {
  setLocale(l)
  langOpen.value = false
}

/* ---------------- 两步验证（2FA） ---------------- */
const tfaOpen = ref(false)
const tfaEnabled = ref(false)
const tfaBusy = ref(false)
const tfaSecret = ref('')
const tfaUri = ref('')
const tfaQr = ref('')
const tfaCode = ref('')
const tfaMsg = ref<{ ok: boolean; text: string } | null>(null)

async function open2FA() {
  langOpen.value = false
  tfaOpen.value = true
  tfaMsg.value = null
  tfaCode.value = ''
  tfaSecret.value = ''
  tfaUri.value = ''
  tfaQr.value = ''
  try {
    const res = await fetch('/api/profile/2fa')
    const d = await res.json()
    tfaEnabled.value = !!d.enabled
    if (res.ok && !d.enabled) await loadSetup()
  } catch {
    tfaMsg.value = { ok: false, text: 'Network error' }
  }
}

async function loadSetup() {
  try {
    const res = await fetch('/api/profile/2fa/setup', { method: 'POST' })
    const d = await res.json()
    if (res.ok) {
      tfaSecret.value = d.secret
      tfaUri.value = d.uri
      tfaQr.value = d.qr
    } else {
      tfaMsg.value = { ok: false, text: d.error || 'setup failed' }
    }
  } catch {
    tfaMsg.value = { ok: false, text: 'Network error' }
  }
}

async function bind2FA() {
  if (tfaBusy.value) return
  if (!/^\d{6}$/.test(tfaCode.value.trim())) {
    tfaMsg.value = { ok: false, text: t('tfa.codePh') }
    return
  }
  tfaBusy.value = true
  try {
    const res = await fetch('/api/profile/2fa/enable', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ secret: tfaSecret.value, code: tfaCode.value.trim() }),
    })
    const d = await res.json()
    if (res.ok) {
      tfaEnabled.value = true
      tfaCode.value = ''
      tfaOpen.value = false
      showToast(t('tfa.bound'))
    } else {
      tfaMsg.value = { ok: false, text: d.error || 'failed' }
    }
  } catch {
    tfaMsg.value = { ok: false, text: 'Network error' }
  } finally {
    tfaBusy.value = false
  }
}

async function unbind2FA() {
  if (tfaBusy.value) return
  if (!/^\d{6}$/.test(tfaCode.value.trim())) {
    tfaMsg.value = { ok: false, text: t('tfa.codePh') }
    return
  }
  tfaBusy.value = true
  try {
    const res = await fetch('/api/profile/2fa/disable', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: tfaCode.value.trim() }),
    })
    const d = await res.json()
    if (res.ok) {
      tfaEnabled.value = false
      tfaCode.value = ''
      tfaOpen.value = false
      showToast(t('tfa.unbound'))
    } else {
      tfaMsg.value = { ok: false, text: d.error || 'failed' }
    }
  } catch {
    tfaMsg.value = { ok: false, text: 'Network error' }
  } finally {
    tfaBusy.value = false
  }
}
const toast = ref<{ msg: string; bad: boolean } | null>(null)
let toastTimer: number | undefined

function showToast(msg: string, bad = false) {
  toast.value = { msg, bad }
  clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toast.value = null }, 2200)
}

async function copy(text: string, msg?: string) {
  try {
    // HTTP（非安全上下文）下 navigator.clipboard 不存在，退回 execCommand
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.setAttribute('readonly', '')
      ta.style.cssText = 'position:fixed;top:-1000px;left:-1000px;opacity:0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (!ok) throw new Error('copy failed')
    }
    showToast(msg || t('copied'))
  } catch {
    showToast(t('copyFailed'), true)
  }
}

function openMenu(e: MouseEvent, p: ProxyItem) {
  const target = e.currentTarget as HTMLElement
  menu.value = { x: 0, y: 0, proxy: p }
  requestAnimationFrame(() => {
    const r = target.getBoundingClientRect()
    const el = document.getElementById('menu')
    if (!el) return
    const w = el.offsetWidth, h = el.offsetHeight
    menu.value.x = Math.min(r.right - w, window.innerWidth - w - 12)
    menu.value.y = Math.min(r.bottom + 6, window.innerHeight - h - 12)
  })
}

function tomlSnippet(): string {
  const i = info.value || ({} as ServerInfo)
  const lines = [
    `bindPort = ${i.bindPort || 7000}`,
    i.kcpPort ? `kcpPort = ${i.kcpPort}` : '',
    i.quicPort ? `quicBindPort = ${i.quicPort}` : '',
    i.vhostHttpPort ? `vhostHTTPPort = ${i.vhostHttpPort}` : '',
    i.vhostHttpsPort ? `vhostHTTPSPort = ${i.vhostHttpsPort}` : '',
    i.subdomainHost ? `subdomainHost = "${i.subdomainHost}"` : '',
    i.maxPoolCount ? `transport.maxPoolCount = ${i.maxPoolCount}` : '',
    i.heartbeatTimeout ? `transport.heartbeatTimeout = ${i.heartbeatTimeout}` : '',
    `transport.tls.force = ${i.tlsForce ? 'true' : 'false'}`,
    `webServer.addr = "127.0.0.1"`,
    `webServer.port = 7500`,
    `auth.method = "token"`,
    `auth.token = "请替换为你的密钥"`,
  ].filter(Boolean)
  return lines.join('\n')
}

function setFilterType(t: string) {
  filter.value.type = t
  filter.value.page = 1
}
function setLogLevel(l: string) {
  logLevel.value = l as typeof logLevel.value
}

function viewRaw(p: ProxyItem) {
  console.log('[' + p.name + ']', JSON.stringify(p, null, 2))
  copy(JSON.stringify(p, null, 2), 'Raw JSON copied')
  menu.value.proxy = null
}
function toggleOnlineOnly() {
  filter.value.onlineOnly = !filter.value.onlineOnly
  filter.value.page = 1
}
function toggleLogLive() {
  logLive.value = !logLive.value
}

const logBody = ref<HTMLElement | null>(null)
watch(visibleLogs, () => {
  const body = logBody.value
  if (!body) return
  const nearBottom = body.scrollHeight - body.scrollTop - body.clientHeight < 40
  if (nearBottom) body.scrollTop = body.scrollHeight
})

function typeClass(t: string) {
  return ['tcp', 'udp', 'http', 'https', 'mux', 'sealed-tcp', 'sealed-udp'].includes(t) ? 't-' + t : 't-tcp'
}

onMounted(() => {
  poll()
  pollTimer = window.setInterval(poll, apiCfg.interval)
  window.addEventListener('click', closeMenu as EventListener)
  window.addEventListener('resize', closeMenu as EventListener)
})
onBeforeUnmount(() => {
  clearInterval(pollTimer)
  window.removeEventListener('click', closeMenu as EventListener)
  window.removeEventListener('resize', closeMenu as EventListener)
})
function closeMenu(e: MouseEvent) {
  const target = e.target as HTMLElement
  if (!target.closest('#menu') && !target.closest('.more')) menu.value.proxy = null
  if (!target.closest('.langsel')) langOpen.value = false
}
</script>

<template>
  <!-- 顶栏 -->
  <header class="topbar">
    <div class="brand">
      <svg width="22" height="22" viewBox="0 0 72 72" fill="none" aria-hidden="true">
        <path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#5A7BFF" stroke-width="7" stroke-linecap="round"/>
        <path d="M10 42H62" stroke="#35E0C8" stroke-width="4.8" stroke-linecap="round"/>
        <circle cx="10" cy="42" r="4" fill="#35E0C8"/><circle cx="62" cy="42" r="4" fill="#35E0C8"/>
      </svg>
      <span class="wordmark">LUMO</span>
      <span class="badge">SERVER</span>
    </div>
    <div class="top-right">
      <span class="welcome">{{ t('welcome') }} <b>LUMO</b></span>
      <button class="themebtn" :title="theme === 'dark' ? '日间模式' : '黑夜模式'" @click="toggleTheme">
        <svg v-if="theme === 'dark'" width="15" height="15" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.4"/><path d="M8 1.2V3M8 13V14.8M1.2 8H3M13 8H14.8M3.2 3.2L4.5 4.5M11.5 11.5L12.8 12.8M12.8 3.2L11.5 4.5M4.5 11.5L3.2 12.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
        <svg v-else width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6A5.8 5.8 0 1 0 13.4 9.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
      </button>
      <div class="langsel" :class="{ open: langOpen }">
        <button class="langbtn" type="button" @click.stop="langOpen = !langOpen">
          <span class="flag">{{ locale === 'zh' ? '🇨🇳' : '🇺🇸' }}</span>
          <span>{{ locale === 'zh' ? '简体中文' : 'English' }}</span>
          <svg class="caret" width="9" height="9" viewBox="0 0 10 10" fill="none">
            <path d="M2 3.8L5 6.8L8 3.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </button>
        <div class="langmenu" v-if="langOpen">
          <button type="button" :class="{ cur: locale === 'zh' }" @click="pickLang('zh')"><span>🇨🇳</span> 简体中文</button>
          <button type="button" :class="{ cur: locale === 'en' }" @click="pickLang('en')"><span>🇺🇸</span> English</button>
        </div>
      </div>
      <button class="acctbtn" type="button" :title="t('accountSecurity')" @click="open2FA">
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
          <path d="M8 2.2L13 4.1V8.2C13 11.1 10.9 13.1 8 14.1C5.1 13.1 3 11.1 3 8.2V4.1L8 2.2Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
          <path d="M6.1 8L7.5 9.4L10 6.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>{{ t('accountSecurity') }}</span>
      </button>
      <button class="exitbtn" type="button" @click="logout">
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
          <path d="M10 2.5H4.5A1.5 1.5 0 0 0 3 4V12A1.5 1.5 0 0 0 4.5 13.5H10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
          <path d="M7 8H13.5M13.5 8L11 5.8M13.5 8L11 10.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>{{ t('logout') }}</span>
      </button>
    </div>
  </header>

  <div class="boot" v-if="!booted">
    <span class="spinner"></span>
    <span>正在读取服务端状态…</span>
  </div>

  <main class="layout" v-if="booted">
    <!-- 顶部状态区 -->
    <section class="band" id="band">
      <article class="card status-card">
        <div class="clabel"><span>{{ t('status.title') }}</span><span class="health">{{ connected ? `healthz ${healthOk === false ? '—' : '200'}` : 'healthz —' }}</span></div>
        <div class="smain"><span class="sdot" :class="{ bad: !connected }"></span><b>{{ connected ? t('status.ok') : t('status.down') }}</b></div>
        <div class="uptime">{{ t('status.uptime') }} {{ uptimeDays }}</div>
        <div class="pid">{{ t('status.lastRestart') }} {{ info?.startedAt || '—' }} · PID {{ info?.pid || '—' }}</div>
      </article>

      <article class="card kpi-card">
        <div class="kpi">
          <div class="k-label">{{ t('kpi.clients') }}</div>
          <div class="k-val"><b>{{ info?.clientCounts ?? '—' }}</b><span class="k-unit">{{ t('unit.clients') }}</span></div>
          <div class="k-sub" style="color:var(--ok)">{{ t('today.n', clientNames.length) }}</div>
        </div>
        <div class="kpi">
          <div class="k-label">{{ t('kpi.proxies') }}</div>
          <div class="k-val"><b>{{ onlineCount || proxyList.length }}</b><span class="k-unit">{{ t('unit.proxies') }}</span></div>
          <div class="k-sub">{{ proxyTypeParts.join(' · ') || `共 ${proxyList.length} 条` }}</div>
        </div>
        <div class="kpi">
          <div class="k-label">{{ t('kpi.conns') }}</div>
          <div class="k-val"><b>{{ info?.curConns ?? '—' }}</b><span class="k-unit">{{ t('unit.conns') }}</span></div>
          <div class="k-sub">{{ t('peak.n', peakConns) }}</div>
        </div>
        <div class="kpi">
          <div class="k-label">{{ t('kpi.traffic') }}</div>
          <div class="k-val"><b>{{ ((Number(info?.totalIn) + Number(info?.totalOut)) / 1024 ** 3).toFixed(1) || '0.0' }}</b><span class="k-unit">GB</span></div>
          <div class="k-sub">↑ {{ formatBytes(info?.totalIn || 0) }} · ↓ {{ formatBytes(info?.totalOut || 0) }}</div>
        </div>
      </article>


    </section>

          <article class="card rate-card" id="rate">
        <div class="clabel"><span>{{ t('rate.title') }}</span><span class="health unk">{{ t('rate.sampling') }}</span></div>
        <div class="rate-vals">
          <span class="up">{{ rateUp }}</span>
          <span class="down">{{ rateDown }}</span>
        </div>
        <svg class="spark" viewBox="0 0 372 54" preserveAspectRatio="none" aria-hidden="true">
          <path :d="sparkPaths.down.fill" fill="#35E0C8" fill-opacity="0.13"></path>
          <path :d="sparkPaths.down.line" fill="none" stroke="#35E0C8" stroke-width="1.8" stroke-linejoin="round"></path>
          <path :d="sparkPaths.up.line" fill="none" stroke="#8FA7FF" stroke-width="1.6" stroke-linejoin="round" opacity="0.8"></path>
        </svg>
      </article>

    <!-- 代理与客户端 -->
    <section class="card panel" id="proxies">
      <div class="phead">
        <h2>{{ t('proxies.title') }}</h2>
        <span class="chip">{{ onlineCount }} {{ t('proxies.online') }}</span>
        <span class="spacer"></span>
        <label class="search">
          <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
            <circle cx="7" cy="7" r="4.6" stroke="#5D6A80" stroke-width="1.6"/>
            <path d="M10.4 10.4L13.4 13.4" stroke="#5D6A80" stroke-width="1.6" stroke-linecap="round"/>
          </svg>
          <input v-model="filter.q" type="search" :placeholder="t('proxies.search')" autocomplete="off">
        </label>
        <button class="btn ghost sm" @click="poll(); showToast(t('refreshed'))">{{ t('proxies.refresh') }}</button>
      </div>

      <div class="tabs" role="tablist">
        <button v-for="tp in ['all', 'tcp', 'udp', 'http', 'https', 'mux', 'sealed-tcp', 'sealed-udp']" :key="tp"
                class="tab" role="tab" :data-type="tp"
                :aria-selected="filter.type === tp" @click="setFilterType(tp)">
          {{ tp === 'all' ? t('proxies.all') : tp.toUpperCase() }} <span>{{ typeCounts[tp] || 0 }}</span>
        </button>
        <span class="spacer"></span>
        <label class="switch-row" @click="toggleOnlineOnly">
          <span class="switch" role="switch" :aria-checked="filter.onlineOnly ? 'true' : 'false'" tabindex="0"></span>
          {{ t('proxies.onlineOnly') }}
        </label>
      </div>

      <div class="table">
        <div class="phead-row">
          <span>{{ t('col.name') }}</span><span>{{ t('col.type') }}</span><span>{{ t('col.client') }}</span>
          <span>{{ t('col.entry') }}</span><span>{{ t('col.traffic') }}</span><span>{{ t('col.status') }}</span><span></span>
        </div>
        <div id="rows">
          <div v-if="!pageSlice.length" class="lempty">{{ t('empty') }}</div>
          <div v-for="p in pageSlice" :key="p.name" class="prow" :class="{ offline: !p.online }">
            <span class="c-name">{{ p.label || p.name }}</span>
            <span class="c-type" :class="typeClass(p.type)">{{ p.type.toUpperCase() }}</span>
            <span class="c-client" :title="p.client">{{ p.client }}</span>
            <span class="c-entry" :title="p.entry">{{ p.entry }}</span>
            <span class="c-traffic">{{ p.trafficIn + p.trafficOut ? formatBytes(p.trafficIn + p.trafficOut) : '—' }}</span>
            <span class="state-pill" :class="p.online ? 'on' : p.waiting ? 'wait' : 'off'">{{ p.online ? t('state.online') : p.waiting ? t('state.waiting') : t('state.offline') }}</span>
            <button class="more" aria-label="more" @click.stop="openMenu($event, p)">⋯</button>
          </div>
        </div>
      </div>

      <div class="lfoot">
        <span class="spacer">{{ pageInfo }}</span>
        <div style="display:flex;gap:6px">
          <button v-for="n in pagerPages" :key="n" class="pg" :aria-current="n === page ? 'true' : undefined" @click="filter.page = n">{{ n }}</button>
        </div>
      </div>
    </section>

    <!-- 服务配置 -->
    <aside class="card panel" id="config">
      <div class="phead">
        <h2>{{ t('config.title') }}</h2>
        <span class="chip">lumos.toml</span>
        <span class="spacer"></span>
        <button class="btn ghost sm" @click="copy(tomlSnippet(), 'lumos.toml ' + t('copied'))">{{ t('config.copy') }}</button>
      </div>
      <div class="cbody" v-if="cfgLoaded">
        <div class="field-row">
          <div class="field"><label>{{ t('config.bindPort') }}</label><input v-model.number="srvCfg.bindPort" type="number"></div>
          <div class="field"><label>{{ t('config.kcpPort') }}</label><input v-model.number="srvCfg.kcpPort" type="number" min="0" max="65535" :placeholder="t('config.kcpOff')"></div>
        </div>
        <div class="field-row">
          <div class="field"><label>{{ t('config.vhostHttp') }}</label><input v-model.number="srvCfg.vhostHttpPort" type="number" placeholder="0"></div>
          <div class="field"><label>{{ t('config.vhostHttps') }}</label><input v-model.number="srvCfg.vhostHttpsPort" type="number" placeholder="0"></div>
        </div>
        <div class="field"><label>{{ t('config.subdomain') }}</label><input v-model="srvCfg.subdomainHost" :placeholder="t('config.subdomainPh')"></div>
        <div class="field"><label>{{ t('config.allowPorts') }}</label><input v-model="srvCfg.allowPortsStr" :placeholder="t('config.allowPortsPh')"></div>
        <div class="field"><label>{{ t('config.authToken') }}<span v-if="authMode === 'key' && authFp" style="margin-left:8px;color:var(--tx-4);font-family:var(--mono)">公钥 {{ authFp }}</span></label>
          <div style="position:relative">
            <input v-model="srvCfg.authToken" :type="tokenGenerated ? 'text' : 'password'" autocomplete="off" readonly
                   :placeholder="authMode === 'key' ? '已保存（不再显示）。要更换请点「生成新令牌」' : '空 = 保持不变；点「生成新令牌」轮换'">
            <button v-if="srvCfg.authToken && tokenGenerated" style="position:absolute;right:6px;top:50%;transform:translateY(-50%);background:none;border:0;cursor:pointer;color:var(--tx-3)" type="button" @click="copyToken">{{ tokenCopying ? '已复制' : '复制' }}</button>
          </div>
          <div v-if="srvCfg.authToken && tokenGenerated" style="font-size:10px;color:var(--warn);line-height:1.6;margin-top:4px">
            ⚠ 保存前先复制！保存后服务端只存公钥，令牌永久不可再查看；所有现有连接会断开，需把新令牌填到客户端。
          </div>
          <div style="margin-top:6px">
            <button class="btn ghost sm" type="button" @click="generateToken">生成新令牌</button>
          </div>
        </div>
        <div class="sw-list">
          <span class="switch-row" style="cursor:default">
            <span class="switch" :class="{ on: (srvCfg.kcpPort || 0) > 0 }" role="switch" :aria-checked="(srvCfg.kcpPort || 0) > 0 ? 'true' : 'false'" tabindex="-1"></span>
            KCP {{ (srvCfg.kcpPort || 0) > 0 ? (locale === 'zh' ? `传输 · UDP ${srvCfg.kcpPort}` : `transport · UDP ${srvCfg.kcpPort}`) : (locale === 'zh' ? '未启用' : 'disabled') }}
          </span>
          <span class="switch-row" style="cursor:default">
            <span class="switch on" role="switch" aria-checked="true" tabindex="-1"></span>
            TCP {{ locale === 'zh' ? '传输 · 隧道端口 ' + (srvCfg.bindPort || '') : 'transport · tunnel port ' + (srvCfg.bindPort || '') }}
          </span>
        </div>
        <div class="note">{{ locale === 'zh' ? '注意：隧道端口与 KCP 端口保存后立即生效，HTTP/HTTPS 虚拟主机端口需重启 lumos' : 'Note: port changes take effect after a lumos restart' }}</div>
      </div>
      <div class="cbody" v-else><div class="note">读取配置…</div></div>
      <div class="cfoot" v-if="cfgLoaded">
        <button class="btn ghost sm" @click="resetCfg">{{ t('config.reset') }}</button>
        <span class="spacer"></span>
        <button class="btn sm" :disabled="savingCfg" @click="saveServerConfig">{{ savingCfg ? t('config.saving') : t('config.save') }}</button>
      </div>
    </aside>

    <!-- 日志 -->
    <section class="card panel" id="logs">
      <div class="phead">
        <h2>{{ t('logs.title') }}</h2>
        <span class="chip" :style="{ color: logLive ? '#3DDC97' : '#6C788E' }">{{ logLive ? 'LIVE' : 'PAUSED' }}</span>
        <span class="spacer"></span>
        <div class="tabs" style="height:auto;border:0;padding:0;gap:6px">
          <button v-for="l in ['ALL', 'INFO', 'WARN', 'ERROR']" :key="l"
                  class="tab" :aria-selected="logLevel === l" @click="setLogLevel(l)">{{ l }}</button>
        </div>
        <label class="switch-row" style="margin-left:6px" @click="toggleLogLive">
          <span class="switch" role="switch" :aria-checked="logLive ? 'true' : 'false'" tabindex="0"></span>
          {{ t('logs.live') }}
        </label>
      </div>
      <div class="lbody" ref="logBody">
        <div v-if="!visibleLogs.length" class="lempty">{{ locale === 'zh' ? '等待事件…' : 'Waiting for events…' }}</div>
        <div v-for="(e, i) in visibleLogs" :key="i" class="lline" :class="{ warn: e.level === 'WARN', error: e.level === 'ERROR' }">
          <span class="ts">{{ e.time }}</span>
          <span class="lv" :class="'lv-' + e.level">{{ e.level }}</span>
          <span class="msg" :title="e.msg">{{ e.msg }}</span>
        </div>
      </div>
    </section>
  </main>

  <div class="menu" id="menu" v-if="menu.proxy" :style="{ left: menu.x + 'px', top: menu.y + 'px' }">
    <button @click="copy(menu.proxy.name, t('copied')); menu.proxy = null">{{ locale === 'zh' ? '复制代理名称' : 'Copy proxy name' }}</button>
    <button @click="copy(menu.proxy.entry, t('copied')); menu.proxy = null">{{ locale === 'zh' ? '复制公网入口' : 'Copy public entry' }}</button>
    <hr>
    <button @click="menu.proxy && viewRaw(menu.proxy)">{{ locale === 'zh' ? '查看原始数据' : 'View raw data' }}</button>
  </div>
  <div class="toast" :class="{ show: toast, bad: toast?.bad }"><span class="dot"></span><span>{{ toast?.msg }}</span></div>

  <!-- 账号安全 / 两步验证 -->
  <div class="modal-mask" v-if="tfaOpen" @click.self="tfaOpen = false">
    <div class="modal tfa">
      <header>
        <div class="thd">
          <h3>{{ t('tfa.title') }}</h3>
          <p>{{ t('c.tfa.sub') }}</p>
        </div>
        <button class="x" type="button" @click="tfaOpen = false">✕</button>
      </header>

      <template v-if="tfaEnabled">
        <p class="tnote">{{ t('c.tfa.onNote') }}</p>
        <div class="tfield">
          <label>{{ t('tfa.code') }}</label>
          <input v-model="tfaCode" type="text" inputmode="numeric" maxlength="6"
                 :placeholder="t('login.2faPlaceholder')" autocomplete="one-time-code">
        </div>
        <div class="tactions">
          <button class="btn ghost sm" type="button" @click="tfaOpen = false">{{ t('c.n.cancel') }}</button>
          <button class="btn danger sm" type="button" :disabled="tfaBusy" @click="unbind2FA">{{ t('c.tfa.disable') }}</button>
        </div>
      </template>

      <template v-else>
        <div class="tqr" v-if="tfaQr">
          <img :src="tfaQr" alt="TOTP QR" width="184" height="184">
        </div>
        <p class="tnote" style="text-align:center;margin:0">{{ t('c.tfa.scanNote') }} <b class="mono">{{ tfaSecret }}</b></p>
        <div class="tfield">
          <label>{{ t('c.tfa.codeToEnable') }}</label>
          <input v-model="tfaCode" type="text" inputmode="numeric" maxlength="6"
                 :placeholder="t('login.2faPlaceholder')" autocomplete="one-time-code">
        </div>
        <p class="tmsg" v-if="tfaMsg" :class="{ ok: tfaMsg.ok }">{{ tfaMsg.text }}</p>
        <div class="tactions">
          <button class="btn ghost sm" type="button" @click="tfaOpen = false">{{ t('c.n.cancel') }}</button>
          <button class="btn sm" type="button" :disabled="tfaBusy" @click="bind2FA">{{ tfaBusy ? '…' : t('c.tfa.enable') }}</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style src="./style.css"></style>
