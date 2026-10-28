// Typed API client for the lumos console. Mirrors the server contract:
//   GET /api/serverinfo · GET /api/proxies · GET /api/logs · GET /healthz

export interface ServerInfo {
  version: string
  bindPort: number
  kcpPort: number
  quicPort: number
  vhostHttpPort: number
  vhostHttpsPort: number
  subdomainHost: string
  allowPortsStr: string
  maxPoolCount: number
  heartbeatTimeout: number
  tlsForce: boolean
  totalIn: number
  totalOut: number
  totalTrafficIn: number
  totalTrafficOut: number
  curConns: number
  clientCounts: number
  proxyTypeCount: Record<string, number>
}

export interface RawProxy {
  name: string
  type?: string
  status: string
  today_traffic_in: number
  today_traffic_out: number
  cur_conns: number
  last_start_time: string
  conf: {
    type: string
    user?: string
    localIP?: string
    localPort?: number
    remotePort?: number
    customDomains?: string[]
  }
}

export interface ProxyItem {
  name: string
  type: string
  client: string
  local: string
  entry: string
  trafficIn: number
  trafficOut: number
  conns: number
  startAt: string
  status: string
  online: boolean
  waiting: boolean
}

export interface LogEntry {
  time: string
  level: string
  msg: string
}

const qs = new URLSearchParams(location.search)

const cfg = {
  base: qs.get('api') || '',
  user: qs.get('user') || '',
  pass: qs.get('pass') || '',
  interval: Number(qs.get('interval')) || 2000,
  timeout: 8000,
}

function headers(): Record<string, string> {
  const h: Record<string, string> = { Accept: 'application/json' }
  if (cfg.user) h.Authorization = 'Basic ' + btoa(`${cfg.user}:${cfg.pass}`)
  return h
}

async function get<T>(path: string): Promise<T> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), cfg.timeout)
  try {
    const res = await fetch(cfg.base.replace(/\/$/, '') + path, {
      headers: headers(),
      credentials: 'include',
      signal: ctrl.signal,
    })
    if (!res.ok) {
      const err = new Error(`HTTP ${res.status}`) as Error & { status?: number }
      err.status = res.status
      throw err
    }
    return (await res.json()) as T
  } finally {
    clearTimeout(timer)
  }
}

function pick(obj: any, keys: string[], fallback: any): any {
  for (const k of keys) {
    const v = obj?.[k]
    if (v !== undefined && v !== null) return v
  }
  return fallback
}

const num = (v: any) => (typeof v === 'number' ? v : Number(v) || 0);

(function probeSubdomainHost() {
  get<any>('/api/serverinfo').then((d) => {
    ;(window as any).__LUMO_STATE__ = { subdomainHost: d.subdomainHost || '' }
  }).catch(() => {})
})()

export function normalizeProxy(p: RawProxy, type: string, host: string): ProxyItem {
  const c = p.conf || (p as any)
  const domains = pick(c, ['customDomains', 'custom_domains'], []) as string[]
  let entry = ''
  if (Array.isArray(domains) && domains.length) {
    entry = domains[0]
  } else {
    const sub = pick(c, ['subdomain'], '') as string
    const root = (window as any).__LUMO_STATE__?.subdomainHost || ''
    if (sub) entry = root ? `${sub}.${root}` : sub
    else {
      const rp = pick(c, ['remotePort', 'remote_port'], '') as number
      entry = rp ? `${host}:${rp}` : '仅密钥访问'
    }
  }
  const ip = pick(c, ['localIP', 'local_ip'], '') as string
  const port = pick(c, ['localPort', 'local_port'], '') as number
  const status = String(pick(p, ['status'], 'offline')).toLowerCase()
  return {
    name: pick(p, ['name'], '—'),
    type: String(pick(c, ['type'], type) || type).toLowerCase(),
    client: pick(c, ['clientName', 'client_name', 'user'], '—'),
    local: ip && port ? `${ip}:${port}` : port ? `:${port}` : '',
    entry,
    trafficIn: num(pick(p, ['today_traffic_in', 'todayTrafficIn'], 0)),
    trafficOut: num(pick(p, ['today_traffic_out', 'todayTrafficOut'], 0)),
    conns: num(pick(p, ['cur_conns', 'curConns'], 0)),
    startAt: pick(p, ['last_start_time', 'lastStartTime'], ''),
    status,
    online: status === 'online',
    waiting: status === 'waiting',
  }
}

