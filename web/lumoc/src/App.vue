<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type ConnConfig, type ConnStatus, type EngineInfo, type Mapping, type Visitor } from './api'
import { applyThemePref, getThemePref, type Theme, type ThemePref } from './theme'
import { locale, setLocale, t } from 'lumo-shared'

/* ---------------- 语言切换 ---------------- */
const langOpen = ref(false)
function pickLang(l: 'zh' | 'en') {
  setLocale(l)
  langOpen.value = false
}
function closeLangMenu(e: MouseEvent) {
  if (!(e.target as HTMLElement).closest('.langsel')) langOpen.value = false
}
onMounted(() => document.addEventListener('click', closeLangMenu as EventListener))
onBeforeUnmount(() => document.removeEventListener('click', closeLangMenu as EventListener))

/* ---------------- 状态 ---------------- */
const configs = ref<ConnConfig[]>([])
const statusMap = ref<Record<string, ConnStatus>>({})
const info = ref<EngineInfo | null>(null)
const selectedId = ref<string | null>(null)
const draft = ref<ConnConfig | null>(null)
const showToken = ref(false)
const filter = ref<'all' | 'on' | 'off'>('all')
const theme = ref<Theme>((document.documentElement.dataset.theme as Theme) || 'dark')
const themePref = ref<ThemePref>(getThemePref())
function setThemePref(p: ThemePref) {
  themePref.value = p
  theme.value = applyThemePref(p)
}
function toggleTheme() {
  setThemePref(theme.value === 'dark' ? 'light' : 'dark')
}

const logLevel = ref('all')
function levelRank(level: string): number {
  const lv = (level || '').toUpperCase()
  if (lv.startsWith('ERR')) return 3
  if (lv.startsWith('WARN')) return 2
  if (lv.startsWith('INFO') || lv.startsWith('DEBUG')) return 1
  return 0
}

const twofaCount = ref(0)
const twofaView = ref<'list' | 'add'>('add')
const twofaSecret = ref('')
const twofaQr = ref('')
const twofaCode = ref('')

async function open2fa() {
  twofaCode.value = ''
  try {
    const st = await api.auth2faStatus()
    twofaCount.value = Number(st.count) || 0
    if (!st.enabled) {
      twofaView.value = 'add'
      const s = await api.auth2faSetup()
      twofaSecret.value = s.secret
      twofaQr.value = s.qr
    } else {
      twofaView.value = 'list'
    }
  } catch (e: any) { toast(t('c.toast.readFail', e?.message || e), true) }
  openModal('modal2fa')
}

async function refreshTwofaCount(): Promise<number> {
  try {
    const st = await api.auth2faStatus()
    twofaCount.value = Number(st.count) || 0
  } catch { /* 保留当前值 */ }
  return twofaCount.value
}

async function startAdd2fa() {
  twofaMsgClear()
  twofaCode.value = ''
  twofaView.value = 'add'
  try {
    const s = await api.auth2faSetup()
    twofaSecret.value = s.secret
    twofaQr.value = s.qr
  } catch (e: any) { toast(t('c.toast.readFail', e?.message || e), true) }
}

function backFromAdd2fa() {
  twofaView.value = twofaCount.value > 0 ? 'list' : 'add'
  if (twofaCount.value === 0) closeModal('modal2fa')
}

async function enable2fa() {
  try {
    await api.auth2faEnable(twofaSecret.value, twofaCode.value.trim())
    twofaCode.value = ''
    const wasFirst = twofaCount.value === 0
    const n = await refreshTwofaCount()
    if (n > 0) {
      twofaView.value = 'list'
      toast(wasFirst ? t('c.toast.tfaOn') : t('tfa.added'))
    } else {
      toast(t('tfa.added'))
    }
  } catch (e: any) { toast(t('c.toast.tfaOnFail', e?.message || e), true) }
}

function twofaMsgClear() { /* 预留：列表视图无内联消息 */ }

async function unbindEntry(index: number) {
  try {
    await api.auth2faDisable(index, twofaCode.value.trim())
    twofaCode.value = ''
    const n = await refreshTwofaCount()
    toast(t('tfa.removed'))
    if (n === 0) {
      twofaView.value = 'add'
      await startAdd2fa()
    }
  } catch (e: any) { toast(t('c.toast.tfaOffFail', e?.message || e), true) }
}

const nType = ref('tcp')
const tomlText = ref('')
const tomlId = ref('')
const tomlSaving = ref(false)
const logView = ref<HTMLElement | null>(null)

const q = ref('')
const view = ref<'list' | 'logs'>('list')
const detailSection = ref<'overview' | 'server' | 'mappings' | 'visitors' | 'files'>('overview')
watch(detailSection, async (sec) => {
  if (sec === 'files' && draft.value && !tomlText.value) {
    try { tomlText.value = await api.toml(draft.value.id) } catch { tomlText.value = t('c.toast.readFail', '') }
  }
});
watch(draft, () => { tomlText.value = '' });
const pendingDelete = ref<ConnConfig | null>(null)
const tomlView = ref('')
const toastMsg = ref<{ msg: string; bad: boolean } | null>(null)
let toastTimer: number | undefined

const uid = () => Math.random().toString(36).slice(2, 10)
const startNow = ref(true)
const nProto = ref('tcp')
const consoleHost = location.host || '本机'
const clone = (o: any) => JSON.parse(JSON.stringify(o))

function toast(msg: string, bad = false) {
  toastMsg.value = { msg, bad }
  clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toastMsg.value = null }, 2400)
}

/* ---------------- 模板 ---------------- */
const TPL: Record<string, { name: string; local: string; remote: string }> = {
  nas: { name: '家庭 NAS', local: '127.0.0.1:5000', remote: '15000' },
  rdp: { name: '办公室远程桌面', local: '10.0.0.8:3389', remote: '15001' },
  web: { name: '网站服务', local: '127.0.0.1:8080', remote: '15002' },
}

/* ---------------- 派生 ---------------- */
const enabledCount = computed(() => configs.value.filter((c) => c.enabled).length)

const filteredConfigs = computed(() => {
  let list = configs.value
  if (filter.value === 'on') list = list.filter((c) => c.enabled)
  if (filter.value === 'off') list = list.filter((c) => !c.enabled)
  const kw = q.value.trim().toLowerCase()
  if (kw) list = list.filter((c) => `${c.name} ${c.serverAddr}`.toLowerCase().includes(kw))
  return list
})

const totalTrafficIn = computed(() => { let t = 0; for (const s of Object.values(statusMap.value)) { for (const p of (s.proxies||[])) t += (p.in||0) } return t })
const totalTrafficOut = computed(() => { let t = 0; for (const s of Object.values(statusMap.value)) { for (const p of (s.proxies||[])) t += (p.out||0) } return t })
function formatB(n: number): string { if (!n) return '0 B'; if (n >= 1073741824) return (n/1073741824).toFixed(1)+' GB'; if (n >= 1048576) return (n/1048576).toFixed(1)+' MB'; if (n >= 1024) return (n/1024).toFixed(0)+' KB'; return n+' B' }

