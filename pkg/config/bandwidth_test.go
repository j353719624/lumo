package config

import "testing"

func TestParseBandwidth(t *testing.T) {
	cases := map[string]int64{
		"":      0,
		"1MB":   1 << 20,
		"500KB": 500 << 10,
		"2m":    2 << 20,
		"1.5MB": 1572864,
		"1024":  1024,
		"1gb":   1 << 30,
	}
	for in, want := range cases {
		got, err := ParseBandwidth(in)
		if err != nil {
			t.Fatalf("ParseBandwidth(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseBandwidth(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseBandwidth("abc"); err == nil {
		t.Fatal("expected error for invalid input")
	}
}
