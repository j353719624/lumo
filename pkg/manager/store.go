// Package manager hosts the lumoc console: a persistent store of tunnel
// connection configs plus an engine that runs each enabled connection
// in-process and reports live status.
package manager

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/j353719624/lumo/pkg/config"
)

// Mapping is one port forwarding rule inside a connection config.
type Mapping struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
	Type       string `json:"type"`
	LocalIP    string `json:"localIP,omitempty"`
	LocalPort  int    `json:"localPort"`
	RemotePort int    `json:"remotePort,omitempty"`
	Domain     string `json:"domain,omitempty"`
}

func (m Mapping) active() bool { return m.Enabled == nil || *m.Enabled }

// ConnConfig is one stored tunnel connection.
type ConnConfig struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	ServerAddr    string    `json:"serverAddr"`
	ServerPort    int       `json:"serverPort"`
	Token         string    `json:"token"`
	Protocol      string    `json:"protocol,omitempty"`
	KCPPort       int       `json:"kcpPort,omitempty"`
	TLS           bool      `json:"tls"`
	Enabled       bool      `json:"enabled"`
	AutoReconnect bool      `json:"autoReconnect,omitempty"`
	AutoStart     bool      `json:"autoStart,omitempty"`
	Mappings      []Mapping `json:"mappings"`
}

// toClientConfig converts the stored form into engine configuration.
func (c *ConnConfig) toClientConfig() (*config.ClientConfig, error) {
	out := &config.ClientConfig{
		Name:       c.Name,
		ServerAddr: c.ServerAddr,
		ServerPort: c.ServerPort,
		Protocol:   c.Protocol,
		KCPPort:    c.KCPPort,
		AuthToken:  c.Token,
		TLSEnable:  c.TLS,
	}
	safe := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
				return r
			}
			return '-'
		}, s)
	}
	for _, m := range c.Mappings {
		if !m.active() {
			continue
		}
		p := config.ProxyConfig{
			Name:       safe(c.ID + "-" + m.ID),
			Label:      m.Name,
			Type:       m.Type,
			LocalIP:    cmpOr(m.LocalIP, "127.0.0.1"),
			LocalPort:  m.LocalPort,
			RemotePort: m.RemotePort,
		}
		if m.Type == "http" || m.Type == "https" {
			p.CustomDomains = []string{m.Domain}
		}
		out.Proxies = append(out.Proxies, p)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Store persists connection configs as a JSON document.
type Store struct {
	mu   sync.Mutex
	path string
	list []*ConnConfig
}

// LoadStore reads the store file; a missing file yields an empty store.
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := jsonUnmarshal(raw, &s.list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// List returns a copy of all stored configs.
func (s *Store) List() []ConnConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ConnConfig, 0, len(s.list))
	for _, c := range s.list {
		out = append(out, *c)
	}
	return out
}

// Get returns a copy of one config.
func (s *Store) Get(id string) (ConnConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.list {
		if c.ID == id {
			return *c, true
		}
	}
	return ConnConfig{}, false
}

// Upsert inserts or replaces a config and persists.
func (s *Store) Upsert(c *ConnConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, existing := range s.list {
		if existing.ID == c.ID {
			s.list[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		s.list = append(s.list, c)
	}
	return s.saveLocked()
}

// Delete removes a config and persists.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.list[:0]
	for _, c := range s.list {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	s.list = kept
	return s.saveLocked()
}

// saveLocked writes the store atomically. Callers hold mu.
func (s *Store) saveLocked() error {
	b, err := jsonMarshalIndent(s.list)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
