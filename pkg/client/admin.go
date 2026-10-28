package client

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/j353719624/lumo/pkg/version"
)

// httpServer is the admin dashboard server type; aliased for the Client struct.
type httpServer = http.Server

// runAdmin serves the client console until ctx is cancelled.
func (c *Client) runAdmin(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", c.apiStatus)
	mux.HandleFunc("/api/reload", c.apiReload)
	mux.Handle("/", c.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(singlePageHTML))
	})))

	addr := c.cfg.Web.Addr + ":" + strconv.Itoa(c.cfg.Web.Port)
	srv := &http.Server{Addr: addr, Handler: mux}
	c.admin = srv
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	c.logi("dashboard listening on http://%s (user %s)", addr, c.cfg.Web.User)
	if err := srv.ListenAndServe(); err != nil {
		c.logd("dashboard stopped: %v", err)
	}
}

func (c *Client) guard(next http.Handler) http.Handler {
	user, pass := c.cfg.Web.User, c.cfg.Web.Password
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="LUMO"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *Client) apiStatus(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	list := make([]map[string]any, 0, len(c.proxies))
	for name, p := range c.proxies {
		st := c.state[name]
		entry := map[string]any{
			"name":   name,
			"type":   p.Type,
			"local":  p.LocalIP + ":" + itoa(p.LocalPort),
			"status": st.Status,
			"err":    st.Err,
		}
		if p.RemotePort > 0 {
			entry["entry"] = c.cfg.ServerAddr + ":" + itoa(p.RemotePort)
		} else if len(p.CustomDomains) > 0 {
			entry["entry"] = p.CustomDomains[0]
		}
		if cnt := c.stats[name]; cnt != nil {
			entry["traffic_in"] = cnt.In.Load()
			entry["traffic_out"] = cnt.Out.Load()
		}
		list = append(list, entry)
	}
	c.mu.Unlock()
	writeJSON(w, map[string]any{"version": version.Version, "proxies": list})
}

func (c *Client) apiReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := c.Reload(); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c.logi("configuration reloaded on request")
	writeJSON(w, map[string]any{"ok": true})
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// singlePageHTML is the minimal admin page for single-config mode
// (lumoc -c). The full console lives behind `lumoc ui`.
const singlePageHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>LUMO · 客户端状态</title>
<style>
body{margin:0;min-height:100vh;background:#0d1117;color:#d7dee8;
font:14px/1.6 "Segoe UI","Microsoft YaHei",sans-serif}
header{display:flex;gap:10px;align-items:center;padding:14px 22px;border-bottom:1px solid #1e2733}
.wordmark{font-weight:700;letter-spacing:2px}
.badge{font-size:10px;padding:2px 8px;border-radius:999px;background:#16233a;color:#7ea2ff}
main{max-width:860px;margin:26px auto;padding:0 18px}
.card{background:#131a24;border:1px solid #1e2733;border-radius:12px;padding:18px 20px}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #1c2532;font-size:13px}
th{color:#8494ab}
.muted{color:#8494ab}
button{background:#2b6cff;color:#fff;border:0;border-radius:8px;padding:8px 18px;cursor:pointer}
</style></head><body>
<header><svg width="20" height="20" viewBox="0 0 72 72" fill="none"><path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#5A7BFF" stroke-width="7" stroke-linecap="round"/><path d="M10 42H62" stroke="#35E0C8" stroke-width="4.8" stroke-linecap="round"/></svg>
<span class="wordmark">LUMO</span><span class="badge">CLIENT</span></header>
<main><div class="card"><h2 style="margin:0 0 12px;font-size:15px">代理状态</h2>
<table><thead><tr><th>名称</th><th>类型</th><th>本地</th><th>入口</th><th>状态</th></tr></thead>
<tbody id="rows"><tr><td colspan="5" class="muted">加载中…</td></tr></tbody></table>
<p class="muted" style="margin-top:14px">管理多条连接请使用 <b>lumoc ui</b> 控制台。</p>
<button id="reload">重新加载配置</button></div></main>
<script>
async function refresh(){
 const r=await fetch('/api/status'); if(!r.ok){location.reload();return}
 const d=await r.json();
 document.getElementById('rows').innerHTML=(d.proxies||[]).map(p=>
 '<tr><td><b>'+p.name+'</b></td><td>'+String(p.type).toUpperCase()+'</td><td>'+p.local+'</td><td>'+(p.entry||'—')+'</td><td>'+p.status+'</td></tr>').join('')
 ||'<tr><td colspan="5" class="muted">尚无代理</td></tr>';
}
document.getElementById('reload').onclick=async()=>{await fetch('/api/reload',{method:'POST'});refresh()};
refresh();setInterval(refresh,3000);
</script></body></html>`
