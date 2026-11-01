// Command engine runs one tunnel connection as a standalone process and
// exposes a localhost-only status endpoint for the console supervisor.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/config"
)

// runEngine implements `lumoc engine -c <toml> --admin <addr>`.
func runEngine(args []string) {
	fs := flag.NewFlagSet("lumoc engine", flag.ExitOnError)
	cfgPath := fs.String("c", "", "engine configuration file")
	admin := fs.String("admin", "127.0.0.1:0", "local status endpoint")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "lumoc engine: -c <config> is required")
		os.Exit(1)
	}
	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc engine:", err)
		os.Exit(1)
	}

	// auto 协议：TCP 主 + KCP 备两条腿同时在线，punch 类映射只走 KCP 腿
	//（UDP 打洞不双归，避免服务端单归属冲突）。
	var legs []*client.Client
	if cfg.Protocol == "auto" {
		tcpCfg := *cfg
		tcpCfg.Protocol = "tcp"
		kcpCfg := *cfg
		kcpCfg.Protocol = "kcp"
		var tcpProxies, kcpProxies []config.ProxyConfig
		for _, p := range cfg.Proxies {
			if p.Type == "punch" {
				kcpProxies = append(kcpProxies, p)
				continue
			}
			tcpProxies = append(tcpProxies, p)
			kcpProxies = append(kcpProxies, p)
		}
		tcpCfg.Proxies = tcpProxies
		kcpCfg.Proxies = kcpProxies
		tcpCl := client.New(&tcpCfg, *cfgPath)
		kcpCl := client.New(&kcpCfg, *cfgPath)
		kcpCl.Log = tcpCl.Log // 共享日志实例：KCP 腿的连接尝试进同一个 lumoc.log
		legs = append(legs, tcpCl, kcpCl)
		go func() { _ = tcpCl.Run(ctxBg()) }()
		// KCP 腿等 TCP 腿完成首次登录（确认服务器支持双归属）后启动；
		// 旧服务端不支持时保持单路 TCP（KCP 注册会被旧服务端拒绝）。
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for ctxBg().Err() == nil {
				if tcpCl.LoggedIn() || time.Now().After(deadline) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if ctxBg().Err() != nil {
				return
			}
			if tcpCl.LoggedIn() {
				_ = kcpCl.Run(ctxBg())
			} else {
				fmt.Fprintln(os.Stderr, "lumoc engine: server unreachable within 30s; KCP leg not started")
			}
		}()
	} else {
		cl := client.New(cfg, *cfgPath)
		legs = append(legs, cl)
		go func() { _ = cl.Run(ctxBg()) }()
	}

	// status 聚合：多腿按代理名合并（跨传输计数器相加），任一腿在线即算在线
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		running := false
		var errs []string
		byName := map[string]*client.ProxyStatus{}
		order := make([]string, 0, len(legs)*4)
		for i, l := range legs {
			r, lastErr, proxies := l.State()
			running = running || r
			tag := legTag(cfg.Protocol, i)
			if !r && lastErr != "" {
				errs = append(errs, tag+": "+lastErr)
			}
			for _, p := range proxies {
				if m, ok := byName[p.Name]; ok {
					m.In += p.In
					m.Out += p.Out
					if p.Status == "online" || m.Status != "online" {
						m.Status = p.Status
					}
					if m.Err == "" {
						m.Err = p.Err
					}
				} else {
					np := p
					byName[p.Name] = &np
					order = append(order, p.Name)
				}
			}
		}
		out := make([]client.ProxyStatus, 0, len(order))
		for _, n := range order {
			out = append(out, *byName[n])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running": running,
			"error":   strings.Join(errs, " / "),
			"proxies": out,
		})
	})
	ln, err := net.Listen("tcp", *admin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc engine: admin endpoint:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		engineStop() // 级联取消所有腿
	}()
	<-ctx.Done()
	time.Sleep(500 * time.Millisecond) // 给腿一点时间优雅收尾
	_ = srv.Close()
}

// legTag 返回某条腿在状态错误里的显示名。
func legTag(protocol string, i int) string {
	if protocol != "auto" {
		return "tunnel"
	}
	if i == 0 {
		return "tcp"
	}
	return "kcp"
}

// ctxBg 返回进程级根 context：所有腿共享，随信号一起取消。
var engineCtx, engineStop = context.WithCancel(context.Background())

func ctxBg() context.Context { return engineCtx }
