package server

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/proto"
)

// 双归属回归：auto 协议下同一映射由 TCP 与 KCP 两条会话分别注册，
// 服务端要双归持有；任一传输断开只摘除其归属，两条都断才整体拆除。

func dualHomeServer(t *testing.T) *Server {
	t.Helper()
	s := New(&config.ServerConfig{ConfigPath: ""})
	return s
}

// fakeSession 造一个假会话；返回的 peer 是管道另一端，测试可读出
// 服务端写来的消息（不读时须自行排水，否则 reply 的管道写会阻塞）。
func fakeSession(s *Server, id, transport string) (*session, net.Conn) {
	c1, c0 := net.Pipe()
	ses := &session{
		srv:       s,
		id:        id,
		transport: transport,
		conn:      c1,
		names:     map[string]*proxyEnt{},
		pendReg:   map[string]chan proto.Msg{},
	}
	return ses, c0
}

func regMsg(name string) proto.Msg {
	return proto.Msg{"type": "reg", "name": name, "kind": "tcp", "remote_port": 15900}
}

func TestDualHomeRegistration(t *testing.T) {
	s := dualHomeServer(t)
	s1, p1 := fakeSession(s, "cli-tcp", "tcp")
	go io.Copy(io.Discard, p1) // 排水：reg_ok 写入不阻塞
	s2, p2 := fakeSession(s, "cli-kcp", "kcp")
	go io.Copy(io.Discard, p2)

	s.handleReg(s1, regMsg("web"))
	ent, ok := s.proxies["web"]
	if !ok {
		t.Fatal("first registration missing")
	}
	if len(ent.owners) != 1 || ent.owners["tcp"] != s1 {
		t.Fatalf("owners after first reg: %+v", ent.owners)
	}

	// 另一条传输注册同名映射：双归属，监听器不重建
	s.handleReg(s2, regMsg("web"))
	if len(ent.owners) != 2 || ent.owners["kcp"] != s2 {
		t.Fatalf("owners after dual-home: %+v", ent.owners)
	}
	if ent.ln == nil {
		t.Fatal("public listener should stay bound across dual-home")
	}

	// 一条传输断开：只摘其归属，代理继续由另一条服务
	s1.drop("test")
	if _, alive := s.proxies["web"]; !alive {
		t.Fatal("proxy must survive while the other transport still owns it")
	}
	if ent.owners["tcp"] != nil {
		t.Fatal("dropped transport should be released")
	}

	// 两条都断：整体拆除
	s2.drop("test")
	if _, alive := s.proxies["web"]; alive {
		t.Fatal("proxy should be fully removed after both transports drop")
	}
}

func TestDualHomeRejectsAliveDup(t *testing.T) {
	s := dualHomeServer(t)
	s1, p1 := fakeSession(s, "cli-tcp", "tcp")
	go io.Copy(io.Discard, p1)
	s2, p2 := fakeSession(s, "cli-tcp-2", "tcp")
	go io.Copy(io.Discard, p2) // 排水：拒绝回复的写入不阻塞

	s.handleReg(s1, regMsg("web"))
	// 同传输活会话重复注册：拒绝
	s.handleReg(s2, regMsg("web"))
	ent := s.proxies["web"]
	if len(ent.owners) != 1 || ent.owners["tcp"] != s1 {
		t.Fatalf("same-transport dup should be rejected: %+v", ent.owners)
	}

	// 同传输死会话接管：允许
	s1.closed.Store(true)
	s.handleReg(s2, regMsg("web"))
	if ent.owners["tcp"] != s2 {
		t.Fatal("dead session should be taken over by the new one")
	}
}

func TestDualHomeRejectsConfigConflict(t *testing.T) {
	s := dualHomeServer(t)
	s1, p1 := fakeSession(s, "cli-tcp", "tcp")
	go io.Copy(io.Discard, p1)
	s2, p2 := fakeSession(s, "cli-kcp", "kcp")
	go io.Copy(io.Discard, p2) // 排水：拒绝回复的写入不阻塞

	s.handleReg(s1, regMsg("web"))
	// 端口冲突的双归：拒绝
	m := regMsg("web")
	m["remote_port"] = 19999
	s.handleReg(s2, m)
	ent := s.proxies["web"]
	if len(ent.owners) != 1 {
		t.Fatalf("conflicting dual-home should be rejected: owners=%+v", ent.owners)
	}
}

func TestGetWorkFailsOverToSecondary(t *testing.T) {
	s := dualHomeServer(t)
	s1, p1 := fakeSession(s, "cli-tcp", "tcp") // 主传输：置死（reg_ok 需排水）
	go io.Copy(io.Discard, p1)
	s2, p2 := fakeSession(s, "cli-kcp", "kcp")
	// 读循环要在 handleReg(s2) 的 reg_ok 写入前就位，否则管道写阻塞
	ch := make(chan proto.Msg, 8)
	go func() {
		for {
			m, err := proto.Recv(p2)
			if err != nil {
				return
			}
			ch <- m
		}
	}()
	s.handleReg(s1, regMsg("web"))
	s.handleReg(s2, regMsg("web"))
	ent := s.proxies["web"]

	// 主传输（tcp）死：getWork 必须把 work 请求发到 kcp 会话。测试里没有
	// 客户端回连配对，getWork 以超时告终——超时本身即证明请求已送达备用。
	s1.closed.Store(true)
	_, err := s.getWork(ent, 300*time.Millisecond)
	if err == nil {
		t.Fatal("getWork without pairing should time out")
	}
	// 消息流里必须出现过发往 web 的 work 请求
	sawWork := false
	for {
		select {
		case m := <-ch:
			if m.Str("type") == "work" && m.Str("name") == "web" {
				sawWork = true
			}
			continue
		default:
		}
		break
	}
	if !sawWork {
		t.Fatal("work request never arrived on the secondary session")
	}
	// 失败计数应已把首选切到 kcp
	if ent.pref != "kcp" {
		t.Fatalf("preference should have flipped to kcp, got %q", ent.pref)
	}
}
