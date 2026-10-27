// Package auth provides the in-memory session store and login rate
// limiter shared by the server and client consoles.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Sessions is a tiny in-memory cookie session store.
type Sessions struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

// NewSessions creates a store whose sessions expire after ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{ttl: ttl, m: map[string]time.Time{}}
}

// Issue creates a new session token.
func (s *Sessions) Issue() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return tok
}

// Valid reports whether the token exists and has not expired; expired
// entries are dropped lazily.
func (s *Sessions) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	return true
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