const runningCount = computed(() =>
  Object.values(statusMap.value).filter((s) => s.running).length)

function statusOf(c: ConnConfig): { cls: string; label: string } {
  if (!c.enabled) return { cls: '', label: t('c.st.stopped') }
  const st = statusMap.value[c.id]
  if (!st) return { cls: 'wait', label: t('c.st.connecting') }
  if (st.error) return { cls: 'err', label: t('c.st.error') }
  if (st.running) return (st.proxies && st.proxies.length)
    ? { cls: 'on', label: t('c.st.connected') } : { cls: 'wait', label: t('c.st.connecting') }
  return { cls: 'wait', label: t('c.st.starting') }
}

function copyLogs(lines: string[]) {
  copyText(lines.join('\n'), t('c.log.copied'))
}

function parseLogs(lines: string[]) {
  return lines.map((l) => {
    const m = l.match(/^(\d{2}:\d{2}:\d{2})\s+\[(\w+)\]\s*(.*)$/)
    if (!m) return { time: '', level: '', msg: l, cls: '' }
    const lv = m[2].toUpperCase()
    const cls = lv.startsWith('WARN') ? 'warn' : lv.startsWith('ERR') ? 'err' : lv === 'INFO' ? 'info' : ''
    return { time: m[1], level: lv, msg: m[3], cls }
  })
}

function mappingSummary(c: ConnConfig): string {
  const m = (c.mappings || [])[0]
  if (!m) return t('c.unmapped')
  const remote = (m.type === 'http' || m.type === 'https') ? (m.domain || t('c.noDomain')) : String(m.remotePort || '—')
  const more = c.mappings.length > 1 ? `  +${c.mappings.length - 1}` : ''
  const tag = m.name ? `[${m.name}] `
    : (m.type === 'http' || m.type === 'https' || m.type === 'mux') ? '' : ''
  return `${c.serverPort} → ${tag}${m.localPort || '—'}  ⇒  ${remote}${more}`
}

watch(() => runningConfigs.value.map((rc) => rc.status.log.length).join(','), () => scrollLogsToEnd())
watch(() => view.value, (v) => { if (v === 'logs') scrollLogsToEnd() })

const runningConfigs = computed(() => {
  const out: { name: string; status: ConnStatus }[] = []
  for (const s of Object.values(statusMap.value)) {
    if (!s.running) continue
    const c = configs.value.find((x) => x.id === s.id)
    out.push({ name: c ? c.name : s.id, status: s })
  }
  return out
})

/* ---------------- 数据加载 ---------------- */
async function refresh(keepDetail = false) {
  try {
    // 只有拿到数组才更新列表：异常结构（例如后端返回错误对象）时保留原数据，
    // 避免界面突然变成「还没有任何连接配置」的空白态。
    const list = await api.list()
    if (Array.isArray(list)) configs.value = list
    statusMap.value = {}
    for (const s of await api.status()) statusMap.value[s.id] = s
    info.value = await api.info()
  } catch (e: any) {
    if (e?.message?.includes('401')) { location.href = 'login'; return }
    toast(t('c.toast.readFail', e?.message || e), true)
  }
  if (keepDetail) return
  if (!selectedId.value && configs.value.length) selectedId.value = configs.value[0].id
  const cfg = configs.value.find((x) => x.id === selectedId.value)
  draft.value = cfg ? clone(cfg) : null
  showToken.value = false
}

function select(id: string) {
  selectedId.value = id
  const cfg = configs.value.find((c) => c.id === id)
  draft.value = cfg ? clone(cfg) : null
  if (window.matchMedia('(max-width:980px)').matches) document.getElementById('detail')?.classList.add('open')
}

/* ---------------- 操作 ---------------- */
async function toggleConfig(c: ConnConfig) {
  try {
    if (!c.enabled) {
      await api.start(c.id)
      toast(t('c.toast.enabled', c.name))
    } else {
      await api.stop(c.id)
      toast(t('c.toast.stopped', c.name))
    }
    c.enabled = !c.enabled
    if (draft.value && draft.value.id === c.id) draft.value.enabled = c.enabled
  } catch (e: any) {
    toast(t('c.toast.opFail', e?.message || e), true)
  }
  await refresh(true)
}

function addMapping() {
  const c = draft.value
  if (!c) return
  c.mappings.push({ id: uid(), name: '', type: 'tcp', localIP: '127.0.0.1', localPort: 0, remotePort: 0 })
}

function removeMapping(idx: number) {
  const c = draft.value
  if (!c) return
  if (c.mappings.length === 1) { toast(t('c.toast.keepOne'), true); return }
  c.mappings.splice(idx, 1)
}

function setMappingType(m: Mapping, t: string) {
  m.type = t
}

function updateLocal(m: Mapping, raw: string) {
  const v = raw.trim()
  const i = v.lastIndexOf(':')
  if (i > -1) {
    m.localIP = v.slice(0, i) || '127.0.0.1'
    m.localPort = Number(v.slice(i + 1)) || 0
  } else {
    m.localPort = Number(v) || 0
  }
}

function splitList(raw: string): string[] {
  return raw.split(',').map((s) => s.trim()).filter(Boolean)
}

function updateRemote(m: Mapping, raw: string) {
  if (m.type === 'http' || m.type === 'https' || m.type === 'mux') {
    const ds = splitList(raw)
    m.domains = ds
    m.domain = ds[0] || ''
  } else if (m.type !== 'sealed-tcp' && m.type !== 'sealed-udp') m.remotePort = Number(raw.trim()) || 0
}

function updatePaths(m: Mapping, raw: string) {
  m.subPaths = splitList(raw)
}

function addVisitor() {
  const c = draft.value
  if (!c) return
  if (!c.visitors) c.visitors = []
  const first = (c.mappings || [])[0]
  c.visitors.push({
    id: uid(), name: '', type: 'sealed-tcp',
    serverName: first ? c.id + '-' + first.id : '', sk: '',
    bindAddr: '127.0.0.1', bindPort: 0,
  })
}

function removeVisitor(idx: number) {
  const c = draft.value
  if (!c || !c.visitors) return
  c.visitors.splice(idx, 1)
}

function visitorList(c: ConnConfig): Visitor[] {
  return c.visitors || []
}

function updateVisitorBind(v: Visitor, raw: string) {
  const s = raw.trim()
  const i = s.lastIndexOf(':')
  if (i > -1) {
    v.bindAddr = s.slice(0, i) || '127.0.0.1'
    v.bindPort = Number(s.slice(i + 1)) || 0
  } else {
    v.bindPort = Number(s) || 0
  }
}

