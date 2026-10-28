package log

import (
	"fmt"
	"sync"
	"time"
)

// Instance is a scoped logger: entries go to the shared stderr output and
// the global ring buffer, plus a per-instance ring used by component
// dashboards (e.g. one tunnel connection).
type Instance struct {
	prefix string
	mu     sync.Mutex
	ring   []Entry
	pos    int
}

// NewInstance creates a scoped logger whose entries carry prefix.
func NewInstance(prefix string) *Instance {
	return &Instance{prefix: prefix, ring: make([]Entry, 128)}
}

func (l *Instance) Debugf(f string, a ...any) { l.logf(LevelDebug, f, a...) }
func (l *Instance) Infof(f string, a ...any)  { l.logf(LevelInfo, f, a...) }
func (l *Instance) Warnf(f string, a ...any)  { l.logf(LevelWarn, f, a...) }
func (l *Instance) Errorf(f string, a ...any) { l.logf(LevelError, f, a...) }

func (l *Instance) logf(lv Level, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	logf(lv, "%s %s", l.prefix, msg) // shared sink: stderr + global ring
	if lv < LevelInfo {
		return
	}
	l.mu.Lock()
	l.ring[l.pos] = Entry{Time: time.Now(), Level: levelName(lv), Msg: msg}
	l.pos = (l.pos + 1) % len(l.ring)
	l.mu.Unlock()
}

// Recent returns up to limit entries, oldest first.
func (l *Instance) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, len(l.ring))
	for i := 0; i < len(l.ring); i++ {
		e := l.ring[(l.pos+i)%len(l.ring)]
		if e.Time.IsZero() {
			continue
		}
		out = append(out, e)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func levelName(l Level) string {
	switch l {
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}
