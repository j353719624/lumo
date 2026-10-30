package auth

import (
	"testing"
	"time"
)

// 会话是滑动的：每次校验都把过期时间往后推。
func TestSessionsSlideOnActivity(t *testing.T) {
	s := NewSessions(50 * time.Millisecond)
	tok := s.Issue()
	time.Sleep(30 * time.Millisecond)
	if !s.Valid(tok) {
		t.Fatal("session should still be valid")
	}
	time.Sleep(30 * time.Millisecond)
	if !s.Valid(tok) {
		t.Fatal("session should have been renewed by the last Valid")
	}
	time.Sleep(80 * time.Millisecond)
	if s.Valid(tok) {
		t.Fatal("session should expire 50ms after the last activity")
	}
}

// Detach 之后宽限期内仍可用，且一次活动即重新挂载；宽限期外直接失效。
func TestSessionsDetachGrace(t *testing.T) {
	s := NewSessions(time.Hour)
	tok := s.Issue()
	s.Detach(tok)
	if !s.Valid(tok) {
		t.Fatal("detached session stays valid inside the grace window")
	}
	if !s.Valid(tok) {
		t.Fatal("activity re-attaches a detached session")
	}

	s.Detach(tok)
	// 直接把 detached 时间拨回宽限之前，模拟时间流逝
	s.mu.Lock()
	s.m[tok].detached = time.Now().Add(-detachGrace - time.Second)
	s.mu.Unlock()
	if s.Valid(tok) {
		t.Fatal("detached session past the grace window must be dead")
	}
}

func TestSessionsRevoke(t *testing.T) {
	s := NewSessions(time.Hour)
	tok := s.Issue()
	s.Revoke(tok)
	if s.Valid(tok) {
		t.Fatal("revoked session must not validate")
	}
}
