// Package log provides a tiny leveled logger writing to stderr.
package log

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	mu    sync.Mutex
	level = LevelInfo
	out   = os.Stderr
)

// SetLevel changes the minimum level that is printed.
func SetLevel(l Level) {
	mu.Lock()
	level = l
	mu.Unlock()
}

func logf(l Level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if l < level {
		return
	}
	tag := "INFO"
	switch l {
	case LevelDebug:
		tag = "DEBUG"
	case LevelWarn:
		tag = "WARN "
	case LevelError:
		tag = "ERROR"
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(out, "%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), tag, msg)
	if l >= LevelInfo {
		record(l, msg)
	}
}

func Debugf(format string, a ...any) { logf(LevelDebug, format, a...) }
func Infof(format string, a ...any)  { logf(LevelInfo, format, a...) }
func Warnf(format string, a ...any)  { logf(LevelWarn, format, a...) }
func Errorf(format string, a ...any) { logf(LevelError, format, a...) }
