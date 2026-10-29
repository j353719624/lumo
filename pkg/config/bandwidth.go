package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBandwidth converts a human-readable rate ("1MB", "500KB", "2M", "1048576")
// into bytes per second. An empty string means unlimited (0).
func ParseBandwidth(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, suffix := range []struct {
		suf string
		m   int64
	}{{"gb", 1 << 30}, {"g", 1 << 30}, {"mb", 1 << 20}, {"m", 1 << 20}, {"kb", 1 << 10}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, suffix.suf) {
			mult = suffix.m
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix.suf))
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid bandwidth %q (use e.g. 1MB or 500KB)", s)
	}
	return int64(v * float64(mult)), nil
}
