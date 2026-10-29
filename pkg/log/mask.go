// Address masking for logs: raw IPs never reach any log output — they are
// replaced by a deterministic short digest (same address → same tag).
package log

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
)

// MaskAddr rewrites an "host:port" (or bare host) address so that an IP
// host shows up as "ip-<8 hex chars>[:port]". Non-IP hosts (domains) pass
// through unchanged.
func MaskAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, ""
	}
	if net.ParseIP(host) == nil {
		return addr
	}
	sum := sha256.Sum256([]byte("lumo:" + host))
	tag := "ip-" + hex.EncodeToString(sum[:])[:8]
	if port == "" {
		return tag
	}
	return tag + ":" + port
}

// StripAddrs removes transport endpoints from error strings before they are
// logged ("read tcp 1.2.3.4:5->6.7.8.9:10: timeout" → "tcp: timeout") and
// masks any address that survives.
func StripAddrs(reason string) string {
	if reason == "" {
		return ""
	}
	// "read tcp <local>-><remote>: i/o timeout" → "tcp: i/o timeout"
	for _, pat := range []string{"tcp ", "udp "} {
		if i := strings.Index(reason, pat); i >= 0 {
			if j := strings.Index(reason[i:], ": "); j >= 0 {
				head := reason[:i] + strings.TrimSuffix(pat, " ") + ":"
				tail := reason[i+j+1:]
				reason = head + " " + StripAddrs(tail)
				return strings.TrimSpace(reason)
			}
		}
	}
	return MaskAddr(reason)
}
