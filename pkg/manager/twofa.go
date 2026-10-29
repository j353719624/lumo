package manager

// Console two-factor authentication for the client console. Fully separate
// from the server console: its own secret file, its own login tickets and
// its own authenticator entry.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/j353719624/lumo/pkg/totp"
)

type consoleTwoFA struct {
	// Secrets holds one base32 secret per bound authenticator; a code from
	// any of them opens the console. Legacy files with a single "secret"
	// field migrate transparently on load.
	Secrets []string `json:"secrets"`
}

const twoFAFile = "console-totp.json"

var (
	twofaMu       sync.Mutex
	twofaSecrets  []string
	twoFATickets  = map[string]time.Time{}
	twoFATicketT  = 2 * time.Minute
)

func (m *Manager) totpPath() string {
	dir := filepath.Dir(m.store.path)
	return filepath.Join(dir, twoFAFile)
}

// loadTwoFA reads the bound authenticator secrets at startup.
func (m *Manager) loadTwoFA() {
	raw, err := os.ReadFile(m.totpPath())
	if err != nil {
		return
	}
	var saved consoleTwoFA
	if err := json.Unmarshal(raw, &saved); err != nil {
		return
	}
	twofaMu.Lock()
	twofaSecrets = saved.Secrets
	twofaMu.Unlock()
}

// saveTwoFA persists the bound authenticator list.
func (m *Manager) saveTwoFA(secrets []string) {
	twofaMu.Lock()
	twofaSecrets = secrets
	twofaMu.Unlock()
	data, _ := json.MarshalIndent(consoleTwoFA{Secrets: secrets}, "", "  ")
	if data != nil {
		_ = os.WriteFile(m.totpPath(), data, 0o600)
	}
}

// twoFAEnabled reports whether the console login requires a TOTP code.
func (m *Manager) twoFAEnabled() bool {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	return len(twofaSecrets) > 0
}

// twoFAAny reports whether the code is valid for any bound authenticator.
func (m *Manager) twoFAAny(code string) bool {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	for _, sec := range twofaSecrets {
		if totp.Verify(sec, code, time.Now()) {
			return true
		}
	}
	return false
}

// twoFACount returns the number of bound authenticators.
func (m *Manager) twoFACount() int {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	return len(twofaSecrets)
}

// twoFARemoveAt drops the binding at index.
func (m *Manager) twoFARemoveAt(i int) {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	if i < 0 || i >= len(twofaSecrets) {
		return
	}
	twofaSecrets = append(twofaSecrets[:i], twofaSecrets[i+1:]...)
}

func issueLoginTicket() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(buf)
	twofaMu.Lock()
	now := time.Now()
	for k, exp := range twoFATickets {
		if exp.Before(now) {
			delete(twoFATickets, k)
		}
	}
	twoFATickets[ticket] = now.Add(twoFATicketT)
	twofaMu.Unlock()
	return ticket, nil
}

func consumeLoginTicket(ticket string) bool {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	exp, ok := twoFATickets[ticket]
	if !ok || exp.Before(time.Now()) {
		delete(twoFATickets, ticket)
		return false
	}
	delete(twoFATickets, ticket)
	return true
}
