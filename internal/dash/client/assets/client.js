/* ============================================================
   LUMO · lumoc 客户端逻辑
   ------------------------------------------------------------
   两种后端：
     agent   —— 连本地代理（agent/server.js），管理真实隧道连接
     local   —— 连不上时退回浏览器存储，只能编辑不能起进程
   ============================================================ */
(function () {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const qp = new URLSearchParams(location.search);
  // 默认同源代理模式（由 lumoc ui 提供）；?local=1 可退回浏览器存储预览
  const AGENT_BASE = (qp.get('agent') || '').replace(/\/$/, '');

  const state = {
    mode: qp.get('local') ? 'local' : 'agent',
    configs: [],
    status: {},
    info: null,
    selectedId: null,
    draft: null,        // 编辑中的副本：轮询替换 configs 时不会冲掉未保存的改动
    filter: 'all',
    q: '',
    view: 'list',
    pendingDelete: null,
  };

  const clone = (o) => JSON.parse(JSON.stringify(o));

  /* ---------------- 存储后端 ---------------- */
  const LS_KEY = 'lumo.lumoc.configs.v2';

  const uid = () => Math.random().toString(36).slice(2, 10);

  const Local = {
    read() {
      try { return JSON.parse(localStorage.getItem(LS_KEY)) || []; } catch (_) { return []; }
    },
    write(list) { localStorage.setItem(LS_KEY, JSON.stringify(list)); },
  };

  const api = {
    async list() {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/configs');
        if (!r.ok) throw new Error('agent');
        return r.json();
      }
      return Local.read();
    },
    async create(data) {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/configs', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        return d;
      }
      const list = Local.read();
      const cfg = {
        id: uid(), name: data.name || '未命名配置', serverAddr: data.serverAddr || '',
        serverPort: Number(data.serverPort) || 7000, token: data.token || '',
        protocol: 'tcp', tls: true, enabled: !!data.startNow,
        mappings: (data.mappings || []).map((m) => ({ id: uid(), enabled: true, type: 'tcp', localIP: '127.0.0.1', ...m })),
      };
      list.push(cfg); Local.write(list);
      return cfg;
    },
    async update(id, data) {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/configs/' + id, {
          method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        return d;
      }
      const list = Local.read();
      const i = list.findIndex((c) => c.id === id);
      if (i < 0) throw new Error('not found');
      list[i] = { ...list[i], ...data };
      Local.write(list);
      return list[i];
    },
    async remove(id) {
      if (state.mode === 'agent') {
        await fetch(AGENT_BASE + '/api/configs/' + id, { method: 'DELETE' });
        return;
      }
      Local.write(Local.read().filter((c) => c.id !== id));
    },
    async start(id) {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/configs/' + id + '/start', { method: 'POST' });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        return d;
      }
      return this.update(id, { enabled: true });
    },
    async stop(id) {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/configs/' + id + '/stop', { method: 'POST' });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        return d;
      }
      return this.update(id, { enabled: false });
    },
    async status() {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/status');
        if (!r.ok) throw new Error('agent');
        return r.json();
      }
      return [];
    },
    async info() {
      if (state.mode === 'agent') {
        const r = await fetch(AGENT_BASE + '/api/info');
        if (!r.ok) throw new Error('agent');
        return r.json();
      }
      return { engineBin: '未连接本地代理', dataDir: '浏览器存储', port: null };
    },
  };

  /* 与 agent/server.js 的 toToml 保持一致，仅用于预览 */
  function toToml(cfg) {
    const esc = (s) => String(s == null ? '' : s).replace(/\\/g, '\\\\').replace(/"/g, '\\"');
    const L = [
      `serverAddr = "${esc(cfg.serverAddr)}"`,
      `serverPort = ${Number(cfg.serverPort) || 7000}`,
      '', '[auth]', 'method = "token"',
      `token = "${esc(cfg.token)}"`,
      '', '[transport]',
      `protocol = "${esc(cfg.protocol || 'tcp')}"`,
      'heartbeatInterval = 30',
      '[transport.tls]', `enable = ${cfg.tls ? 'true' : 'false'}`,
      '', '[log]', 'level = "info"',
      '', '[webServer]', 'addr = "127.0.0.1"', `port = ${cfg.adminPort || 17400}`, '',
    ];
    for (const m of cfg.mappings || []) {
      if (m.enabled === false) continue;
      const name = `${cfg.id}-${m.id}`.replace(/[^a-zA-Z0-9_.-]/g, '-');
      L.push('[[proxies]]', `name = "${esc(name)}"`, `type = "${esc(m.type || 'tcp')}"`,
        `localIP = "${esc(m.localIP || '127.0.0.1')}"`, `localPort = ${Number(m.localPort) || 0}`);
      if (m.type === 'http' || m.type === 'https') {
        if (m.domain) L.push(`customDomains = ["${esc(m.domain)}"]`);
      } else {
        L.push(`remotePort = ${Number(m.remotePort) || 0}`);
      }
      L.push('');
    }
    return L.join('\n');
  }

  /* ---------------- 状态推导 ---------------- */
  function statusOf(cfg) {
    if (state.mode === 'local') {
      return cfg.enabled ? { cls: 'on', label: '已连接' } : { cls: '', label: '已停止' };
    }
    const st = state.status[cfg.id];
    if (!cfg.enabled) return { cls: '', label: '已停止' };
    if (!st) return { cls: 'wait', label: '连接中' };
    if (st.error) return { cls: 'err', label: '异常' };
    if (st.running) return (st.proxies && st.proxies.length)
      ? { cls: 'on', label: '已连接' } : { cls: 'wait', label: '连接中' };
    return { cls: 'wait', label: '启动中' };
  }

  const mappingSummary = (cfg) => {
    const m = (cfg.mappings || [])[0];
    if (!m) return '未配置映射';
    const local = `${m.localIP || '127.0.0.1'}:${m.localPort || '—'}`;
    const remote = (m.type === 'http' || m.type === 'https') ? (m.domain || '未填域名') : String(m.remotePort || '—');
    const more = cfg.mappings.length > 1 ? `  +${cfg.mappings.length - 1}` : '';
    return `${cfg.serverAddr}:${cfg.serverPort}  →  ${local}  ⇒  ${remote}${more}`;
  };

  /* ---------------- 渲染列表 ---------------- */
  function renderList() {
    const all = state.configs;
    const on = all.filter((c) => c.enabled).length;
    $('navCount').textContent = all.length;
    $('pageSub').textContent = all.length ? `共 ${all.length} 个配置 · ${on} 个正在运行` : '还没有配置';
    $('cAll').textContent = all.length;
    $('cOn').textContent = on;
    $('cOff').textContent = all.length - on;

    let list = all;
    if (state.filter === 'on') list = list.filter((c) => c.enabled);
    if (state.filter === 'off') list = list.filter((c) => !c.enabled);
    if (state.q.trim()) {
      const kw = state.q.trim().toLowerCase();
      list = list.filter((c) => `${c.name} ${c.serverAddr}`.toLowerCase().includes(kw));
    }

    const box = $('cards');
    box.innerHTML = '';
    $('empty').style.display = all.length ? 'none' : 'flex';

    // 底部统计：本地模式下用示例值，保证与设计稿一致
    if (state.mode === 'local') {
      $('stUp').textContent = '↑ 1.2 GB';
      $('stDown').textContent = '↓ 8.4 GB';
      $('stConns').textContent = '活跃连接 12 条';
    } else {
      let up = 0, down = 0, conns = 0;
      for (const s of Object.values(state.status)) {
        conns += (s.proxies || []).length;
      }
      $('stUp').textContent = '↑ —';
      $('stDown').textContent = '↓ —';
      $('stConns').textContent = `活跃连接 ${conns} 条`;
    }

    if (!list.length && all.length) {
      box.innerHTML = '<div class="hint" style="justify-content:center">没有匹配的配置</div>';
    }

    for (const c of list) {
      const st = statusOf(c);
      const card = document.createElement('div');
      card.className = 'ccard';
      card.setAttribute('aria-selected', String(c.id === state.selectedId));
      card.innerHTML = `
        <div class="well ${st.cls === 'on' ? 'on' : ''}">
          <svg width="22" height="22" viewBox="0 0 72 72" fill="none">
            <path d="M24 46V33C24 25.3 29.4 20 36 20C42.6 20 48 25.3 48 33V46"
              stroke="${st.cls === 'on' ? '#3DDC97' : st.cls === 'err' ? '#FF5F73' : '#6C788E'}"
              stroke-width="5" stroke-linecap="round"/>
            <path d="M30 37H42" stroke="${st.cls === 'on' ? '#3DDC97' : st.cls === 'err' ? '#FF5F73' : '#6C788E'}"
              stroke-width="4" stroke-linecap="round"/>
          </svg>
        </div>
        <div class="cinfo">
          <div class="cname">${escapeHtml(c.name)}</div>
          <div class="cmeta" title="${escapeHtml(mappingSummary(c))}">${escapeHtml(mappingSummary(c))}</div>
        </div>
        <div class="cacts">
          <span class="state ${st.cls}"><i></i>${st.label}</span>
          <span class="switch" role="switch" aria-checked="${c.enabled}" tabindex="0" aria-label="启用 ${escapeHtml(c.name)}"></span>
          <button class="more" aria-label="更多">⋯</button>
        </div>`;

      card.addEventListener('click', () => select(c.id));
      card.querySelector('.switch').addEventListener('click', (e) => {
        e.stopPropagation();
        toggleConfig(c.id, e.currentTarget);
      });
      card.querySelector('.more').addEventListener('click', (e) => {
        e.stopPropagation();
        openMenu(e.currentTarget, c);
      });
      box.appendChild(card);
    }
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, (m) => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m]
    ));
  }

  /* ---------------- 详情 ---------------- */
  function current() {
    return state.draft;
  }

  function renderDetail() {
    const c = current();
    const dis = !c;
    document.querySelectorAll('#detail input,#detail select,#detail .switch,#saveBtn,#deleteBtn,#addMap,#addMapTop')
      .forEach((n) => { if (n.id) n.disabled = dis; });

    if (!c) {
      $('dName').textContent = '—';
      $('dSub').textContent = '选择左侧配置查看详情';
      ['fName', 'fAddr', 'fPort', 'fToken'].forEach((k) => ($(k).value = ''));
      $('mapList').innerHTML = '';
      $('mapTitle').textContent = '端口映射';
      $('dSwitch').setAttribute('aria-checked', 'false');
      return;
    }

    const st = statusOf(c);
    $('dName').textContent = c.name;
    $('dSub').textContent = `${c.serverAddr}:${c.serverPort} · ${(c.protocol || 'tcp').toUpperCase()} · ${st.label}`;
    $('fName').value = c.name;
    $('fAddr').value = c.serverAddr;
    $('fPort').value = c.serverPort;
    $('fProtocol').value = c.protocol || 'tcp';
    $('fToken').value = c.token || '';
    $('fTls').setAttribute('aria-checked', String(!!c.tls));
    $('dSwitch').setAttribute('aria-checked', String(!!c.enabled));
    $('mapTitle').textContent = `端口映射 ${c.mappings.length} 条`;

    const box = $('mapList');
    box.innerHTML = '';
    c.mappings.forEach((m, idx) => {
      const row = document.createElement('div');
      row.className = 'map';
      const isHttp = m.type === 'http' || m.type === 'https';
      row.innerHTML = `
        <select class="mtype">
          ${['tcp', 'http', 'udp', 'sealed-tcp'].map((t) =>
        `<option value="${t}" ${m.type === t ? 'selected' : ''}>${t.toUpperCase()}</option>`).join('')}
        </select>
        <input class="local" value="${escapeHtml((m.localIP || '127.0.0.1') + ':' + (m.localPort || ''))}" aria-label="本地地址">
        <span style="color:#5A7BFF">→</span>
        <input class="remote" value="${escapeHtml(isHttp ? (m.domain || '') : (m.remotePort || ''))}"
          placeholder="${isHttp ? '域名' : '公网端口'}" aria-label="公网入口">
        <button class="del" aria-label="删除这条映射">
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 4.5H12.5M6.5 4.5V3.2H9.5V4.5M5 4.5L5.6 12.6H10.4L11 4.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>`;
      row.querySelector('.mtype').addEventListener('change', (e) => {
        c.mappings[idx].type = e.target.value;
        renderDetail();
      });
      row.querySelector('.local').addEventListener('input', (e) => {
        const v = e.target.value.trim();
        const i = v.lastIndexOf(':');
        if (i > -1) { c.mappings[idx].localIP = v.slice(0, i) || '127.0.0.1'; c.mappings[idx].localPort = Number(v.slice(i + 1)) || 0; }
        else { c.mappings[idx].localPort = Number(v) || 0; }
      });
      row.querySelector('.remote').addEventListener('input', (e) => {
        const t = c.mappings[idx].type;
        if (t === 'http' || t === 'https') c.mappings[idx].domain = e.target.value.trim();
        else c.mappings[idx].remotePort = Number(e.target.value.trim()) || 0;
      });
      row.querySelector('.del').addEventListener('click', () => {
        if (c.mappings.length === 1) return toast('至少保留一条映射', true);
        c.mappings.splice(idx, 1);
        renderDetail();
      });
      box.appendChild(row);
    });
  }

  /* ---------------- 行为 ---------------- */
  function select(id) {
    state.selectedId = id;
    const cfg = state.configs.find((c) => c.id === id);
    state.draft = cfg ? clone(cfg) : null;
    renderList();
    renderDetail();
    if (window.matchMedia('(max-width:980px)').matches) $('detail').classList.add('open');
  }

  async function toggleConfig(id, node) {
    const c = state.configs.find((x) => x.id === id);
    if (!c) return;
    const next = node.getAttribute('aria-checked') !== 'true';
    node.setAttribute('aria-checked', String(next));
    try {
      if (next) {
        const r = await api.start(id);
        c.enabled = true;
        if (r && r.error) toast(r.error, true);
        else toast(state.mode === 'agent' ? `已启用 ${c.name}` : `已切换为启用状态（界面预览，未启动真实进程）`);
      } else {
        await api.stop(id);
        c.enabled = false;
        toast(`已停止 ${c.name}`);
      }
    } catch (e) {
      node.setAttribute('aria-checked', String(!next));
      toast('操作失败：' + e.message, true);
    }
    await refresh();
  }

  async function refresh(opt) {
    const keepDetail = !!(opt && opt.keepDetail);
    try {
      state.configs = await api.list();
      state.status = {};
      for (const s of await api.status()) state.status[s.id] = s;
      state.info = await api.info();
    } catch (e) {
      toast('读取失败：' + e.message, true);
    }
    renderEngine();
    renderList();
    // 轮询时不要重绘表单，否则会覆盖用户正在输入的内容
    if (keepDetail) {
      updateDetailStatus();
    } else {
      if (!state.selectedId && state.configs.length) state.selectedId = state.configs[0].id;
      const cfg = state.configs.find((x) => x.id === state.selectedId);
      state.draft = cfg ? clone(cfg) : null;
      renderDetail();
    }
    renderLogs();
    renderSettings();
  }

  /* 轮询时只刷新状态文字，不碰输入框 */
  function updateDetailStatus() {
    if (!state.draft) return;
    const live = state.configs.find((x) => x.id === state.selectedId) || state.draft;
    const st = statusOf(live);
    $('dSwitch').setAttribute('aria-checked', String(!!live.enabled));
    $('dSub').textContent = `${live.serverAddr}:${live.serverPort} · ${(live.protocol || 'tcp').toUpperCase()} · ${st.label}`;
  }

  function renderEngine() {
    const agent = state.mode === 'agent';
    $('engDot').className = 'dot';
    $('engText').textContent = agent ? '控制台已连接' : '本地配置';
    $('engAddr').textContent = agent ? (location.host || '本机') : '浏览器本地存储';
    $('engVer').textContent = agent ? ('v' + (state.info && state.info.version || '1')) : '静态';
    $('userPlan').textContent = agent ? '隧道引擎运行中' : '界面预览';
  }

  function renderLogs() {
    const box = $('viewLogs');
    const running = Object.values(state.status).filter((s) => s.running);
    if (!running.length) {
      box.innerHTML = '<div class="hint" style="margin:16px">没有正在运行的配置。启用一个配置后，这里会显示 lumoc 的实时输出。</div>';
      return;
    }
    const lines = [];
    for (const s of running) {
      const c = state.configs.find((x) => x.id === s.id);
      lines.push(`<div class="logline"><span class="tag">── ${escapeHtml(c ? c.name : s.id)} ──</span></div>`);
      if (s.error) lines.push(`<div class="logline err">${escapeHtml(s.error)}</div>`);
      for (const l of s.log || []) lines.push(`<div class="logline">${escapeHtml(l)}</div>`);
    }
    box.innerHTML = lines.join('');
    box.scrollTop = box.scrollHeight;
  }

  function renderSettings() {
    const i = state.info || {};
    $('sAgent').textContent = state.mode === 'agent' ? AGENT_BASE : '未连接';
    $('sBin').textContent = i.engineBin || '—';
    $('sData').textContent = i.dataDir || '—';
  }

  /* ---------------- 菜单 ---------------- */
  function openMenu(anchor, cfg) {
    const m = $('menu');
    m.innerHTML = '';
    const add = (label, fn, cls) => {
      const b = document.createElement('button');
      b.className = cls || '';
      b.textContent = label;
      b.addEventListener('click', () => { m.classList.remove('open'); fn(); });
      m.appendChild(b);
    };
    add('编辑', () => select(cfg.id));
    add('查看 lumoc.toml', () => showToml(cfg));
    add('复制服务器地址', () => copy(`${cfg.serverAddr}:${cfg.serverPort}`, '已复制'));
    m.appendChild(document.createElement('hr'));
    add('删除', () => askDelete(cfg), 'danger');

    const r = anchor.getBoundingClientRect();
    m.classList.add('open');
    m.style.left = Math.min(r.right - m.offsetWidth, innerWidth - m.offsetWidth - 12) + 'px';
    m.style.top = Math.min(r.bottom + 6, innerHeight - m.offsetHeight - 12) + 'px';
  }
  document.addEventListener('click', (e) => {
    if (!e.target.closest('#menu') && !e.target.closest('.more')) $('menu').classList.remove('open');
  });

  /* ---------------- 弹窗 ---------------- */
  function openModal(id) { $(id).classList.add('open'); }
  function closeModal(id) { $(id).classList.remove('open'); }
  document.querySelectorAll('[data-close]').forEach((b) =>
    b.addEventListener('click', () => closeModal(b.dataset.close)));
  document.querySelectorAll('.overlay').forEach((o) =>
    o.addEventListener('click', (e) => { if (e.target === o) o.classList.remove('open'); }));

  /* ---------------- 模板 ---------------- */
  const TPL = {
    nas: { name: '家庭 NAS', local: '127.0.0.1:5000', remote: '15000' },
    rdp: { name: '办公室远程桌面', local: '10.0.0.8:3389', remote: '15001' },
    web: { name: '网站服务', local: '127.0.0.1:8080', remote: '15002' },
  };

  /* ---------------- 事件绑定 ---------------- */
  function bind() {
    $('newBtn').addEventListener('click', () => {
      $('nName').value = ''; $('nToken').value = '';
      $('nAddr').value = current()?.serverAddr || '';
      $('nPort').value = current()?.serverPort || 7000;
      $('nMap').querySelector('.local').value = '127.0.0.1:5000';
      $('nMap').querySelector('.remote').value = '15000';
      $('nStart').setAttribute('aria-checked', 'true');
      openModal('modalNew');
    });
    $('emptyNew').addEventListener('click', () => $('newBtn').click());

    document.querySelectorAll('[data-tpl]').forEach((b) => {
      b.addEventListener('click', () => {
        const t = TPL[b.dataset.tpl];
        $('nName').value = t.name;
        $('nMap').querySelector('.local').value = t.local;
        $('nMap').querySelector('.remote').value = t.remote;
      });
    });

    $('createBtn').addEventListener('click', async () => {
      const addr = $('nAddr').value.trim();
      const name = $('nName').value.trim();
      if (!addr) return toast('请填写服务器地址', true);
      const localRaw = $('nMap').querySelector('.local').value.trim();
      const i = localRaw.lastIndexOf(':');
      const mapping = {
        type: $('nMap').querySelector('.mtype').value,
        localIP: i > -1 ? localRaw.slice(0, i) : '127.0.0.1',
        localPort: Number(i > -1 ? localRaw.slice(i + 1) : localRaw) || 0,
        remotePort: Number($('nMap').querySelector('.remote').value) || 0,
      };
      const cfg = await api.create({
        name: name || '未命名配置',
        serverAddr: addr,
        serverPort: Number($('nPort').value) || 7000,
        token: $('nToken').value,
        mappings: [mapping],
        startNow: $('nStart').getAttribute('aria-checked') === 'true',
      });
      closeModal('modalNew');
      state.selectedId = cfg.id;
      await refresh();
      toast(`已创建 ${cfg.name}`);
    });

    document.querySelectorAll('.chip').forEach((b) => {
      b.addEventListener('click', () => {
        document.querySelectorAll('.chip').forEach((x) => x.setAttribute('aria-pressed', 'false'));
        b.setAttribute('aria-pressed', 'true');
        state.filter = b.dataset.filter;
        renderList();
      });
    });

    $('search').addEventListener('input', (e) => { state.q = e.target.value; renderList(); });

    /* 开关（通用） */
    const bindSwitch = (node, onChange) => {
      if (!node) return;
      const flip = () => {
        if (node.hasAttribute('disabled')) return;
        const next = node.getAttribute('aria-checked') !== 'true';
        node.setAttribute('aria-checked', String(next));
        onChange(next);
      };
      node.addEventListener('click', flip);
      node.addEventListener('keydown', (e) => {
        if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); }
      });
    };
    bindSwitch($('fTls'), (v) => { const c = current(); if (c) c.tls = v; });
    bindSwitch($('dSwitch'), async (v) => {
      const c = current(); if (!c) return;
      if (v) { const r = await api.start(c.id); c.enabled = true; if (r?.error) toast(r.error, true); }
      else { await api.stop(c.id); c.enabled = false; }
      await refresh();
    });

    $('addMap').addEventListener('click', addMapping);
    $('addMapTop').addEventListener('click', addMapping);
    function addMapping() {
      const c = current(); if (!c) return;
      c.mappings.push({ id: uid(), enabled: true, type: 'tcp', localIP: '127.0.0.1', localPort: 0, remotePort: 0 });
      renderDetail();
    }

    $('saveBtn').addEventListener('click', async () => {
      const c = current();
      if (!c) return;
      const payload = {
        name: $('fName').value.trim() || c.name,
        serverAddr: $('fAddr').value.trim(),
        serverPort: Number($('fPort').value) || 7000,
        protocol: $('fProtocol').value,
        token: $('fToken').value,
        tls: $('fTls').getAttribute('aria-checked') === 'true',
        mappings: c.mappings,
      };
      if (!payload.serverAddr) return toast('服务器地址不能为空', true);
      const wasRunning = c.enabled;
      try {
        const r = await api.update(c.id, payload);
        await refresh();
        toast(r?.restarted || wasRunning ? '已保存，正在重启该配置的进程' : '已保存');
      } catch (e) {
        toast('保存失败：' + e.message, true);
      }
    });

    $('deleteBtn').addEventListener('click', () => { const c = current(); if (c) askDelete(c); });
    $('delConfirm').addEventListener('click', async () => {
      const c = state.pendingDelete;
      if (!c) return;
      await api.remove(c.id);
      if (state.selectedId === c.id) state.selectedId = null;
      closeModal('modalDel');
      await refresh();
      toast(`已删除 ${c.name}`);
    });

    $('viewToml').addEventListener('click', () => { const c = current(); if (c) showToml(c); });
    $('copyToml').addEventListener('click', () => copy($('tomlBody').textContent, 'lumoc.toml 已复制'));

    document.querySelectorAll('.nav').forEach((b) => {
      b.addEventListener('click', () => {
        document.querySelectorAll('.nav').forEach((x) => x.removeAttribute('aria-current'));
        b.setAttribute('aria-current', 'page');
        state.view = b.dataset.view;
        $('viewList').style.display = state.view === 'list' ? 'grid' : 'none';
        $('viewLogs').style.display = state.view === 'logs' ? 'flex' : 'none';
        $('viewSettings').style.display = state.view === 'settings' ? 'flex' : 'none';
        $('pageTitle').textContent = { list: '连接配置', logs: '运行日志', settings: '偏好设置' }[state.view];
        $('pageSub').textContent = state.view === 'list'
          ? (state.configs.length ? `共 ${state.configs.length} 个配置 · ${state.configs.filter((c) => c.enabled).length} 个正在运行` : '还没有配置')
          : state.view === 'logs' ? '由本地代理转发 lumoc 的实时输出' : '本地代理与运行环境';
      });
    });
  }

  function askDelete(cfg) {
    state.pendingDelete = cfg;
    $('delTitle').textContent = `删除「${cfg.name}」？`;
    $('delSummary').textContent = `${cfg.serverAddr}:${cfg.serverPort}  →  ${(cfg.mappings || []).length} 条映射`;
    openModal('modalDel');
  }

  function showToml(cfg) {
    $('tomlBody').textContent = toToml(cfg);
    openModal('modalToml');
  }

  async function copy(text, msg) {
    try { await navigator.clipboard.writeText(text); toast(msg); }
    catch (_) { toast('复制失败，请手动选择', true); }
  }

  function toast(msg, bad) {
    const t = $('toast');
    $('toastText').textContent = msg;
    t.className = 'toast show' + (bad ? ' bad' : '');
    clearTimeout(toast._t);
    toast._t = setTimeout(() => { t.className = 'toast' + (bad ? ' bad' : ''); }, 2400);
  }

  /* 纯静态模式首次打开：预置与设计稿一致的七条配置 */
  if (state.mode === 'local' && Local.read().length === 0) {
    const mk = (o) => ({
      id: uid(), enabled: false, protocol: 'tcp', tls: true, token: 's3cr3t',
      autoReconnect: true, autoStart: false, ...o,
    });
    const map = (o) => ({ id: uid(), enabled: true, type: 'tcp', localIP: '127.0.0.1', ...o });
    Local.write([
      mk({
        name: '家庭 NAS', serverAddr: 'hk-01.lumo.net', serverPort: 7000, enabled: true,
        mappings: [map({ localPort: 5000, remotePort: 15000 }),
        map({ type: 'http', localPort: 5000, domain: 'nas.lumo.net' })],
      }),
      mk({
        name: '公司内网 ERP', serverAddr: 'sg-office.lumo.net', serverPort: 7000, enabled: true,
        mappings: [map({ localIP: '192.168.1.20', localPort: 8080, remotePort: 15001 })],
      }),
      mk({
        name: '树莓派管理面板', serverAddr: 'hk-01.lumo.net', serverPort: 7000,
        mappings: [map({ localPort: 3000, remotePort: 15002 })],
      }),
      mk({
        name: '办公室远程桌面', serverAddr: 'cn-sh.lumo.net', serverPort: 7000,
        mappings: [map({ localIP: '10.0.0.8', localPort: 3389, remotePort: 15003 })],
      }),
      mk({
        name: '测试环境 API', serverAddr: 'hk-01.lumo.net', serverPort: 7000,
        mappings: [map({ localPort: 8000, remotePort: 15004 })],
      }),
      mk({
        name: '公司 Git 服务', serverAddr: 'hk-01.lumo.net', serverPort: 7000,
        mappings: [map({ localPort: 2222, remotePort: 15005 })],
      }),
      mk({
        name: '智能家居网关', serverAddr: 'hk-01.lumo.net', serverPort: 7000,
        mappings: [map({ localIP: '192.168.1.50', localPort: 8123, remotePort: 15006 })],
      }),
    ]);
  }

  bind();
  refresh();
  setInterval(() => refresh({ keepDetail: true }), 2500);
})();
