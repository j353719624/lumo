package log

import "time"

type Entry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

const ringSize = 512

var ring [ringSize]Entry
var ringPos int

// record stores the entry in the in-memory ring buffer for the dashboard
// API. Called with mu held.
func record(l Level, msg string) {
	if l < LevelInfo {
		// Debug noise is skipped; INFO and above feed the dashboard.
		return
	}
	tag := "INFO"
	switch l {
	case LevelWarn:
		tag = "WARN"
	case LevelError:
		tag = "ERROR"
	}
	ring[ringPos] = Entry{Time: time.Now(), Level: tag, Msg: msg}
	ringPos = (ringPos + 1) % ringSize
}

// Recent returns up to limit buffered entries, newest last, optionally
// filtered by level ("ALL", "INFO", "WARN", "ERROR"). INFO includes INFO
// and above.
func Recent(level string, limit int) []Entry {
	if limit <= 0 || limit > ringSize {
		limit = ringSize
	}
	out := make([]Entry, 0, limit)
	for i := 0; i < ringSize; i++ {
		e := ring[(ringPos+i)%ringSize]
		if e.Time.IsZero() {
			continue
		}
		switch level {
		case "WARN":
			if e.Level != "WARN" && e.Level != "ERROR" {
				continue
			}
		case "ERROR":
			if e.Level != "ERROR" {
				continue
			}
		default:
			// ALL / INFO: everything recorded.
		}
		out = append(out, e)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
