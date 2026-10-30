package log

import "testing"

// 样例地址一律使用 RFC 5737 文档保留段，避免把真实部署地址写进仓库。
const sampleIP = "203.0.113.10"
const otherIP = "198.51.100.7"

func TestMaskAddr(t *testing.T) {
	a := MaskAddr(sampleIP + ":5666")
	b := MaskAddr(sampleIP + ":5666")
	if a != b || len(a) != len("ip-12345678:5666") {
		t.Fatalf("mask unstable or wrong shape: %q", a)
	}
	if a == sampleIP+":5666" {
		t.Fatal("raw IP leaked")
	}
	if MaskAddr(sampleIP) == sampleIP {
		t.Fatal("bare IP leaked")
	}
	if MaskAddr("nas.example.com:7000") != "nas.example.com:7000" {
		t.Fatal("domain must pass through")
	}
	if MaskAddr(sampleIP+":5666") == MaskAddr(otherIP+":5666") {
		t.Fatal("different IPs must map to different tags")
	}
}

func TestStripAddrs(t *testing.T) {
	got := StripAddrs("read: read tcp 192.0.2.20:58619->" + sampleIP + ":5268: i/o timeout")
	for _, leak := range []string{"192.0.2", "203.0.113", "58619->"} {
		if got != "" && contains(got, leak) {
			t.Fatalf("address leaked in %q", got)
		}
	}
	if got == "" {
		t.Fatal("reason must not be emptied")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