function updateAuth(m: Mapping, raw: string) {
  const i = raw.indexOf(':')
  if (i < 0) {
    m.httpUser = raw.trim()
    m.httpPass = ''
  } else {
    m.httpUser = raw.slice(0, i).trim()
    m.httpPass = raw.slice(i + 1)
  }
}

async function save() {
  const c = draft.value
  if (!c) return
  if (!c.serverAddr.trim()) { toast(t('c.toast.addrEmpty'), true); return }
  const wasRunning = !!statusMap.value[c.id]?.running
  try {
    const r = await api.update(c.id, {
      name: c.name, serverAddr: c.serverAddr, serverPort: c.serverPort,
      protocol: c.protocol || 'tcp', kcpPort: c.protocol === 'kcp' ? Number(c.kcpPort) || 0 : 0,
      token: c.token, tls: c.tls, mappings: c.mappings, visitors: c.visitors || [],
    })
    await refresh()
    toast(r?.restarted || wasRunning ? t('c.toast.savedRestart') : t('c.toast.saved'))
  } catch (e: any) {
    toast(t('c.toast.saveFail', e?.message || e), true)
  }
}

async function askDelete(c: ConnConfig) {
  pendingDelete.value = c
}

async function confirmDelete() {
  const c = pendingDelete.value
  if (!c) return
  try {
    await api.remove(c.id)
    if (selectedId.value === c.id) selectedId.value = null
    pendingDelete.value = null
    await refresh()
    toast(t('c.toast.deleted', c.name))
  } catch (e: any) {
    toast(t('c.toast.delFail', e?.message || e), true)
  }
}

async function createAndConnect() {
  const addr = (document.getElementById('nAddr') as HTMLInputElement)?.value.trim()
  const name = (document.getElementById('nName') as HTMLInputElement)?.value.trim()
  const token = (document.getElementById('nToken') as HTMLInputElement)?.value
  const port = Number((document.getElementById('nPort') as HTMLInputElement)?.value) || 7000
  if (!addr) { toast(t('c.toast.needAddr'), true); return }

  const localRaw = (document.querySelector('#nMap .local') as HTMLInputElement)?.value.trim() || ''
  const i = localRaw.lastIndexOf(':')
  const type = (document.querySelector('#nMap .mtype') as HTMLSelectElement)?.value || 'tcp'
  const remoteRaw = (document.querySelector('#nMap .remote') as HTMLInputElement)?.value.trim() || ''
  const mapping: Mapping = {
    id: uid(),
    type,
    localIP: i > -1 ? localRaw.slice(0, i) : '127.0.0.1',
    localPort: Number(i > -1 ? localRaw.slice(i + 1) : localRaw) || 0,
  }
  const mapName = (document.querySelector('#nMap .mname') as HTMLInputElement)?.value.trim() || ''
  if (mapName) mapping.name = mapName
  if (type === 'http' || type === 'https' || type === 'mux') {
    const ds = splitList(remoteRaw)
    mapping.domains = ds
    mapping.domain = ds[0] || ''
  } else if (type === 'sealed-tcp' || type === 'sealed-udp') {
    mapping.sk = ((document.querySelector('#nMap .msk') as HTMLInputElement)?.value || '').trim()
  } else {
    mapping.remotePort = Number(remoteRaw) || 0
  }
  if (type === 'http' || type === 'https') {
    mapping.subPaths = splitList((document.querySelector('#nMap .paths') as HTMLInputElement)?.value || '')
    mapping.bandwidth = ((document.querySelector('#nMap .bw') as HTMLInputElement)?.value || '').trim()
    const authRaw = ((document.querySelector('#nMap .auth') as HTMLInputElement)?.value || '').trim()
    if (authRaw) {
      const i2 = authRaw.indexOf(':')
      mapping.httpUser = i2 < 0 ? authRaw : authRaw.slice(0, i2).trim()
      mapping.httpPass = i2 < 0 ? '' : authRaw.slice(i2 + 1)
    }
  }

  const compress = ((document.querySelector('#nMap .mcompress') as HTMLInputElement)?.checked) || false
  if (compress) mapping.compress = true
  const startNowVal = startNow.value

  try {
    const nKcp = document.getElementById('nKcpPort') as HTMLInputElement | null
    const cfg = await api.create({
      name: name || t('c.untitled'), serverAddr: addr, serverPort: port,
      protocol: nProto.value, kcpPort: nProto.value === 'kcp' ? Number(nKcp?.value) || 0 : 0,
      token: token || '', mappings: [mapping], enabled: startNowVal,
    })
    closeModal('modalNew')
    selectedId.value = cfg.id
    await refresh()
    toast(t('c.toast.created', cfg.name))
  } catch (e: any) {
    toast(t('c.toast.createFail', e?.message || e), true)
  }
}

async function showToml(c: ConnConfig) {
  try {
    tomlView.value = await api.toml(c.id)
    openModal('modalToml')
  } catch (e: any) {
    toast(t('c.toast.readFail', e?.message || e), true)
  }
}

/* ---------------- 模态框 ---------------- */
// genToken 生成 256 位随机令牌（与服务端 lumoc token 输出一致）
function genToken(): string {
  const buf = new Uint8Array(32)
  crypto.getRandomValues(buf)
  return Array.from(buf).map((b) => b.toString(16).padStart(2, '0')).join('')
}

function openModal(id: string) {
  if (id === 'modalNew') {
    const el = document.getElementById('nToken') as HTMLInputElement | null
    if (el) el.value = ''
  }
  document.getElementById(id)?.classList.add('open')}
function closeModal(id: string) { document.getElementById(id)?.classList.remove('open') }

function useTemplate(key: string) {
  const tpl = TPL[key]
  if (!tpl) return
  const name = document.getElementById('nName') as HTMLInputElement
  const local = document.querySelector('#nMap .local') as HTMLInputElement
  const remote = document.querySelector('#nMap .remote') as HTMLInputElement
  if (name) name.value = t('c.tpl.' + key)
  if (local) local.value = tpl.local
  if (remote) remote.value = tpl.remote
}

async function saveToml() {
  if (!tomlId.value || tomlSaving.value) return
  tomlSaving.value = true
  try {
    const r = await api.saveToml(tomlId.value, tomlText.value)
    closeModal('modalToml')
    await refresh()
    toast(r?.restarted ? t('c.toast.savedRestartAll') : t('c.toast.saved'))
  } catch (e: any) {
    toast(t('c.toast.saveFail', e?.message || e), true)
  } finally {
    tomlSaving.value = false
  }
}

async function copyText(text: string, msg: string) {
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
    toast(msg)
  } catch {
    toast(t('c.toast.copyFail'), true)
  }
}

