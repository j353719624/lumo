/* ============================================================
   LUMO · lumos 控制台 逻辑
   数据来源见 assets/api.js；连不上服务端时自动进入演示数据
   ============================================================ */
(function () {
  'use strict';

  const API = window.LUMO_API;
  const $ = (id) => document.getElementById(id);
  const el = (tag, cls, html) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  };

  const state = {
    info: null,
    proxies: [],
    clients: [],
    events: [],
    samplesUp: [],
    samplesDown: [],
    prevTotal: null,
    startedAt: Date.now(),
    connected: false,
    demo: false,
    filter: { type: 'all', q: '', onlineOnly: false, page: 1 },
    logLevel: 'ALL',
    logLive: true,
  };

  const PAGE_SIZE = 6;
  const MAX_EVENTS = 200;
  const MAX_SAMPLES = 60;

  /* ---------------- 格式化 ---------------- */
  const num = (v) => Number(v) || 0;

  function bytes(n) {
    n = num(n);
    if (n >= 1024 ** 3) return (n / 1024 ** 3).toFixed(1) + ' GB';
    if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(0) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
    return n + ' B';
  }
  function rate(n) {
    n = num(n);
    if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(1) + ' MB/s';
    if (n >= 1024) return (n / 1024).toFixed(0) + ' KB/s';
    return n.toFixed(0) + ' B/s';
  }
  function hms(ms) {
    const s = Math.floor(ms / 1000);
    const h = String(Math.floor(s / 3600)).padStart(2, '0');
    const m = String(Math.floor((s % 3600) / 60)).padStart(2, '0');
    const ss = String(s % 60).padStart(2, '0');
    return `${h}:${m}:${ss}`;
  }
  function clock(d = new Date()) {
    return d.toTimeString().slice(0, 8);
  }

  /* ---------------- 演示数据（连不上真实服务端时） ---------------- */
  const DEMO_TYPES = {
    tcp: ['hk-nas-ssh', 'office-erp', 'dev-api', 'db-mysql', 'git-serve', 'redis-cache',
      'mqtt-broker', 'jenkins-ci', 'gitea', 'portainer', 'vnc-office', 'ssh-lab', 'es-node', 'k8s-api'],
    http: ['hk-nas-web', 'nas-panel', 'blog-site', 'docs-site', 'grafana', 'kibana', 'minio-console', 'jupyter'],
    udp: ['game-udp', 'dns-local', 'sip-voice', 'wireguard'],
    sealed-tcp: ['lab-ocr', 'private-db'],
  };
  const DEMO_CLIENTS = ['home-nas', 'office-pc', 'dev-mac', 'home-pc', 'lab-01', 'gpu-node'];

  function buildDemo() {
    const proxies = [];
    let port = 15000;
    Object.entries(DEMO_TYPES).forEach(([type, names]) => {
      names.forEach((name, i) => {
        const online = !(name === 'dev-api' || name === 'jupyter');
        proxies.push({
          name,
          type,
          client: DEMO_CLIENTS[(i + port) % DEMO_CLIENTS.length],
          local: `127.0.0.1:${5000 + i * 7}`,
          entry: type === 'http'
            ? `${name.replace(/^hk-/, '')}.lumo.net`
            : type === 'sealed-tcp' ? '仅密钥访问' : `hk-01.lumo.net:${port++}`,
          trafficIn: Math.round(Math.random() * 9e8),
          trafficOut: Math.round(Math.random() * 3e9),
          conns: online ? Math.ceil(Math.random() * 12) : 0,
          status: online ? 'online' : 'offline',
          online,
          waiting: false,
        });
      });
    });
    return {
      version: 'v0.61.1',
      bindPort: 7000, kcpPort: 7001, quicPort: 7002,
      vhostHttpPort: 80, vhostHttpsPort: 443,
      subdomainHost: 'lumo.net', allowPorts: '15000-15100',
      maxPoolCount: 5, heartbeatTimeout: 90, tlsForce: true,
      totalIn: 8.6e9, totalOut: 34.2e9,
      curConns: 143, clientCounts: 12,
      proxyTypeCount: { tcp: 14, http: 8, udp: 4, sealed-tcp: 2 },
    };
  }
  const DEMO_INFO = buildDemo();

  /* ---------------- 事件流（日志接口不可用时由差分生成） ---------------- */
  function pushEvent(level, msg) {
    state.events.push({ time: clock(), level, msg });
    if (state.events.length > MAX_EVENTS) state.events.shift();
    if (state.logLive) renderLogs();
  }

  function diffEvents(nextProxies, nextClients) {
    if (!state.info) return;
    const prev = new Map(state.proxies.map((p) => [p.name, p]));
    const seen = new Set();

    for (const p of nextProxies) {
      seen.add(p.name);
      const old = prev.get(p.name);
      if (!old) {
        pushEvent('INFO', `代理上线 ${p.name} · ${p.type} · ${p.entry}`);
      } else if (old.online !== p.online) {
        pushEvent(p.online ? 'INFO' : 'WARN',
          p.online ? `代理恢复 ${p.name}` : `代理离线 ${p.name}（心跳超时或客户端退出）`);
      }
    }
    for (const [name, old] of prev) {
      if (!seen.has(name)) pushEvent('WARN', `代理移除 ${name} · ${old.type}`);
    }
    const prevClients = new Set(state.clients);
    for (const c of nextClients) if (!prevClients.has(c)) pushEvent('INFO', `客户端接入 ${c} · ${API.host()}`);
    for (const c of prevClients) if (!nextClients.includes(c)) pushEvent('WARN', `客户端断开 ${c}`);
  }

  /* ---------------- 渲染：顶栏与状态带 ---------------- */
  function renderStatus() {
    const i = state.info;
    if (!i) return;
    $('ver').textContent = i.version;
    $('engine').className = 'pill' + (state.connected ? '' : ' wait');
    $('engineText').textContent = state.connected ? '服务运行中' : '演示数据';
    $('statusText').textContent = state.connected ? '服务运行正常' : '演示数据 · 未连服务端';
    $('sdot').className = 'sdot' + (state.connected ? '' : ' bad');
    $('healthz').textContent = state.connected ? 'healthz 200' : 'healthz —';
    $('uptime').textContent = '已监控 ' + hms(Date.now() - state.startedAt);
    $('pid').textContent = `bindPort ${i.bindPort} · vhost ${i.vhostHttpPort || '—'}/${i.vhostHttpsPort || '—'}`;

    const online = state.proxies.filter((p) => p.online).length;
    const clientSet = new Set(state.proxies.map((p) => p.client).filter((c) => c && c !== '—'));
    $('kClient').textContent = i.clientCounts || clientSet.size;
    $('kClientSub').textContent = `共 ${clientSet.size} 个客户端在建`;

    $('kProxy').textContent = online || state.proxies.length;
    const t = i.proxyTypeCount || {};
    const parts = Object.entries(t).filter(([, v]) => v > 0)
      .map(([k, v]) => `${k.toUpperCase()} ${v}`);
    $('kProxySub').textContent = parts.length ? parts.join(' · ') : `共 ${state.proxies.length} 条`;

    $('kConn').textContent = i.curConns;
    const avg = online ? (i.curConns / online).toFixed(1) : '0';
    $('kConnSub').textContent = `平均 ${avg} 条 / 代理`;

    const total = i.totalIn + i.totalOut;
    $('kTraffic').textContent = (total / 1024 ** 3).toFixed(1);
    $('kTrafficSub').textContent = `↑ ${bytes(i.totalIn)} · ↓ ${bytes(i.totalOut)}`;
  }

  function renderSpark() {
    const W = 372, H = 54;
    const max = Math.max(...state.samplesDown, ...state.samplesUp, 1);
    const draw = (arr) => {
      if (arr.length < 2) return { line: '', fill: '' };
      const step = W / (arr.length - 1);
      const d = arr.map((v, i) => {
        const x = (i * step).toFixed(1);
        const y = (H - 2 - (v / max) * (H - 8)).toFixed(1);
        return `${i ? 'L' : 'M'}${x} ${y}`;
      }).join('');
      return { line: d, fill: `${d}L${W} ${H}L0 ${H}Z` };
    };
    const down = draw(state.samplesDown);
    const up = draw(state.samplesUp);
    $('sparkDown').setAttribute('d', down.line);
    $('sparkDownFill').setAttribute('d', down.fill);
    $('sparkUp').setAttribute('d', up.line);
  }

  /* ---------------- 渲染：代理表 ---------------- */
  function typeClass(t) {
    return ['tcp', 'http', 'udp', 'sealed-tcp'].includes(t) ? 't-' + t : 't-tcp';
  }

  function filtered() {
    const { type, q, onlineOnly } = state.filter;
    const kw = q.trim().toLowerCase();
    return state.proxies.filter((p) => {
      if (type !== 'all' && p.type !== type) return false;
      if (onlineOnly && !p.online) return false;
      if (kw && !(`${p.name} ${p.client} ${p.entry}`.toLowerCase().includes(kw))) return false;
      return true;
    });
  }

  function renderTabs() {
    const counts = { all: state.proxies.length };
    for (const p of state.proxies) counts[p.type] = (counts[p.type] || 0) + 1;
    document.querySelectorAll('#tabs [data-count]').forEach((n) => {
      n.textContent = counts[n.dataset.count] || 0;
    });
    const online = state.proxies.filter((p) => p.online).length;
    $('proxyChip').textContent = `${online} 在线`;
  }

  function renderTable() {
    const list = filtered();
    const pages = Math.max(1, Math.ceil(list.length / PAGE_SIZE));
    const page = Math.min(state.filter.page, pages);
    state.filter.page = page;
    const slice = list.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

    const box = $('rows');
    box.innerHTML = '';
    if (!slice.length) {
      box.appendChild(el('div', 'lempty', '没有匹配的代理'));
    }
    for (const p of slice) {
      const row = el('div', 'prow' + (p.online ? '' : ' offline'));
      row.innerHTML = `
        <span class="c-name" title="${p.name}">${p.name}</span>
        <span class="c-type ${typeClass(p.type)}">${p.type.toUpperCase()}</span>
        <span class="c-client" title="${p.client}">${p.client}</span>
        <span class="c-entry" title="${p.entry}">${p.entry}</span>
        <span class="c-traffic">${p.trafficIn + p.trafficOut ? bytes(p.trafficIn + p.trafficOut) : '—'}</span>
        <span class="state-pill ${p.online ? 'on' : p.waiting ? 'wait' : 'off'}">${p.online ? '在线' : p.waiting ? '等待' : '离线'}</span>
        <button class="more" aria-label="更多">⋯</button>`;
      row.querySelector('.more').addEventListener('click', (e) => openMenu(e.currentTarget, p));
      box.appendChild(row);
    }

    $('pageInfo').textContent = `共 ${list.length} 条 · 第 ${page} / ${pages} 页`;
    const pager = $('pager');
    pager.innerHTML = '';
    for (let n = 1; n <= Math.min(pages, 5); n++) {
      const b = el('button', 'pg', String(n));
      if (n === page) b.setAttribute('aria-current', 'true');
      b.addEventListener('click', () => { state.filter.page = n; renderTable(); });
      pager.appendChild(b);
    }
  }

  /* ---------------- 菜单 ---------------- */
  function openMenu(anchor, p) {
    const m = $('menu');
    m.innerHTML = '';
    const item = (label, fn, cls) => {
      const b = el('button', cls || '', label);
      b.addEventListener('click', () => { m.classList.remove('open'); fn(); });
      m.appendChild(b);
    };
    item('复制代理名称', () => copy(p.name, '已复制名称'));
    item('复制公网入口', () => copy(p.entry, '已复制入口'));
    m.appendChild(el('hr'));
    item('查看原始数据', () => {
      const json = JSON.stringify(p, null, 2);
      console.log(`[${p.name}]`, json);
      copy(json, '原始 JSON 已复制，也打印在控制台');
    });
    const r = anchor.getBoundingClientRect();
    m.classList.add('open');
    const w = m.offsetWidth, h = m.offsetHeight;
    m.style.left = Math.min(r.right - w, window.innerWidth - w - 12) + 'px';
    m.style.top = Math.min(r.bottom + 6, window.innerHeight - h - 12) + 'px';
  }
  document.addEventListener('click', (e) => {
    if (!e.target.closest('#menu') && !e.target.closest('.more')) $('menu').classList.remove('open');
  });

  /* ---------------- 渲染：配置 ---------------- */
  function renderConfig() {
    const i = state.info;
    if (!i) return;
    $('cBind').value = i.bindPort || '—';
    $('cKcp').value = i.kcpPort || '未启用';
    $('cHttp').value = i.vhostHttpPort || '未启用';
    $('cHttps').value = i.vhostHttpsPort || '未启用';
    $('cQuic').value = i.quicPort || '未启用';
    $('cPool').value = i.maxPoolCount || '—';
    $('cSub').value = i.subdomainHost || '未设置';
    $('cPorts').value = i.allowPorts || '不限制';
    $('cTls').setAttribute('aria-checked', String(!!i.tlsForce));
  }

  function tomlSnippet() {
    const i = state.info || {};
    const lines = [
      `bindPort = ${i.bindPort || 7000}`,
      i.kcpPort ? `kcpBindPort = ${i.kcpPort}` : null,
      i.quicPort ? `quicBindPort = ${i.quicPort}` : null,
      i.vhostHttpPort ? `vhostHTTPPort = ${i.vhostHttpPort}` : null,
      i.vhostHttpsPort ? `vhostHTTPSPort = ${i.vhostHttpsPort}` : null,
      i.subdomainHost ? `subdomainHost = "${i.subdomainHost}"` : null,
      i.maxPoolCount ? `transport.maxPoolCount = ${i.maxPoolCount}` : null,
      i.heartbeatTimeout ? `transport.heartbeatTimeout = ${i.heartbeatTimeout}` : null,
      `transport.tls.force = ${i.tlsForce ? 'true' : 'false'}`,
      i.allowPorts ? `allowPorts = [{ start = ${String(i.allowPorts).split('-')[0]}, end = ${String(i.allowPorts).split('-')[1] || ''} }]` : null,
      `webServer.addr = "127.0.0.1"`,
      `webServer.port = 7500`,
      `auth.method = "token"`,
      `auth.token = "请替换为你的密钥"`,
    ].filter(Boolean);
    return lines.join('\n');
  }

  async function copy(text, msg) {
    try {
      await navigator.clipboard.writeText(text);
      toast(msg || '已复制');
    } catch (_) {
      toast('复制失败，请手动选择文本', true);
    }
  }

  /* ---------------- 渲染：日志 ---------------- */
  function renderLogs() {
    const body = $('logBody');
    const list = state.events.filter((e) => state.logLevel === 'ALL' || e.level === state.logLevel);
    if (!list.length) {
      body.innerHTML = '<div class="lempty">暂无事件</div>';
      return;
    }
    const nearBottom = body.scrollHeight - body.scrollTop - body.clientHeight < 40;
    body.innerHTML = list.map((e) => {
      const cls = e.level === 'WARN' ? 'warn' : e.level === 'ERROR' ? 'error' : '';
      return `<div class="lline ${cls}">
        <span class="ts">${e.time}</span>
        <span class="lv lv-${e.level}">${e.level}</span>
        <span class="msg" title="${e.msg}">${e.msg}</span>
      </div>`;
    }).join('');
    if (nearBottom) body.scrollTop = body.scrollHeight;
  }

  /* ---------------- 轮询 ---------------- */
  let timer = null;

  async function poll() {
    let info, proxies;
    try {
      info = await API.serverInfo();
      proxies = await API.proxies(API.host());
      state.connected = true;
      state.demo = false;
    } catch (err) {
      if (!state.demo && !state.connected) {
        // 首次就连不上 → 进演示模式，让界面先能看
        state.demo = true;
        info = { ...DEMO_INFO, totalIn: DEMO_INFO.totalIn, totalOut: DEMO_INFO.totalOut };
        proxies = buildDemo();
        pushEvent('WARN', '未连接服务端，当前为演示数据');
        pushEvent('INFO', '在地址后加 ?api=http://127.0.0.1:7500 接入真实服务端');
      } else if (state.demo) {
        info = { ...state.info };
        info.totalIn += Math.random() * 1.6e7;
        info.totalOut += Math.random() * 6.2e7;
        info.curConns = Math.max(60, Math.round(info.curConns + (Math.random() - 0.5) * 8));
        proxies = state.proxies.map((p) => {
          if (!p.online) return p;
          const addIn = Math.random() * 2.2e6;
          const addOut = Math.random() * 8e6;
          return { ...p, trafficIn: p.trafficIn + addIn, trafficOut: p.trafficOut + addOut };
        });
      } else {
        state.connected = false;
        $('engine').className = 'pill bad';
        $('engineText').textContent = '连接失败';
        $('statusText').textContent = '与 lumos 断开连接';
        $('sdot').className = 'sdot bad';
        pushEvent('ERROR', `拉取失败：${err.message}（${API.cfg.base || location.origin}）`);
        return;
      }
    }

    // 速率：用累计流量差分，这是 lumos 服务端能提供的原始数据
    if (state.info) {
      const now = info.totalIn + info.totalOut;
      const before = state.prevTotal;
      const dt = API.cfg.interval / 1000;
      if (before != null) {
        const dIn = Math.max(0, (info.totalIn - (state.info.totalIn || 0)) / dt);
        const dOut = Math.max(0, (info.totalOut - (state.info.totalOut || 0)) / dt);
        state.samplesUp.push(dIn);
        state.samplesDown.push(dOut);
        if (state.samplesUp.length > MAX_SAMPLES) state.samplesUp.shift();
        if (state.samplesDown.length > MAX_SAMPLES) state.samplesDown.shift();
        $('rateUp').textContent = '↑ ' + rate(dIn);
        $('rateDown').textContent = '↓ ' + rate(dOut);
      }
      state.prevTotal = now;
    }

    diffEvents(proxies, [...new Set(proxies.map((p) => p.client).filter((c) => c && c !== '—'))]);
    state.info = info;
    state.proxies = proxies;
    state.clients = [...new Set(proxies.map((p) => p.client).filter((c) => c && c !== '—'))];

    renderStatus();
    renderSpark();
    renderTabs();
    renderTable();
    renderConfig();
  }

  function start() {
    poll();
    clearInterval(timer);
    timer = setInterval(poll, API.cfg.interval);
  }

  /* ---------------- 交互绑定 ---------------- */
  function bind() {
    $('tabs').addEventListener('click', (e) => {
      const b = e.target.closest('.tab');
      if (!b) return;
      document.querySelectorAll('#tabs .tab').forEach((t) => t.setAttribute('aria-selected', 'false'));
      b.setAttribute('aria-selected', 'true');
      state.filter.type = b.dataset.type;
      state.filter.page = 1;
      renderTable();
    });

    $('search').addEventListener('input', (e) => {
      state.filter.q = e.target.value;
      state.filter.page = 1;
      renderTable();
    });

    const toggle = (node, onChange) => {
      const key = () => node.getAttribute('aria-checked') === 'true';
      const flip = () => { node.setAttribute('aria-checked', String(!key())); onChange(!key()); };
      node.addEventListener('click', flip);
      node.addEventListener('keydown', (e) => {
        if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); }
      });
    };
    toggle($('onlineOnly'), (v) => { state.filter.onlineOnly = v; state.filter.page = 1; renderTable(); });
    toggle($('logLive'), (v) => {
      state.logLive = v;
      $('logChip').textContent = v ? 'LIVE' : 'PAUSED';
      $('logChip').style.color = v ? '#3DDC97' : '#6C788E';
    });

    $('logLevels').addEventListener('click', (e) => {
      const b = e.target.closest('.tab');
      if (!b) return;
      document.querySelectorAll('#logLevels .tab').forEach((t) => t.setAttribute('aria-selected', 'false'));
      b.setAttribute('aria-selected', 'true');
      state.logLevel = b.dataset.level;
      renderLogs();
    });

    $('refresh').addEventListener('click', () => { poll(); toast('已刷新'); });
    $('reload').addEventListener('click', () => { poll(); toast('已重新读取配置'); });
    $('copyToml').addEventListener('click', () => copy(tomlSnippet(), 'lumos.toml 片段已复制'));
    $('copyToml2').addEventListener('click', () => copy(tomlSnippet(), 'lumos.toml 片段已复制'));

    window.addEventListener('resize', () => $('menu').classList.remove('open'));
  }

  function toast(msg, bad) {
    const t = $('toast');
    $('toastText').textContent = msg;
    t.className = 'toast show' + (bad ? ' bad' : '');
    clearTimeout(toast._t);
    toast._t = setTimeout(() => { t.className = 'toast' + (bad ? ' bad' : ''); }, 2200);
  }

  setInterval(() => {
    if (state.info) $('uptime').textContent = '已监控 ' + hms(Date.now() - state.startedAt);
  }, 1000);

  bind();
  start();
})();
