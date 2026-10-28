package toml

import (
	"testing"
)

func TestParseTablesAndValues(t *testing.T) {
	src := `
# full line comment
bindPort = 7000   # trailing comment
enabled = true
ratio = 1.5

[auth]
token = "s3cret"

[webServer]
addr = "127.0.0.1"
port = 7500

[[proxies]]
name = "a"
domains = ["a.example.com", "b.example.com"]

[[proxies]]
name = "b"
multi = [
  "x",   # comment inside array
  "y",
]
`
	m, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := m["bindPort"].(int64); got != 7000 {
		t.Errorf("bindPort = %v", got)
	}
	if m["enabled"].(bool) != true {
		t.Error("enabled")
	}
	if m["ratio"].(float64) != 1.5 {
		t.Error("ratio")
	}
	auth := m["auth"].(map[string]any)
	if auth["token"].(string) != "s3cret" {
		t.Error("auth token")
	}
	web := m["webServer"].(map[string]any)
	if web["addr"].(string) != "127.0.0.1" || web["port"].(int64) != 7500 {
		t.Error("webServer table")
	}
	proxies := m["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("proxies len = %d", len(proxies))
	}
	p0 := proxies[0].(map[string]any)
	if p0["name"].(string) != "a" {
		t.Error("p0 name")
	}
	domains := p0["domains"].([]any)
	if len(domains) != 2 || domains[1].(string) != "b.example.com" {
		t.Errorf("domains = %v", domains)
	}
	p1 := proxies[1].(map[string]any)
	multi := p1["multi"].([]any)
	if len(multi) != 2 || multi[0].(string) != "x" || multi[1].(string) != "y" {
		t.Errorf("multi = %v", multi)
	}
}

func TestParseDottedKey(t *testing.T) {
	m, err := Parse("auth.token = \"abc\"\n")
	if err != nil {
		t.Fatal(err)
	}
	auth := m["auth"].(map[string]any)
	if auth["token"].(string) != "abc" {
		t.Errorf("got %v", auth)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"justakey\n",
		"[unclosed\n",
		"x = \"unterminated\n",
		"x = [1, 2\n",
		"x = weird$\n",
	}
	for _, src := range cases {
		if _, err := Parse(src); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}