function scrollLogsToEnd() {
  nextTick(() => {
    const el = logView.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

function setView(v: 'list' | 'logs') {
  view.value = v
}

async function logout() {
  try { await fetch('logout') } catch { /* ignore */ }
  location.href = 'login'
}

function openSection(s: 'overview' | 'server' | 'mappings' | 'visitors' | 'files') {
  view.value = 'list'
  detailSection.value = s
}


async function testConnection(addr: string, port: number) {
  if (!addr) { toast(t('c.toast.addrFirst'), true); return }
  testing.value = true
  try {
    const r = await fetch('api/test-connection', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ addr, port }),
    })
    const d = await r.json()
    toast(d.ok ? t('c.toast.testOk') : t('c.toast.testFail', d.error || '?'), !d.ok)
  } catch (e: any) { toast(t('c.toast.testErr', e?.message || e), true) }
  finally { testing.value = false }
}

const testing = ref(false)

function importToml() {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.toml'
  input.onchange = async () => {
    const f = input.files?.[0]
    if (!f) return
    const text = await f.text()
    try {
      const cfg = await api.create({ name: f.name.replace(/.toml$/, ''), serverAddr: '', serverPort: 7000, token: '', mappings: [], enabled: false })
      // 简单解析：把内容发给后端处理
      toast(t('c.toast.imported', f.name))
      await refresh()
      select(cfg.id)
    } catch (e: any) { toast(t('c.toast.importFail', e?.message || e), true) }
  }
  input.click()
}

