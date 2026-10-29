package manager

import (
	"strings"
	"testing"
)

func TestRenderTomlCarriesMappingLabels(t *testing.T) {
	out := renderToml(ConnConfig{
		ID:         "abc12345",
		Name:       "飞牛NAS",
		ServerAddr: "example.com",
		ServerPort: 7000,
		Token:      "tok",
		Protocol:   "kcp",
		KCPPort:    47011,
		Mappings: []Mapping{
			{ID: "web", Name: "飞牛系统", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 5666, RemotePort: 16066},
			{ID: "db", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 5432, RemotePort: 15432},
		},
	})
	for _, want := range []string{
		`name = "飞牛NAS"`,
		`protocol = "kcp"`,
		"kcpPort = 47011",
		`label = "飞牛系统"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered toml missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, `label = `) != 1 {
		t.Fatalf("only named mappings should emit a label:\n%s", out)
	}
}
