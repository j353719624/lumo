// Typed API client for the lumoc console.

export interface Mapping {
  id: string
  enabled?: boolean
  type: string
  localIP?: string
  localPort: number
  remotePort?: number
  domain?: string
}

export interface ConnConfig {
  id: string
  name: string
  serverAddr: string
  serverPort: number
  token: string
  protocol?: string
  tls: boolean
  enabled: boolean
  autoReconnect?: boolean
  autoStart?: boolean
  mappings: Mapping[]
}

export interface ConnStatus {
  id: string
  running: boolean
  error?: string
  proxies: { name: string; type: string; status: string; err?: string; in: number; out: number }[]
  log: string[]
}

export interface EngineInfo {
  version: string
  engineBin: string
  dataDir: string
  port: string
}

async function json<T>(res: Response): Promise<T> {
  const d = await res.json()
  if (!res.ok) throw new Error(d.error || `HTTP ${res.status}`)
  return d as T
}

export const api = {
  async list(): Promise<ConnConfig[]> {
    const r = await fetch('/api/configs')
    return json<ConnConfig[]>(r)
  },
  async create(data: Partial<ConnConfig>): Promise<ConnConfig> {
    const r = await fetch('/api/configs', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
    })
    return json<ConnConfig>(r)
  },
  async update(id: string, data: Partial<ConnConfig>): Promise<{ restarted: boolean; config: ConnConfig }> {
    const r = await fetch('/api/configs/' + id, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
    })
    return json<{ restarted: boolean; config: ConnConfig }>(r)
  },
  async remove(id: string): Promise<void> {
    const r = await fetch('/api/configs/' + id, { method: 'DELETE' })
    await json<{ ok: boolean }>(r)
  },
  async start(id: string): Promise<{ ok: boolean }> {
    const r = await fetch(`/api/configs/${id}/start`, { method: 'POST' })
    return json<{ ok: boolean }>(r)
  },
  async stop(id: string): Promise<{ ok: boolean }> {
    const r = await fetch(`/api/configs/${id}/stop`, { method: 'POST' })
    return json<{ ok: boolean }>(r)
  },
  async status(): Promise<ConnStatus[]> {
    const r = await fetch('/api/status')
    return json<ConnStatus[]>(r)
  },
  async info(): Promise<EngineInfo> {
    const r = await fetch('/api/info')
    return json<EngineInfo>(r)
  },
  async toml(id: string): Promise<string> {
    const r = await fetch(`/api/configs/${id}/toml`)
    const d = await json<{ toml: string }>(r)
    return d.toml
  },
}