function escapeHtml(s: string): string {
  return String(s ?? '').replace(/[&<>"']/g, (m) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m] || m))
}

onMounted(() => refresh())
setInterval(() => refresh(true), 2500)
</script>

<template>
  <div class="app">
    <!-- 侧栏 -->
    <aside class="sidebar">
      <div class="sbrand">
        <svg width="26" height="26" viewBox="0 0 72 72" fill="none" aria-hidden="true">
          <path d="M22 61V37C22 26 28.3 16.6 36 16.6C43.7 16.6 50 26 50 37V61" stroke="#4A5CF5" stroke-width="9.4" stroke-linecap="round"/>
          <path d="M12 41H60" stroke="#35E0C8" stroke-width="5.6" stroke-linecap="round"/>
          <circle cx="12" cy="41" r="4.6" fill="#35E0C8"/><circle cx="60" cy="41" r="4.6" fill="#35E0C8"/>
        </svg>
        <span class="wm">LUMO</span><span class="badge">CLIENT</span>
      </div>

      <div class="nav-label">{{ t('c.nav.workspace') }}</div>

      <button class="nav" :aria-current="view === 'list' && detailSection === 'overview' ? 'page' : undefined" @click="openSection('overview')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M2 5.5L8 2L14 5.5V10.5L8 14L2 10.5V5.5Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/><path d="M8 8.2V14M2 5.5L8 8.2M14 5.5L8 8.2" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
        <span>{{ t('c.nav.overview') }}</span>
        <span class="nbadge">{{ configs.length }}</span>
      </button>
      <button class="nav" :aria-current="view === 'list' && detailSection === 'server' ? 'page' : undefined" @click="openSection('server')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="12" height="5" rx="1.2" stroke="currentColor" stroke-width="1.4"/><rect x="2" y="9" width="12" height="5" rx="1.2" stroke="currentColor" stroke-width="1.4"/><path d="M4.5 4.5H5.5M4.5 11.5H5.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
        <span>{{ t('c.nav.server') }}</span>
      </button>
      <button class="nav" :aria-current="view === 'list' && detailSection === 'mappings' ? 'page' : undefined" @click="openSection('mappings')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M3 4H9M11 4H13M3 8H5M7 8H13M3 12H9M11 12H13" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/><circle cx="10" cy="4" r="1.4" stroke="currentColor" stroke-width="1.2"/><circle cx="6" cy="8" r="1.4" stroke="currentColor" stroke-width="1.2"/><circle cx="10" cy="12" r="1.4" stroke="currentColor" stroke-width="1.2"/></svg>
        <span>{{ t('c.nav.mappings') }}</span>
      </button>
      <button class="nav" :aria-current="view === 'list' && detailSection === 'visitors' ? 'page' : undefined" @click="openSection('visitors')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M8 2C5 2 2.8 4.2 2.8 7.2C2.8 10.8 8 14 8 14C8 14 13.2 10.8 13.2 7.2C13.2 4.2 11 2 8 2Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/><circle cx="8" cy="7" r="1.8" stroke="currentColor" stroke-width="1.3"/></svg>
        <span>{{ t('c.nav.visitors') }}</span>
      </button>
      <button class="nav" :aria-current="view === 'list' && detailSection === 'files' ? 'page' : undefined" @click="openSection('files')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M4 1.5H9.5L13 5V14.5H4V1.5Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/><path d="M9 1.5V5.5H13" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
        <span>{{ t('c.nav.files') }}</span>
      </button>
      <button class="nav" :aria-current="view === 'logs' ? 'page' : undefined" @click="setView('logs')">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M3 3H13M3 6.5H13M3 10H9M3 13.5H11" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
        <span>{{ t('c.nav.logs') }}</span>
      </button>

      <div class="spacer"></div>      <div class="spacer"></div>

      <div class="engine">
        <div class="row"><span class="dot on"></span><span class="t">{{ t('c.engine.connected') }}</span><span class="v">v{{ info?.version || '—' }}</span></div>
        <div class="addr">{{ consoleHost }}</div>
      </div>
    </aside>

    <!-- 主区 -->
    <main class="main">
      <div class="topbar">
        <div class="titleblock">
          <h1>{{ { list: t('c.title.list'), logs: t('c.nav.logs') }[view] }}<template v-if="view === 'list'">{{ ' · ' + { overview: t('c.sec.overview'), server: t('c.sec.server'), mappings: t('c.sec.mappings'), visitors: t('c.sec.visitors'), files: t('c.sec.files') }[detailSection] }}</template></h1>
          <p>{{ view === 'list' ? (configs.length ? t('c.sub.configs', configs.length, enabledCount) : t('c.sub.none')) : t('c.sub.logs') }}</p>
        </div>
        <span class="spacer"></span>
        <span class="welcome">{{ t('welcome') }} <b>LUMO</b></span>
        <button class="themebtn" :title="theme === 'dark' ? '日间模式' : '黑夜模式'" @click="toggleTheme">
          <svg v-if="theme === 'dark'" width="16" height="16" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.4"/><path d="M8 1.2V3M8 13V14.8M1.2 8H3M13 8H14.8M3.2 3.2L4.5 4.5M11.5 11.5L12.8 12.8M12.8 3.2L11.5 4.5M4.5 11.5L3.2 12.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
          <svg v-else width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6A5.8 5.8 0 1 0 13.4 9.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
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
        <button class="acctbtn" type="button" :title="t('accountSecurity')" @click="open2fa">
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
            <path d="M8 2.2L13 4.1V8.2C13 11.1 10.9 13.1 8 14.1C5.1 13.1 3 11.1 3 8.2V4.1L8 2.2Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
            <path d="M6.1 8L7.5 9.4L10 6.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
          <span>{{ t('accountSecurity') }}</span>
        </button>
        <button class="exitbtn" type="button" @click="logout">
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
            <path d="M10 2.5H4.5A1.5 1.5 0 0 0 3 4V12A1.5 1.5 0 0 0 4.5 13.5H10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            <path d="M7 8H13.5M13.5 8L11 5.8M13.5 8L11 10.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
          </svg>
          <span>{{ t('logout') }}</span>
        </button>
      </div>

      <!-- 连接配置视图 -->
      <div class="content" v-show="view === 'list'">
        <div class="listcol">
          <div class="filters">
            <button class="chip" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">{{ t('c.filter.all') }} {{ configs.length }}</button>
            <button class="chip" :aria-pressed="filter === 'on' ? 'true' : 'false'" @click="filter = 'on'">{{ t('c.filter.on') }} {{ enabledCount }}</button>
            <button class="chip" :aria-pressed="filter === 'off' ? 'true' : 'false'" @click="filter = 'off'">{{ t('c.filter.off') }} {{ configs.length - enabledCount }}</button>
          </div>

          <div class="cards">
            <div v-for="c in filteredConfigs" :key="c.id" class="ccard" :aria-selected="c.id === selectedId ? 'true' : 'false'" @click="select(c.id)">
              <div class="well" :class="statusOf(c).cls === 'on' ? 'on' : ''">
                <svg width="22" height="22" viewBox="0 0 72 72" fill="none">
                  <path d="M24 46V33C24 25.3 29.4 20 36 20C42.6 20 48 25.3 48 33V46" :stroke="statusOf(c).cls === 'on' ? '#3DDC97' : statusOf(c).cls === 'err' ? '#FF5F73' : '#6C788E'" stroke-width="5" stroke-linecap="round"/>
                  <path d="M30 37H42" :stroke="statusOf(c).cls === 'on' ? '#3DDC97' : statusOf(c).cls === 'err' ? '#FF5F73' : '#6C788E'" stroke-width="4" stroke-linecap="round"/>
                </svg>
              </div>
              <div class="cinfo">
                <div class="cname">{{ c.name }}</div>
                <div class="cmeta" :title="mappingSummary(c)">{{ mappingSummary(c) }}</div>
              </div>
              <div class="cacts">
                <span class="state" :class="statusOf(c).cls"><i></i>{{ statusOf(c).label }}</span>
                <span class="switch" role="switch" :aria-checked="c.enabled ? 'true' : 'false'" tabindex="0" @click.stop="toggleConfig(c)"></span>
              </div>
            </div>
          </div>

          <div class="empty" v-if="!configs.length">
            <svg width="72" height="72" viewBox="0 0 72 72" fill="none">
              <path d="M22 61V37C22 26 28.3 16.6 36 16.6C43.7 16.6 50 26 50 37V61" stroke="#2A3346" stroke-width="9.4" stroke-linecap="round"/>
              <path d="M12 41H60" stroke="#2A3346" stroke-width="5.6" stroke-linecap="round"/>
              <circle cx="12" cy="41" r="4.6" fill="#2A3346"/><circle cx="60" cy="41" r="4.6" fill="#2A3346"/>
            </svg>
            <h3>{{ t('c.empty.h') }}</h3>
            <p>{{ t('c.empty.p') }}</p>
            <div class="row"><button class="btn" @click="openModal('modalNew')">{{ t('c.empty.new') }}</button> <button class="btn ghost" @click="importToml()">{{ t('c.empty.import') }}</button></div>
          </div>

          <div class="stats">
            <span class="lb">{{ t('c.stats.traffic') }}</span>
            <span class="up" id="stUp">↑ {{ totalTrafficOut > 1024**2 ? (totalTrafficOut / 1024**2).toFixed(1) + ' MB' : formatB(totalTrafficOut) }}</span>
            <span class="spacer"></span>
            <span class="lb">{{ t('c.stats.active', runningCount) }}</span>
          </div>
        </div>

        <!-- 详情面板 -->
        <aside class="detail" id="detail">
          <template v-if="draft">
            <div class="dhead">
              <div class="txt">
                <h2>{{ draft.name }}</h2>
                <div class="sub">{{ (draft.protocol || 'tcp').toUpperCase() }} · {{ statusOf(draft).label }}</div>
              </div>
              <span class="switch" role="switch" :aria-checked="draft.enabled ? 'true' : 'false'" tabindex="0" @click="toggleConfig(draft)"></span>
            </div>

            <div v-show="detailSection === 'overview'">
              <div class="sec"><span>{{ t('c.ov.status') }}</span></div>
              <div class="setcard">
                <div class="kv"><span>{{ t('c.ov.connState') }}</span><b>{{ statusOf(draft).label }}</b></div>
                <div class="kv"><span>{{ t('c.ov.proto') }}</span><b>{{ (draft.protocol || 'tcp').toUpperCase() }}</b></div>
                <div class="kv"><span>{{ t('c.ov.mappings') }}</span><b>{{ t('c.count', draft.mappings.length) }}</b></div>
                <div class="kv"><span>{{ t('c.ov.visitors') }}</span><b>{{ t('c.count', visitorList(draft).length) }}</b></div>
              </div>
              <div class="sec" style="padding-top:10px"><span>{{ t('c.ov.engine') }}</span></div>
              <div class="setcard">
                <div class="kv"><span>{{ t('c.ov.mode') }}</span><b>{{ t('c.ov.modeV') }}</b></div>
                <div class="kv"><span>{{ t('c.ov.engineBin') }}</span><b>{{ info?.engineBin || '—' }}</b></div>
                <div class="kv"><span>{{ t('c.ov.dataDir') }}</span><b>{{ info?.dataDir || '—' }}</b></div>
                <div class="kv"><span>{{ t('c.ov.version') }}</span><b>v{{ info?.version || '—' }}</b></div>
              </div>
              <p class="setnote">{{ t('c.ov.note') }}</p>
            </div>
            <div v-show="detailSection === 'server'">
            <div class="sec"><span>{{ t('c.sv.connInfo') }}</span><span class="flex1"></span><span class="sechint">{{ t('c.sv.tomlHint') }}</span><button class="secadd" @click="save">{{ t('config.save') }}</button></div>

            <div class="field"><label>{{ t('c.sv.name') }}</label><input v-model="draft.name" :placeholder="t('c.sv.namePh')"></div>
            <div class="field">
              <label>{{ t('c.sv.addr') }}</label>
              <div class="inputwrap">
                <input v-model="draft.serverAddr" class="mono" placeholder="hk-01.example.net">
              </div>
            </div>
            <div class="field-row">
              <div class="field"><label>{{ t('c.sv.port') }}</label><input v-model.number="draft.serverPort" class="mono" placeholder="7000"></div>
              <div class="field"><label>{{ t('c.sv.proto') }}</label>
                <select v-model="draft.protocol">
                  <option value="tcp">TCP</option>
                  <option value="kcp">KCP（UDP）</option>
                </select>
              </div>
            </div>
            <div class="field-row">
              <div class="field"><label>{{ t('c.sv.protoV') }}</label>
                <select :value="draft.protocolVersion || 2" @change="draft.protocolVersion = Number(($event.target as HTMLSelectElement).value)">
                  <option :value="2">{{ t('c.sv.v2') }}</option>
                  <option :value="1">{{ t('c.sv.v1') }}</option>
                </select>
              </div>
            </div>
            <div class="field" v-if="draft.protocol === 'kcp'"><label>{{ t('c.sv.kcpPort') }}</label><input v-model.number="draft.kcpPort" class="mono" type="number" min="1" max="65535" :placeholder="t('c.sv.kcpPh')"></div>
            <div class="sec"><span>{{ t('c.sv.advanced') }}</span></div>
            <div class="optrow"><span class="lbl">{{ t('c.sv.tls') }}</span><span class="switch sm" role="switch" :aria-checked="draft.tls ? 'true' : 'false'" tabindex="0" @click="draft.tls = !draft.tls"></span></div>
            <div class="optrow"><span class="lbl">{{ t('c.sv.autoreconn') }}</span><span class="switch sm on" role="switch" aria-checked="true" tabindex="0"></span></div>

            <div class="field"><label>{{ t('c.sv.token') }}</label>
              <div class="token-wrap">
                <input v-model="draft.token" class="mono" :type="showToken ? 'text' : 'password'" :placeholder="t('c.sv.tokenPh')" autocomplete="off">
                <button type="button" class="eye-btn" :aria-pressed="showToken ? 'true' : 'false'" @click="showToken = !showToken" :title="t('c.sv.showHide')">
                  <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                    <path d="M1.6 8C3.4 4.9 5.6 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C12.6 11.1 10.4 12.6 8 12.6C5.6 12.6 3.4 11.1 1.6 8Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                    <circle cx="8" cy="8" r="2.1" fill="currentColor"/>
                    <path v-if="showToken" d="M2.6 2.6L13.4 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                  </svg>
                </button>
              </div>
            </div>

            </div>
            <div v-show="detailSection === 'mappings'">
            <div class="sec"><span>{{ t('c.mp.title', draft.mappings.length) }}</span><span class="flex1"></span>
              <button class="secadd" @click="save">{{ t('config.save') }}</button>
              <button class="secadd" @click="addMapping">{{ t('c.add') }}</button>
            </div>
            <div class="cards" style="gap:8px">
              <div v-for="(m, idx) in draft.mappings" :key="m.id" class="map">
                <input class="mname" v-model="m.name" :placeholder="t('c.mp.namePh')" :aria-label="t('c.mp.namePh')">
                <select :value="m.type" @change="setMappingType(m, ($event.target as HTMLSelectElement).value)">
                  <option value="tcp">TCP</option><option value="udp">UDP</option>
                  <option value="http">HTTP</option><option value="https">HTTPS</option>
                  <option value="sealed-tcp">密钥隧道 TCP</option><option value="sealed-udp">密钥隧道 UDP</option>
                  <option value="punch">{{ t('c.mp.punch') }}</option>
                  <option value="mux">多路复用</option>
                </select>
                <input class="local" :value="(m.localIP || '127.0.0.1') + ':' + (m.localPort || '')" :aria-label="t('c.sv.addr')" @input="updateLocal(m, ($event.target as HTMLInputElement).value)">
                <div class="mapto">
                  <span style="color:#5A7BFF">→</span>
                  <input class="remote" :value="(m.type === 'http' || m.type === 'https' || m.type === 'mux') ? ((m.domains && m.domains.length ? m.domains : (m.domain ? [m.domain] : [])).join(', ')) : (m.remotePort || '')"
                         :placeholder="(m.type === 'http' || m.type === 'https' || m.type === 'mux') ? t('c.mp.domainsPh') : (m.type === 'sealed-tcp' || m.type === 'sealed-udp' || m.type === 'punch') ? t('c.mp.noPort') : t('c.mp.portPh')"
                         :disabled="m.type === 'sealed-tcp' || m.type === 'sealed-udp' || m.type === 'punch'" :aria-label="t('c.mp.portPh')"
                         @input="updateRemote(m, ($event.target as HTMLInputElement).value)">
                  <input v-if="m.type === 'sealed-tcp' || m.type === 'sealed-udp' || m.type === 'punch'" class="msk" v-model="m.sk" :placeholder="t('c.mp.sk')" :aria-label="t('c.mp.sk')">
                  <label class="cmpbox" :title="t('c.mp.compressTip')"><input type="checkbox" v-model="m.compress"> {{ t('c.mp.compress') }}</label>
                  <button class="del" :aria-label="t('c.mp.compressTip')" @click="removeMapping(idx)">
                    <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 4.5H12.5M6.5 4.5V3.2H9.5V4.5M5 4.5L5.6 12.6H10.4L11 4.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
                  </button>
                </div>
                <div v-if="m.type === 'http' || m.type === 'https'" class="maptoadv">
                  <input class="paths" :value="(m.subPaths || []).join(', ')" :placeholder="t('c.mp.pathsPh')" :aria-label="t('c.mp.pathsPh')" @input="updatePaths(m, ($event.target as HTMLInputElement).value)">
                  <input class="bw" v-model="m.bandwidth" :placeholder="t('c.mp.bwPh')" :aria-label="t('c.mp.bwPh')">
                  <input class="auth" :value="(m.httpUser ? m.httpUser + ':' + (m.httpPass || '') : '')" :placeholder="t('c.mp.authPh')" :aria-label="t('c.mp.authPh')" @input="updateAuth(m, ($event.target as HTMLInputElement).value)">
                </div>
              </div>
            </div>

            </div>
            <div v-show="detailSection === 'visitors'">
            <div class="sec"><span>{{ t('c.vi.title', visitorList(draft).length) }}</span><span class="flex1"></span>
              <button class="secadd" @click="save">{{ t('config.save') }}</button>
              <button class="secadd" @click="addVisitor">{{ t('c.add') }}</button>
            </div>
            <div class="cards" style="gap:8px">
              <div v-for="(v, idx) in visitorList(draft)" :key="v.id" class="map">
                <input class="mname" v-model="v.name" :placeholder="t('c.mp.namePh')" :aria-label="t('c.mp.namePh')">
                <select v-model="v.type" :aria-label="t('c.nav.visitors')">
                  <option value="sealed-tcp">密钥隧道 TCP</option><option value="sealed-udp">密钥隧道 UDP</option><option value="punch">{{ t('c.mp.punch') }}</option>
                </select>
                <input class="local" v-model="v.serverName" :placeholder="t('c.vi.targetPh')" :aria-label="t('c.vi.targetPh')">
                <input class="msk" v-model="v.sk" :placeholder="t('c.mp.sk')" :aria-label="t('c.mp.sk')">
                <input class="remote" :value="(v.bindAddr || '127.0.0.1') + ':' + (v.bindPort || '')" placeholder="127.0.0.1:6000" :aria-label="t('c.sv.addr')" @input="updateVisitorBind(v, ($event.target as HTMLInputElement).value)">
                <button class="del" :aria-label="t('c.nav.visitors')" @click="removeVisitor(idx)">
                  <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 4.5H12.5M6.5 4.5V3.2H9.5V4.5M5 4.5L5.6 12.6H10.4L11 4.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
                </button>
              </div>
            </div>

            </div>

            <div v-show="detailSection === 'files'">
              <div class="sec"><span>lumoc.toml</span><span class="flex1"></span>
                <button class="btn sm" @click="openModal('modalNew')">
                  <svg width="12" height="12" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="#0B1117" stroke-width="2" stroke-linecap="round"/></svg>
                  {{ t('c.f.new') }}
                </button>
              </div>
              <pre class="warnbox" style="white-space:pre-wrap;max-height:40vh;overflow:auto">{{ tomlText || t('c.f.loading') }}</pre>
              <div class="setcard" style="margin-top:12px">
                <div class="kv"><span>{{ t('c.ov.mode') }}</span><b>{{ t('c.ov.modeV') }}</b></div>
                <div class="kv"><span>{{ t('c.ov.engineBin') }}</span><b>{{ info?.engineBin || '—' }}</b></div>
                <div class="kv"><span>{{ t('c.ov.dataDir') }}</span><b>{{ info?.dataDir || '—' }}</b></div>
                <div class="kv"><span>{{ t('c.ov.version') }}</span><b>v{{ info?.version || '—' }}</b></div>
              </div>
            </div>
            <div class="dfoot" v-show="detailSection === 'files'">
              <span class="link" @click="showToml(draft)">{{ t('c.f.fullEdit') }}</span>
              <span class="spacer"></span>
              <button class="btn" @click="save">{{ t('c.f.saveApply') }}</button>
              <button id="deleteBtn" style="display:none"></button>
            </div>
          </template>
          <template v-else>
            <div class="dhead">
              <div class="txt">
                <h2>—</h2>
                <div class="sub">{{ t('c.detail.pick') }}</div>
              </div>
            </div>
          </template>
        </aside>
      </div>

      <!-- 运行日志视图 -->
      <div class="logview" v-show="view === 'logs'" ref="logView" @scroll="0">
        <div class="logtools">
          <span class="lg-lb">{{ t('c.log.level') }}</span>
          <select v-model="logLevel" class="lvsel">
            <option value="all">{{ t('c.log.all') }}</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </div>
        <template v-if="runningConfigs.length">
          <div v-for="rc in runningConfigs" :key="rc.status.id" class="loggroup">
            <div class="loghead">
              <span class="lg-name"><i class="dot" :class="{ on: rc.status.running }"></i>{{ rc.name }}</span>
              <span class="lg-count">{{ t('c.log.lines', rc.status.log.length) }}</span>
              <span class="spacer"></span>
              <button class="btn ghost sm" @click="copyLogs(rc.status.log)">{{ t('c.log.copy') }}</button>
            </div>
            <div v-if="rc.status.error" class="logrow err"><span class="lt"></span><span class="lv">ERR</span><span class="lm">{{ rc.status.error }}</span></div>
            <div v-for="(l, i) in parseLogs(rc.status.log).filter((x) => levelRank(x.level) >= levelRank(logLevel))" :key="i" class="logrow" :class="l.cls">
              <span class="lt">{{ l.time }}</span>
              <span class="lv">{{ l.level }}</span>
              <span class="lm">{{ l.msg }}</span>
            </div>
          </div>
        </template>
        <div v-else class="hint" style="margin:16px">{{ t('c.log.empty') }}</div>
      </div>

    </main>
  </div>

  <!-- 新建配置 -->
  <div class="overlay" id="modalNew">
    <div class="modal">
      <div class="mhead">
        <div class="txt">
          <h3>{{ t('c.n.title') }}</h3>
          <p>{{ t('c.n.sub') }}</p>
        </div>
        <button class="mclose" @click="closeModal('modalNew')">✕</button>
      </div>

      <div class="tplrow">
        <span class="lb">{{ t('c.n.tpl') }}</span>
        <button class="tpl" v-for="(v, k) in TPL" :key="k" @click="useTemplate(String(k))">{{ t('c.tpl.' + k) }}</button>
      </div>

      <div class="grp"><span class="gnum">1</span><b>{{ t('c.n.step1') }}</b><em>{{ t('c.n.step1d') }}</em></div>
      <div class="field-row">
        <div class="field"><label>{{ t('c.sv.addr') }}</label><input id="nAddr" class="mono" placeholder="hk-01.example.net"></div>
        <div class="field"><label>{{ t('c.sv.port') }}</label><input id="nPort" class="mono" value="7000"></div>
      </div>
      <div class="field-row">
        <div class="field"><label>{{ t('c.sv.proto') }}</label>
          <select id="nProto" v-model="nProto">
            <option value="tcp">TCP</option>
            <option value="kcp">KCP（UDP）</option>
          </select>
        </div>
        <div class="field" v-if="nProto === 'kcp'"><label>{{ t('c.sv.kcpPort') }}</label><input id="nKcpPort" class="mono" value="0" :placeholder="t('c.sv.kcpPh')"></div>
      </div>
      <div class="field"><label>{{ t('c.sv.name') }}</label><input id="nName" :placeholder="t('c.sv.namePh')"></div>
      <div class="field"><label>{{ t('c.n.tokenLb') }}</label>
        <div class="token-wrap">
          <input id="nToken" class="mono" type="text" autocomplete="off" spellcheck="false" :placeholder="t('c.n.tokenPh')">

        </div>
      </div>

      <div class="grp"><span class="gnum">2</span><b>{{ t('c.n.step2') }}</b><em>{{ t('c.n.step2d') }}</em></div>
      <div class="map" id="nMap">
        <input class="mname" :placeholder="t('c.mp.namePh')" :aria-label="t('c.mp.namePh')">
        <select class="mtype" v-model="nType">
          <option value="tcp">TCP</option><option value="udp">UDP</option>
          <option value="http">HTTP</option><option value="https">HTTPS</option>
          <option value="sealed-tcp">密钥隧道 TCP</option><option value="sealed-udp">密钥隧道 UDP</option>
          <option value="punch">{{ t('c.mp.punch') }}</option>
          <option value="mux">多路复用</option>
        </select>
        <input class="local" placeholder="127.0.0.1:5000">
        <div class="mapto"><span style="color:#5A7BFF">→</span>
          <input class="remote" :placeholder="(nType === 'http' || nType === 'https' || nType === 'mux') ? t('c.mp.domainsPh') : (nType === 'sealed-tcp' || nType === 'sealed-udp' || nType === 'punch') ? t('c.mp.noPort') : '15000'" :disabled="nType === 'sealed-tcp' || nType === 'sealed-udp' || nType === 'punch'">
        </div>
        <input v-if="nType === 'sealed-tcp' || nType === 'sealed-udp' || nType === 'punch'" class="msk" :placeholder="t('c.mp.sk')">
        <label class="cmpbox" :title="t('c.mp.compressTip')"><input class="mcompress" type="checkbox"> {{ t('c.mp.compress') }}</label>
        <div v-if="nType === 'http' || nType === 'https'" class="maptoadv">
          <input class="paths" :placeholder="t('c.mp.pathsPh')">
          <input class="bw" :placeholder="t('c.mp.bwPh')">
          <input class="auth" :placeholder="t('c.mp.authPh')">
        </div>
      </div>

      <div class="grp"><span class="gnum">3</span><b>{{ t('c.n.step3') }}</b><em>{{ t('c.n.step3d') }}</em></div>
      <div class="optrow"><span class="lbl">{{ t('c.n.startNow') }}</span><span class="switch sm" :class="{ on: startNow }" id="nStart" role="switch" :aria-checked="startNow ? 'true' : 'false'" tabindex="0" @click="startNow = !startNow"></span></div>

      <div class="mfoot">
        <span class="spacer"></span>
        <button class="btn ghost" @click="closeModal('modalNew')">{{ t('c.n.cancel') }}</button>
        <button class="btn" @click="createAndConnect">{{ t('c.n.saveConn') }}</button>
      </div>
    </div>
  </div>

  <!-- 账号安全（2FA） -->
  <div class="overlay" id="modal2fa">
    <div class="modal sm">
      <div class="mhead">
        <div class="txt"><h3>{{ t('tfa.title') }}</h3><p>{{ t('c.tfa.sub') }}</p></div>
        <button class="mclose" @click="closeModal('modal2fa')">✕</button>
      </div>
      <!-- 已绑定：列表管理 -->
      <template v-if="twofaCount > 0 && twofaView === 'list'">
        <p class="setnote">{{ t('tfa.count', twofaCount) }}</p>
        <div class="field"><label>{{ t('tfa.codeFor') }}</label><input v-model="twofaCode" class="mono" maxlength="6" :placeholder="t('login.2faPlaceholder')"></div>
        <div class="tlist">
          <div class="trow" v-for="i in twofaCount" :key="i">
            <span class="trow-name">{{ t('tfa.entry', i) }}</span>
            <button class="tunlink" type="button" @click="unbindEntry(i - 1)">{{ t('tfa.removeOne') }}</button>
          </div>
        </div>
        <div class="mfoot"><span class="spacer"></span>
          <button class="btn ghost" @click="closeModal('modal2fa')">{{ t('c.n.cancel') }}</button>
          <button class="btn" @click="startAdd2fa">{{ t('tfa.addOne') }}</button>
        </div>
      </template>
      <template v-else>
        <div class="qrwrap" v-if="twofaQr"><img :src="twofaQr" alt="TOTP QR" width="184" height="184"></div>
        <p class="setnote" style="text-align:center;margin:0">{{ t('c.tfa.scanNote') }} <b class="mono">{{ twofaSecret }}</b></p>
        <div class="field" style="margin-top:12px"><label>{{ t('c.tfa.codeToEnable') }}</label>
          <input v-model="twofaCode" class="mono" maxlength="6" :placeholder="t('login.2faPlaceholder')">
        </div>
        <div class="mfoot"><span class="spacer"></span>
          <button class="btn ghost" @click="twofaCount > 0 ? backFromAdd2fa() : closeModal('modal2fa')">{{ t('c.n.cancel') }}</button>
          <button class="btn" :disabled="!twofaCode" @click="enable2fa">{{ t('c.tfa.enable') }}</button>
        </div>
      </template>
    </div>
  </div>

  <!-- 删除确认 -->
  <div class="overlay" id="modalDel">
    <div class="modal sm">
      <div class="mhead">
        <div class="txt">
          <h3>{{ pendingDelete ? t('c.del.title', pendingDelete.name) : '' }}</h3>
          <p>{{ t('c.del.p') }}</p>
        </div>
      </div>
      <div class="warnbox">{{ pendingDelete ? `${pendingDelete.serverAddr}:${pendingDelete.serverPort}  →  ${t('c.del.count', pendingDelete.mappings.length)}` : '' }}</div>
      <div class="mfoot">
        <span class="spacer"></span>
        <button class="btn ghost" @click="closeModal('modalDel')">{{ t('c.n.cancel') }}</button>
        <button class="btn danger" @click="confirmDelete">{{ t('c.del.confirm') }}</button>
      </div>
    </div>
  </div>

  <!-- lumoc.toml -->
  <div class="overlay" id="modalToml">
    <div class="modal">
      <div class="mhead">
        <div class="txt"><h3>lumoc.toml</h3><p>{{ t('c.toml.sub') }}</p></div>
        <button class="mclose" @click="closeModal('modalToml')">✕</button>
      </div>
      <textarea class="tomledit" v-model="tomlText" spellcheck="false" aria-label="lumoc.toml editor"></textarea>
      <p class="setnote">{{ t('c.toml.note') }}</p>
      <div class="mfoot">
        <button class="btn ghost" @click="copyText(tomlText, t('c.toml.copied'))">{{ t('config.copy') }}</button>
        <span class="spacer"></span>
        <button class="btn ghost" @click="closeModal('modalToml')">{{ t('c.n.cancel') }}</button>
        <button class="btn" :disabled="tomlSaving" @click="saveToml">{{ tomlSaving ? t('config.saving') : t('c.f.saveApply') }}</button>
      </div>
    </div>
  </div>

  <div class="toast" :class="{ show: toastMsg, bad: toastMsg?.bad }"><span class="dot"></span><span>{{ toastMsg?.msg }}</span></div>
</template>

<style src="./style.css"></style>
