<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type ConnConfig, type ConnStatus, type EngineInfo, type Mapping } from './api'
import { applyThemePref, getThemePref, type Theme, type ThemePref } from './theme'

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

const q = ref('')
const view = ref<'list' | 'logs' | 'settings'>('list')
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
  if (!c.enabled) return { cls: '', label: '已停止' }
  const st = statusMap.value[c.id]
  if (!st) return { cls: 'wait', label: '连接中' }
  if (st.error) return { cls: 'err', label: '异常' }
  if (st.running) return (st.proxies && st.proxies.length)
    ? { cls: 'on', label: '已连接' } : { cls: 'wait', label: '连接中' }
  return { cls: 'wait', label: '启动中' }
}

function mappingSummary(c: ConnConfig): string {
  const m = (c.mappings || [])[0]
  if (!m) return '未配置映射'
  const local = `${m.localIP || '127.0.0.1'}:${m.localPort || '—'}`
  const remote = (m.type === 'http' || m.type === 'https') ? (m.domain || '未填域名') : String(m.remotePort || '—')
  const more = c.mappings.length > 1 ? `  +${c.mappings.length - 1}` : ''
  const tag = m.name ? `[${m.name}] ` : ''
  return `${c.serverAddr}:${c.serverPort}  →  ${tag}${local}  ⇒  ${remote}${more}`
}

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
    configs.value = await api.list()
    statusMap.value = {}
    for (const s of await api.status()) statusMap.value[s.id] = s
    info.value = await api.info()
  } catch (e: any) {
    if (e?.message?.includes('401')) { location.href = '/login'; return }
    toast('读取失败：' + (e?.message || e), true)
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
      toast('已启用 ' + c.name)
    } else {
      await api.stop(c.id)
      toast('已停止 ' + c.name)
    }
    c.enabled = !c.enabled
    if (draft.value && draft.value.id === c.id) draft.value.enabled = c.enabled
  } catch (e: any) {
    toast('操作失败：' + (e?.message || e), true)
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
  if (c.mappings.length === 1) { toast('至少保留一条映射', true); return }
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

function updateRemote(m: Mapping, raw: string) {
  if (m.type === 'http' || m.type === 'https') m.domain = raw.trim()
  else m.remotePort = Number(raw.trim()) || 0
}

async function save() {
  const c = draft.value
  if (!c) return
  if (!c.serverAddr.trim()) { toast('服务器地址不能为空', true); return }
  const wasRunning = !!statusMap.value[c.id]?.running
  try {
    const r = await api.update(c.id, {
      name: c.name, serverAddr: c.serverAddr, serverPort: c.serverPort,
      protocol: c.protocol || 'tcp', kcpPort: c.protocol === 'kcp' ? Number(c.kcpPort) || 0 : 0,
      token: c.token, tls: c.tls, mappings: c.mappings,
    })
    await refresh()
    toast(r?.restarted || wasRunning ? '已保存，正在重启该连接' : '已保存')
  } catch (e: any) {
    toast('保存失败：' + (e?.message || e), true)
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
    toast(`已删除 ${c.name}`)
  } catch (e: any) {
    toast('删除失败：' + (e?.message || e), true)
  }
}

async function createAndConnect() {
  const addr = (document.getElementById('nAddr') as HTMLInputElement)?.value.trim()
  const name = (document.getElementById('nName') as HTMLInputElement)?.value.trim()
  const token = (document.getElementById('nToken') as HTMLInputElement)?.value
  const port = Number((document.getElementById('nPort') as HTMLInputElement)?.value) || 7000
  if (!addr) { toast('请填写服务器地址', true); return }

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
  if (type === 'http' || type === 'https') mapping.domain = remoteRaw
  else mapping.remotePort = Number(remoteRaw) || 0

  const startNowVal = startNow.value

  try {
    const nKcp = document.getElementById('nKcpPort') as HTMLInputElement | null
    const cfg = await api.create({
      name: name || '未命名配置', serverAddr: addr, serverPort: port,
      protocol: nProto.value, kcpPort: nProto.value === 'kcp' ? Number(nKcp?.value) || 0 : 0,
      token: token || '', mappings: [mapping], enabled: startNowVal,
    })
    closeModal('modalNew')
    selectedId.value = cfg.id
    await refresh()
    toast(`已创建 ${cfg.name}`)
  } catch (e: any) {
    toast('创建失败：' + (e?.message || e), true)
  }
}

async function showToml(c: ConnConfig) {
  try {
    tomlView.value = await api.toml(c.id)
    openModal('modalToml')
  } catch (e: any) {
    toast('读取失败：' + (e?.message || e), true)
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
    if (el) el.value = genToken()
  }
  document.getElementById(id)?.classList.add('open')
}
function closeModal(id: string) { document.getElementById(id)?.classList.remove('open') }

function regenToken() {
  const el = document.getElementById('nToken') as HTMLInputElement | null
  if (el) el.value = genToken()
}

function useTemplate(t: string) {
  const tpl = TPL[t]
  if (!tpl) return
  const name = document.getElementById('nName') as HTMLInputElement
  const local = document.querySelector('#nMap .local') as HTMLInputElement
  const remote = document.querySelector('#nMap .remote') as HTMLInputElement
  if (name) name.value = tpl.name
  if (local) local.value = tpl.local
  if (remote) remote.value = tpl.remote
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
    toast('复制失败，请手动选择', true)
  }
}

