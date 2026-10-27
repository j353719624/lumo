/* ============================================================
   LUMO 服务端 API 适配层
   ------------------------------------------------------------
   只改这个文件就能接到你现有的服务端。
   默认按 LUMO 服务端接口获取：
     GET /api/serverinfo
     GET /api/proxy/{tcp|udp|http|https|sealed-tcp|punch|sealed-udp|mux}
     GET /api/proxy/traffic/{name}
     GET /healthz
   如果你的服务端字段不一样，只改下面的 pick()/normalizeProxy()。
   ============================================================ */
window.LUMO_API = (function () {
  const qs = new URLSearchParams(location.search);

  const cfg = {
    // 同源部署留空；跨域时填服务端地址，例如 http://127.0.0.1:7500
    base: qs.get('api') || '',
    // webServer.user / webServer.password（跨域时需要手动带）
    user: qs.get('user') || '',
    pass: qs.get('pass') || '',
    interval: Number(qs.get('interval')) || 2000,
    timeout: 8000,
  };

  const TYPES = ['tcp', 'udp', 'http', 'https', 'sealed-tcp', 'punch', 'sealed-udp', 'mux'];

  function headers() {
    const h = { Accept: 'application/json' };
    if (cfg.user) h.Authorization = 'Basic ' + btoa(`${cfg.user}:${cfg.pass}`);
    return h;
  }

  async function get(path) {
    const url = cfg.base.replace(/\/$/, '') + path;
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), cfg.timeout);
    try {
      const res = await fetch(url, {
        headers: headers(),
        credentials: cfg.base ? 'omit' : 'include', // 同源走浏览器缓存里的 Basic 凭据
        signal: ctrl.signal,
      });
      if (!res.ok) {
        const err = new Error(`HTTP ${res.status}`);
        err.status = res.status;
        throw err;
      }
      return await res.json();
    } finally {
      clearTimeout(timer);
    }
  }

  const num = (v) => (typeof v === 'number' ? v : Number(v) || 0);

  /* 字段可能平铺或包在 conf 里，两种都兼容 */
  function pick(obj, keys, fallback) {
    for (const k of keys) {
      const v = obj?.[k];
      if (v !== undefined && v !== null) return v;
    }
    return fallback;
  }

  function entryOf(c, host) {
    const domains = pick(c, ['customDomains', 'custom_domains'], []);
    if (Array.isArray(domains) && domains.length) return domains[0];
    const sub = pick(c, ['subdomain'], '');
    if (sub) {
      const root = (window.__LUMO_STATE__?.subdomainHost) || '';
      return root ? `${sub}.${root}` : sub;
    }
    const rp = pick(c, ['remotePort', 'remote_port'], '');
    if (rp) return `${host}:${rp}`;
    return '仅密钥访问';
  }

  function normalizeProxy(p, type, host) {
    const c = p.conf || p;
    const status = String(pick(p, ['status'], 'offline')).toLowerCase();
    return {
      name: pick(p, ['name'], '—'),
      type: String(pick(c, ['type'], type) || type).toLowerCase(),
      client: pick(c, ['clientName', 'client_name', 'user'], '—'),
      local: (() => {
        const ip = pick(c, ['localIP', 'local_ip'], '');
        const port = pick(c, ['localPort', 'local_port'], '');
        return ip && port ? `${ip}:${port}` : (port ? `:${port}` : '');
      })(),
      entry: entryOf(c, host),
      trafficIn: num(pick(p, ['today_traffic_in', 'todayTrafficIn'], 0)),
      trafficOut: num(pick(p, ['today_traffic_out', 'todayTrafficOut'], 0)),
      conns: num(pick(p, ['cur_conns', 'curConns'], 0)),
      startAt: pick(p, ['last_start_time', 'lastStartTime'], ''),
      status,
      online: status === 'online',
      waiting: status === 'waiting',
    };
  }

  /* ---------- 对外接口 ---------- */
  async function serverInfo() {
    const d = await get('/api/serverinfo');
    const s = d.conf || d;
    const base = {
      version: pick(s, ['version'], '—'),
      bindPort: pick(s, ['bindPort', 'bind_port'], ''),
      kcpPort: pick(s, ['kcpBindPort', 'kcp_bind_port'], ''),
      quicPort: pick(s, ['quicBindPort', 'quic_bind_port'], ''),
      vhostHttpPort: pick(s, ['vhostHTTPPort', 'vhost_http_port'], ''),
      vhostHttpsPort: pick(s, ['vhostHTTPSPort', 'vhost_https_port'], ''),
      subdomainHost: pick(s, ['subdomainHost', 'subdomain_host'], ''),
      allowPorts: pick(s, ['allowPortsStr', 'allow_ports'], ''),
      maxPoolCount: pick(s, ['maxPoolCount', 'max_pool_count'], ''),
      heartbeatTimeout: pick(s, ['heartbeatTimeout', 'heart_beat_timeout'], ''),
      tlsForce: !!pick(s, ['tlsForce', 'tls_force'], false),
      // 累计流量：注意是累计值，速率由前端差分得到
      totalIn: num(pick(s, ['totalTrafficIn', 'total_traffic_in'], 0)),
      totalOut: num(pick(s, ['totalTrafficOut', 'total_traffic_out'], 0)),
      curConns: num(pick(s, ['curConns', 'cur_conns'], 0)),
      clientCounts: num(pick(s, ['clientCounts', 'client_counts'], 0)),
      proxyTypeCount: pick(s, ['proxyTypeCount', 'proxy_type_count'], {}) || {},
    };
    window.__LUMO_STATE__ = { subdomainHost: base.subdomainHost };
    return base;
  }

  /* 优先单个聚合接口；不存在再按类型逐个取，兼容新旧版本 */
  async function proxies(host) {
    try {
      const d = await get('/api/proxies?page=1&pageSize=500');
      const list = d.proxies || d.items || [];
      if (Array.isArray(list) && list.length) {
        return list.map((p) => normalizeProxy(p, p.type, host));
      }
    } catch (_) { /* 走下面的分类型 */ }

    const results = await Promise.allSettled(
      TYPES.map((t) => get(`/api/proxy/${t}`).then((d) => ({ t, list: d.proxies || [] })))
    );
    const out = [];
    for (const r of results) {
      if (r.status !== 'fulfilled') continue;
      for (const p of r.value.list) out.push(normalizeProxy(p, r.value.t, host));
    }
    return out;
  }

  async function health() {
    try {
      const res = await fetch(cfg.base.replace(/\/$/, '') + '/healthz', {
        headers: headers(),
        credentials: cfg.base ? 'omit' : 'include',
      });
      return res.ok;
    } catch (_) {
      return false;
    }
  }

  /* 可选：若你的服务端有日志接口，返回 {time,level,msg}[] */
  async function logs(level, limit) {
    try {
      const d = await get(`/api/logs?level=${level}&limit=${limit}`);
      const list = d.logs || d.items || [];
      return list.map((l) => ({
        time: pick(l, ['time', 'timestamp', 'ts'], ''),
        level: String(pick(l, ['level'], 'INFO')).toUpperCase(),
        msg: pick(l, ['msg', 'message'], ''),
      }));
    } catch (_) {
      return null; // 没有日志接口 → 上层改用事件流
    }
  }

  return {
    cfg,
    configure: (patch) => Object.assign(cfg, patch),
    serverInfo,
    proxies,
    health,
    logs,
    host: () => location.hostname,
  };
})();
