package server

import "testing"

// Older clients register their secret-key tunnels under a type name the
// server no longer knows. The server must still accept them by capability,
// otherwise every already-deployed client silently loses its tunnels.
func TestNormalizeKindAcceptsLegacySecretTunnel(t *testing.T) {
	legacy := []string{"sealed-tcp", "sealed-udp", "punch", "sealed-tcp", "some-future-name"}
	for _, kind := range legacy {
		got, ok := normalizeKind(kind, "a-secret-key", 0)
		if !ok {
			t.Fatalf("kind %q rejected, want accepted", kind)
		}
		if got != "sealed-tcp" {
			t.Fatalf("kind %q normalized to %q, want sealed-tcp", kind, got)
		}
	}
}

// A proxy that claims a public port must be one of the known kinds; guessing
// there would silently expose services the operator never asked for.
func TestNormalizeKindRejectsPublicPortWithoutKnownKind(t *testing.T) {
	if _, ok := normalizeKind("mystery", "a-secret-key", 16066); ok {
		t.Fatal("kind with a public port was accepted")
	}
	if _, ok := normalizeKind("mystery", "", 0); ok {
		t.Fatal("kind without a secret key was accepted")
	}
}