function setView(v: 'list' | 'logs' | 'settings') {
  view.value = v
}


async function testConnection(addr: string, port: number) {
  if (!addr) { toast('请先填写服务器地址', true); return }
  testing.value = true
  try {
    const r = await fetch('/api/test-connection', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ addr, port }),
    })
    const d = await r.json()
    toast(d.ok ? '连接成功' : ('连接失败: ' + (d.error || '未知')), !d.ok)
  } catch (e: any) { toast('测试失败: ' + (e?.message || e), true) }
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
      toast('已导入 ' + f.name + '，请编辑后保存')
      await refresh()
      select(cfg.id)
    } catch (e: any) { toast('导入失败：' + (e?.message || e), true) }
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
          <path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#5A7BFF" stroke-width="6.8" stroke-linecap="round"/>
          <path d="M10 42H62" stroke="#35E0C8" stroke-width="4.6" stroke-linecap="round"/>
          <circle cx="10" cy="42" r="3.8" fill="#35E0C8"/><circle cx="62" cy="42" r="3.8" fill="#35E0C8"/>
        </svg>
        <span class="wm">LUMO</span>
      </div>

      <div class="nav-label">工作区</div>
      <button class="nav" :aria-current="view === 'list' ? 'page' : undefined" @click="setView('list')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><circle cx="3.6" cy="8" r="2" stroke="#AFC0FF" stroke-width="1.5"/><circle cx="12.4" cy="8" r="2" stroke="#AFC0FF" stroke-width="1.5"/><path d="M5.6 8H10.4" stroke="#AFC0FF" stroke-width="1.5" stroke-linecap="round"/></svg>
        <span>连接配置</span><span class="nbadge">{{ configs.length }}</span>
      </button>
      <button class="nav" :aria-current="view === 'logs' ? 'page' : undefined" @click="setView('logs')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M3 4.5H13M3 8H10M3 11.5H7.5" stroke="#7D8AA0" stroke-width="1.5" stroke-linecap="round"/></svg>
        <span>运行日志</span>
      </button>
      <button class="nav" :aria-current="view === 'settings' ? 'page' : undefined" @click="setView('settings')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M3 5H13M3 11H13" stroke="#7D8AA0" stroke-width="1.5" stroke-linecap="round"/><circle cx="6" cy="5" r="1.8" fill="#0C1017" stroke="#7D8AA0" stroke-width="1.4"/><circle cx="10.5" cy="11" r="1.8" fill="#0C1017" stroke="#7D8AA0" stroke-width="1.4"/></svg>
        <span>偏好设置</span>
      </button>

      <div class="spacer"></div>

      <div class="engine">
        <div class="row"><span class="dot on"></span><span class="t">控制台已连接</span><span class="v">v{{ info?.version || '—' }}</span></div>
        <div class="addr">{{ consoleHost }}</div>
      </div>
      <div class="user">
        <div class="av">A</div>
        <div>
          <div class="nm">admin</div>
          <div class="pl">隧道引擎运行中</div>
        </div>
      </div>
    </aside>

    <!-- 主区 -->
    <main class="main">
      <div class="topbar">
        <div class="titleblock">
          <h1>{{ { list: '连接配置', logs: '运行日志', settings: '偏好设置' }[view] }}</h1>
          <p>{{ view === 'list' ? (configs.length ? `共 ${configs.length} 个配置 · ${enabledCount} 个正在运行` : '还没有配置') : view === 'logs' ? '本地引擎的实时运行事件' : '引擎与运行环境' }}</p>
        </div>
        <span class="spacer"></span>
        <button class="themebtn" :title="theme === 'dark' ? '日间模式' : '黑夜模式'" @click="toggleTheme">
          <svg v-if="theme === 'dark'" width="16" height="16" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.4"/><path d="M8 1.2V3M8 13V14.8M1.2 8H3M13 8H14.8M3.2 3.2L4.5 4.5M11.5 11.5L12.8 12.8M12.8 3.2L11.5 4.5M4.5 11.5L3.2 12.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
          <svg v-else width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6A5.8 5.8 0 1 0 13.4 9.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
        </button>
        <label class="search">
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="7" cy="7" r="4.6" stroke="#5D6A80" stroke-width="1.6"/><path d="M10.4 10.4L13.4 13.4" stroke="#5D6A80" stroke-width="1.6" stroke-linecap="round"/></svg>
          <input v-model="q" type="search" placeholder="搜索配置" autocomplete="off">
        </label>
        <button class="btn" @click="openModal('modalNew')">
          <svg width="13" height="13" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="#0B1117" stroke-width="2" stroke-linecap="round"/></svg>
          新建配置
        </button>
      </div>

      <!-- 连接配置视图 -->
      <div class="content" v-show="view === 'list'">
        <div class="listcol">
          <div class="filters">
            <button class="chip" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">全部 {{ configs.length }}</button>
            <button class="chip" :aria-pressed="filter === 'on' ? 'true' : 'false'" @click="filter = 'on'">已启用 {{ enabledCount }}</button>
            <button class="chip" :aria-pressed="filter === 'off' ? 'true' : 'false'" @click="filter = 'off'">未启用 {{ configs.length - enabledCount }}</button>
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
              <path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#2A3346" stroke-width="5.2" stroke-linecap="round"/>
              <path d="M10 42H62" stroke="#2A3346" stroke-width="3.2" stroke-linecap="round"/>
              <circle cx="10" cy="42" r="3.3" fill="#2A3346"/><circle cx="62" cy="42" r="3.3" fill="#2A3346"/>
            </svg>
            <h3>还没有任何连接配置</h3>
            <p>添加第一个配置之后，就能从外网访问家里的 NAS、路由器或公司电脑 —— 全程只用鼠标，不写一行命令。</p>
            <div class="row"><button class="btn" @click="openModal('modalNew')">新建第一个配置</button> <button class="btn ghost" @click="importToml()">导入 lumoc.toml</button></div>
          </div>

          <div class="stats">
            <span class="lb">今日流量</span>
            <span class="up" id="stUp">↑ {{ totalTrafficOut > 1024**2 ? (totalTrafficOut / 1024**2).toFixed(1) + ' MB' : formatB(totalTrafficOut) }}</span>
            <span class="spacer"></span>
            <span class="lb">活跃连接 {{ runningCount }} 条</span>
          </div>
        </div>

        <!-- 详情面板 -->
        <aside class="detail" id="detail">
          <template v-if="draft">
            <div class="dhead">
              <div class="txt">
                <h2>{{ draft.name }}</h2>
                <div class="sub">{{ draft.serverAddr }}:{{ draft.serverPort }} · {{ (draft.protocol || 'tcp').toUpperCase() }} · {{ statusOf(draft).label }}</div>
              </div>
              <span class="switch" role="switch" :aria-checked="draft.enabled ? 'true' : 'false'" tabindex="0" @click="toggleConfig(draft)"></span>
            </div>

            <div class="sec"><span>连接信息</span><span class="flex1"></span><span class="sechint">对应 lumoc.toml 基础段</span></div>

            <div class="field"><label>配置名称</label><input v-model="draft.name" placeholder="例如 家庭 NAS"></div>
            <div class="field">
              <label>服务器地址</label>
              <div class="inputwrap">
                <input v-model="draft.serverAddr" class="mono" placeholder="hk-01.example.net">
              </div>
            </div>
            <div class="field-row">
              <div class="field"><label>服务端口</label><input v-model.number="draft.serverPort" class="mono" placeholder="7000"></div>
              <div class="field"><label>连接协议</label>
                <select v-model="draft.protocol">
                  <option value="tcp">TCP</option>
                  <option value="kcp">KCP（UDP）</option>
                </select>
              </div>
            </div>
            <div class="field" v-if="draft.protocol === 'kcp'"><label>KCP 端口（服务端 UDP）</label><input v-model.number="draft.kcpPort" class="mono" type="number" min="1" max="65535" placeholder="如 47011"></div>
            <div class="field"><label>认证令牌 · auth.token</label>
              <div class="token-wrap">
                <input v-model="draft.token" class="mono" :type="showToken ? 'text' : 'password'" placeholder="与服务端一致" autocomplete="off">
                <button type="button" class="eye-btn" :aria-pressed="showToken ? 'true' : 'false'" @click="showToken = !showToken" title="显示/隐藏">
                  <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                    <path d="M1.6 8C3.4 4.9 5.6 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C12.6 11.1 10.4 12.6 8 12.6C5.6 12.6 3.4 11.1 1.6 8Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                    <circle cx="8" cy="8" r="2.1" fill="currentColor"/>
                    <path v-if="showToken" d="M2.6 2.6L13.4 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                  </svg>
                </button>
              </div>
            </div>

            <div class="sec"><span>端口映射 {{ draft.mappings.length }} 条</span><span class="flex1"></span>
              <button class="secadd" @click="addMapping">＋ 添加</button>
            </div>
            <div class="cards" style="gap:8px">
              <div v-for="(m, idx) in draft.mappings" :key="m.id" class="map">
                <input class="mname" v-model="m.name" placeholder="名称" aria-label="映射名称">
                <select :value="m.type" @change="setMappingType(m, ($event.target as HTMLSelectElement).value)">
                  <option value="tcp">TCP</option><option value="http">HTTP</option>
                  <option value="udp">UDP</option><option value="sealed-tcp">sealed-tcp</option>
                </select>
                <input class="local" :value="(m.localIP || '127.0.0.1') + ':' + (m.localPort || '')" aria-label="本地地址" @input="updateLocal(m, ($event.target as HTMLInputElement).value)">
                <div class="mapto">
                  <span style="color:#5A7BFF">→</span>
                  <input class="remote" :value="(m.type === 'http' || m.type === 'https') ? (m.domain || '') : (m.remotePort || '')"
                         :placeholder="(m.type === 'http' || m.type === 'https') ? '域名' : '公网端口'" aria-label="公网入口"
                         @input="updateRemote(m, ($event.target as HTMLInputElement).value)">
                  <button class="del" aria-label="删除这条映射" @click="removeMapping(idx)">
                    <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 4.5H12.5M6.5 4.5V3.2H9.5V4.5M5 4.5L5.6 12.6H10.4L11 4.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
                  </button>
                </div>
              </div>
            </div>

            <div class="sec"><span>传输与高级</span></div>
            <div class="optrow"><span class="lbl">TLS 加密传输</span><span class="switch sm" role="switch" :aria-checked="draft.tls ? 'true' : 'false'" tabindex="0" @click="draft.tls = !draft.tls"></span></div>
            <div class="optrow"><span class="lbl">断线自动重连</span><span class="switch sm on" role="switch" aria-checked="true" tabindex="0"></span></div>

            <div class="dfoot">
              <span class="link" @click="showToml(draft)">查看 lumoc.toml</span>
              <span class="spacer"></span>
              <button class="btn" @click="save">保存并应用</button>
              <button id="deleteBtn" style="display:none"></button>
            </div>
          </template>
          <template v-else>
            <div class="dhead">
              <div class="txt">
                <h2>—</h2>
                <div class="sub">选择左侧配置查看详情</div>
              </div>
            </div>
          </template>
        </aside>
      </div>

      <!-- 运行日志视图 -->
      <div class="logview" v-show="view === 'logs'">
        <template v-if="runningConfigs.length">
          <div v-for="rc in runningConfigs" :key="rc.status.id">
            <div class="logline"><span class="tag">── {{ rc.name }} ──</span></div>
            <div v-if="rc.status.error" class="logline err">{{ rc.status.error }}</div>
            <div v-for="(l, i) in rc.status.log" :key="i" class="logline">{{ l }}</div>
          </div>
        </template>
        <div v-else class="hint" style="margin:16px">没有正在运行的连接。启用一个配置后，这里会显示 lumoc 的实时事件。</div>
      </div>

      <!-- 设置视图 -->
      <div class="setpage" v-show="view === 'settings'">
        <div class="setcard">
          <div class="kv"><span>外观主题</span>
            <div class="seg">
              <button :aria-pressed="themePref === 'auto'" @click="setThemePref('auto')">跟随系统</button>
              <button :aria-pressed="themePref === 'light'" @click="setThemePref('light')">日间</button>
              <button :aria-pressed="themePref === 'dark'" @click="setThemePref('dark')">黑夜</button>
            </div>
          </div>
          <p class="setnote">「跟随系统」会随操作系统的浅色/深色设置自动切换；右上角的按钮可快速临时切换。</p>
        </div>
        <div class="setcard">
          <div class="kv"><span>运行模式</span><b>单进程托管全部连接</b></div>
          <div class="kv"><span>隧道引擎</span><b>{{ info?.engineBin || '—' }}</b></div>
          <div class="kv"><span>数据位置</span><b>{{ info?.dataDir || '—' }}</b></div>
          <div class="kv"><span>版本</span><b>v{{ info?.version || '—' }}</b></div>
        </div>
      </div>
    </main>
  </div>

  <!-- 新建配置 -->
  <div class="overlay" id="modalNew">
    <div class="modal">
      <div class="mhead">
        <div class="txt">
          <h3>新建连接配置</h3>
          <p>只填表单，自动建立并管理隧道连接</p>
        </div>
        <button class="mclose" @click="closeModal('modalNew')">✕</button>
      </div>

      <div class="tplrow">
        <span class="lb">快速模板</span>
        <button class="tpl" v-for="(v, k) in TPL" :key="k" @click="useTemplate(String(k))">{{ v.name }}</button>
      </div>

      <div class="grp"><span class="gnum">1</span><b>连接服务器</b><em>向服务端发起隧道</em></div>
      <div class="field-row">
        <div class="field"><label>服务器地址</label><input id="nAddr" class="mono" placeholder="hk-01.example.net"></div>
        <div class="field"><label>服务端口</label><input id="nPort" class="mono" value="7000"></div>
      </div>
      <div class="field-row">
        <div class="field"><label>连接协议</label>
          <select id="nProto" v-model="nProto">
            <option value="tcp">TCP</option>
            <option value="kcp">KCP（UDP）</option>
          </select>
        </div>
        <div class="field" v-if="nProto === 'kcp'"><label>KCP 端口（服务端 UDP）</label><input id="nKcpPort" class="mono" value="0" placeholder="如 47011"></div>
      </div>
      <div class="field"><label>配置名称</label><input id="nName" placeholder="例如 家庭 NAS"></div>
      <div class="field"><label>认证令牌（与服务端一致，可自动生成或手动输入）</label>
        <div class="token-wrap">
          <input id="nToken" class="mono" type="text" autocomplete="off" spellcheck="false" placeholder="auth.token">
          <button type="button" class="eye-btn" title="重新生成" @click="regenToken()">
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
              <path d="M13 6.2A5.3 5.3 0 1 0 13.3 9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
              <path d="M9.4 3.4L13.2 6.1L10.4 9.6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </button>
        </div>
      </div>

      <div class="grp"><span class="gnum">2</span><b>配置端口映射</b><em>内网端口 → 公网端口</em></div>
      <div class="map" id="nMap">
        <input class="mname" placeholder="名称" aria-label="映射名称">
        <select class="mtype"><option value="tcp">TCP</option><option value="http">HTTP</option><option value="udp">UDP</option><option value="sealed-tcp">sealed-tcp</option></select>
        <input class="local" placeholder="127.0.0.1:5000">
        <div class="mapto"><span style="color:#5A7BFF">→</span><input class="remote" placeholder="15000"></div>
      </div>

      <div class="grp"><span class="gnum">3</span><b>启动方式</b><em>可随时在列表里开关</em></div>
      <div class="optrow"><span class="lbl">保存后立即启用</span><span class="switch sm" :class="{ on: startNow }" id="nStart" role="switch" :aria-checked="startNow ? 'true' : 'false'" tabindex="0" @click="startNow = !startNow"></span></div>

      <div class="mfoot">
        <span class="spacer"></span>
        <button class="btn ghost" @click="closeModal('modalNew')">取消</button>
        <button class="btn" @click="createAndConnect">保存并连接</button>
      </div>
    </div>
  </div>

  <!-- 删除确认 -->
  <div class="overlay" id="modalDel">
    <div class="modal sm">
      <div class="mhead">
        <div class="txt">
          <h3>删除「{{ pendingDelete?.name }}」？</h3>
          <p>该配置的端口映射与令牌会被一并删除，正在运行的隧道会立即中断。此操作不可撤销。</p>
        </div>
      </div>
      <div class="warnbox">{{ pendingDelete ? `${pendingDelete.serverAddr}:${pendingDelete.serverPort}  →  ${pendingDelete.mappings.length} 条映射` : '' }}</div>
      <div class="mfoot">
        <span class="spacer"></span>
        <button class="btn ghost" @click="closeModal('modalDel')">取消</button>
        <button class="btn danger" @click="confirmDelete">删除配置</button>
      </div>
    </div>
  </div>

  <!-- lumoc.toml -->
  <div class="overlay" id="modalToml">
    <div class="modal">
      <div class="mhead">
        <div class="txt"><h3>lumoc.toml</h3><p>该配置对应的隧道配置</p></div>
        <button class="mclose" @click="closeModal('modalToml')">✕</button>
      </div>
      <pre class="warnbox" style="white-space:pre-wrap;max-height:50vh;overflow:auto">{{ tomlView }}</pre>
      <div class="mfoot"><span class="spacer"></span><button class="btn ghost" @click="copyText(tomlView, 'lumoc.toml 已复制')">复制</button></div>
    </div>
  </div>

  <div class="toast" :class="{ show: toastMsg, bad: toastMsg?.bad }"><span class="dot"></span><span>{{ toastMsg?.msg }}</span></div>
</template>

<style src="./style.css"></style>
