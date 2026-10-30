// Package auth provides the in-memory session store and login rate
// limiter shared by the server and client consoles.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// detachGrace is how long a detached session (desktop window closed, page
// unloading) stays valid. Within the grace window any request re-attaches
// the session — that is what makes a page refresh seamless; once the grace
// passes with no activity the session is dead, which is what makes closing
// the window a logout.
const detachGrace = 10 * time.Second

type sessionEntry struct {
	exp      time.Time
	detached time.Time // zero while the session is attached
}

// Sessions is a tiny in-memory cookie session store with a sliding TTL:
// every validated request pushes the expiry out again.
type Sessions struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]*sessionEntry
}

// NewSessions creates a store whose sessions expire ttl after the last
// validated request.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{ttl: ttl, m: map[string]*sessionEntry{}}
}

// Issue creates a new session token.
func (s *Sessions) Issue() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = &sessionEntry{exp: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return tok
}

// Valid reports whether the token exists and has not expired; expired
// entries are dropped lazily. A detached session (window closed) is only
// valid inside the detach grace window, and any validated request
// re-attaches it — activity keeps the session alive.
func (s *Sessions) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[tok]
	if !ok {
		return false
	}
	now := time.Now()
	if now.After(e.exp) {
		delete(s.m, tok)
		return false
	}
	if !e.detached.IsZero() {
		if now.Sub(e.detached) > detachGrace {
			delete(s.m, tok)
			return false
		}
		e.detached = time.Time{}
	}
	e.exp = now.Add(s.ttl)
	return true
}

// Detach marks the session as detached (its window is going away). The
// session stays valid only for the grace window unless activity re-attaches
// it, so closing a console window ends the login shortly after.
func (s *Sessions) Detach(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[tok]; ok {
		e.detached = time.Now()
	}
}

// DetachedAt reports when the session was detached (zero while attached or
// unknown). Test/introspection helper.
func (s *Sessions) DetachedAt(tok string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[tok]; ok {
		return e.detached
	}
	return time.Time{}
}

// Revoke removes one session.
func (s *Sessions) Revoke(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

const limiterMax = 5

// Limiter locks an origin out after too many failed logins.
type Limiter struct {
	mu   sync.Mutex
	m    map[string]*attempt
	lock time.Duration
}

type attempt struct {
	fails       int
	lockedUntil time.Time
}

// NewLimiter locks an origin for lock after limiterMax consecutive failures.
func NewLimiter(lock time.Duration) *Limiter {
	return &Limiter{m: map[string]*attempt{}, lock: lock}
}

// Locked reports whether the origin is currently locked out.
func (l *Limiter) Locked(origin string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.m[origin]
	return ok && time.Now().Before(a.lockedUntil)
}

// Fail records one failed attempt, engaging the lock at the threshold.
func (l *Limiter) Fail(origin string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[origin]
	if a == nil {
		a = &attempt{}
		l.m[origin] = a
	}
	a.fails++
	if a.fails >= limiterMax {
		a.lockedUntil = time.Now().Add(l.lock)
		a.fails = 0
	}
}

// Reset clears the failure counter after a successful login.
func (l *Limiter) Reset(origin string) {
	l.mu.Lock()
	delete(l.m, origin)
	l.mu.Unlock()
}
