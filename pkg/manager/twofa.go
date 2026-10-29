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
	Secret string `json:"secret"`
}

const twoFAFile = "console-totp.json"

var (
	twofaMu      sync.Mutex
	twofaSecret  string
	twoFATickets = map[string]time.Time{}
	twoFATicketT = 2 * time.Minute
)

func (m *Manager) totpPath() string {
	dir := filepath.Dir(m.store.path)
	return filepath.Join(dir, twoFAFile)
}

// loadTwoFA reads the console TOTP secret at startup.
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
	twofaSecret = saved.Secret
	twofaMu.Unlock()
}

func (m *Manager) saveTwoFA(secret string) {
	twofaMu.Lock()
	twofaSecret = secret
	twofaMu.Unlock()
	data, _ := json.MarshalIndent(consoleTwoFA{Secret: secret}, "", "  ")
	if data != nil {
		_ = os.WriteFile(m.totpPath(), data, 0o600)
	}
}

// twoFAEnabled reports whether the console login requires a TOTP code.
func (m *Manager) twoFAEnabled() bool {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	return totp.Valid(twofaSecret)
}

func (m *Manager) twoFAVerify(code string) bool {
	twofaMu.Lock()
	defer twofaMu.Unlock()
	return totp.Verify(twofaSecret, code, time.Now())
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