export async function serverInfo(): Promise<ServerInfo> {
  const d = await get<any>('/api/serverinfo')
  const s = d.conf || d
  return {
    version: pick(s, ['version'], '—'),
    bindPort: pick(s, ['bindPort', 'bind_port'], 0),
    kcpPort: pick(s, ['kcpBindPort', 'kcp_bind_port'], 0),
    quicPort: pick(s, ['quicBindPort', 'quic_bind_port'], 0),
    vhostHttpPort: pick(s, ['vhostHTTPPort', 'vhost_http_port'], 0),
    vhostHttpsPort: pick(s, ['vhostHTTPSPort', 'vhost_https_port'], 0),
    subdomainHost: pick(s, ['subdomainHost', 'subdomain_host'], ''),
    allowPortsStr: pick(s, ['allowPortsStr', 'allow_ports'], ''),
    maxPoolCount: pick(s, ['maxPoolCount', 'max_pool_count'], 0),
    heartbeatTimeout: pick(s, ['heartbeatTimeout', 'heart_beat_timeout'], 0),
    tlsForce: !!pick(s, ['tlsForce', 'tls_force'], false),
    totalIn: num(pick(s, ['totalTrafficIn', 'total_traffic_in'], 0)),
    totalOut: num(pick(s, ['totalTrafficOut', 'total_traffic_out'], 0)),
    curConns: num(pick(s, ['curConns', 'cur_conns'], 0)),
    clientCounts: num(pick(s, ['clientCounts', 'client_counts'], 0)),
    proxyTypeCount: pick(s, ['proxyTypeCount', 'proxy_type_count'], {}) || {},
  } as ServerInfo
}

export async function proxies(host: string): Promise<ProxyItem[]> {
  try {
    const d = await get<any>('/api/proxies')
    const list = d.proxies || d.items || []
    if (Array.isArray(list) && list.length) {
      return list.map((p: RawProxy) => normalizeProxy(p, p.type || "tcp", host))
    }
  } catch { /* fall through to per-type queries */ }

  const TYPES = ['tcp', 'udp', 'http', 'https', 'sealed-tcp', 'punch', 'sealed-udp', 'mux']
  const results = await Promise.allSettled(
    TYPES.map((t) => get<any>(`/api/proxy/${t}`).then((d) => ({ t, list: d.proxies || [] }))),
  )
  const out: ProxyItem[] = []
  for (const r of results) {
    if (r.status !== 'fulfilled') continue
    for (const p of r.value.list) out.push(normalizeProxy(p, r.value.t, host))
  }
  return out
}

export async function logs(level: string, limit: number): Promise<LogEntry[]> {
  try {
    const d = await get<any>(`/api/logs?level=${level}&limit=${limit}`)
    const list = d.logs || d.items || []
    return list.map((l: any) => ({
      time: pick(l, ['time', 'timestamp', 'ts'], ''),
      level: String(pick(l, ['level'], 'INFO')).toUpperCase(),
      msg: pick(l, ['msg', 'message'], ''),
    }))
  } catch {
    return []
  }
}

export async function health(): Promise<boolean> {
  try {
    const res = await fetch(cfg.base.replace(/\/$/, '') + '/healthz', { credentials: 'include' })
    return res.ok
  } catch {
    return false
  }
}

export const apiCfg = cfg
export const host = () => location.hostname
