package log

import "testing"

func TestMaskAddr(t *testing.T) {
	a := MaskAddr("47.100.29.31:5666")
	b := MaskAddr("47.100.29.31:5666")
	if a != b || len(a) != len("ip-12345678:5666") {
		t.Fatalf("mask unstable or wrong shape: %q", a)
	}
	if a == "47.100.29.31:5666" {
		t.Fatal("raw IP leaked")
	}
	if MaskAddr("47.100.29.31") == "47.100.29.31" {
		t.Fatal("bare IP leaked")
	}
	if MaskAddr("nas.example.com:7000") != "nas.example.com:7000" {
		t.Fatal("domain must pass through")
	}
	if MaskAddr("47.100.29.31:5666") == MaskAddr("1.2.3.4:5666") {
		t.Fatal("different IPs must map to different tags")
	}
}

func TestStripAddrs(t *testing.T) {
	got := StripAddrs("read: read tcp 172.24.51.21:58619->112.22.108.5:5268: i/o timeout")
	for _, leak := range []string{"172.24", "112.22", "58619->"} {
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
