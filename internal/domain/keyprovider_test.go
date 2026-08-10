package domain

import (
	"testing"
)

func TestStaticKeyProvider(t *testing.T) {
	provider := NewStaticKeyProvider("v2", "secret-v2", map[string]string{
		"v1": "secret-v1",
	})

	keyID, secret := provider.ActiveKey()
	if keyID != "v2" || string(secret) != "secret-v2" {
		t.Fatalf("expected ActiveKey (v2, secret-v2), got (%s, %s)", keyID, string(secret))
	}

	// Lookup active key by ID
	sec, ok := provider.GetKey("v2")
	if !ok || string(sec) != "secret-v2" {
		t.Fatalf("expected active key v2, got ok=%v sec=%s", ok, string(sec))
	}

	// Lookup active key by empty ID (fallback)
	sec, ok = provider.GetKey("")
	if !ok || string(sec) != "secret-v2" {
		t.Fatalf("expected active key for empty ID, got ok=%v sec=%s", ok, string(sec))
	}

	// Lookup legacy key
	sec, ok = provider.GetKey("v1")
	if !ok || string(sec) != "secret-v1" {
		t.Fatalf("expected legacy key v1, got ok=%v sec=%s", ok, string(sec))
	}

	// Lookup non-existent key
	_, ok = provider.GetKey("v0")
	if ok {
		t.Fatalf("expected false for unknown key v0, got true")
	}
}

func TestStaticKeyProvider_EmptySecretFailClosed(t *testing.T) {
	emptyProvider := NewStaticKeyProvider("v1", "", nil)

	keyID, secret := emptyProvider.ActiveKey()
	if keyID != "" || secret != nil {
		t.Fatalf("expected empty active key for empty secret, got keyID=%s secret=%v", keyID, secret)
	}

	sec, ok := emptyProvider.GetKey("v1")
	if ok || sec != nil {
		t.Fatalf("expected fail-closed false for empty secret, got ok=%v sec=%v", ok, sec)
	}

	sec, ok = emptyProvider.GetKey("")
	if ok || sec != nil {
		t.Fatalf("expected fail-closed false for empty secret, got ok=%v sec=%v", ok, sec)
	}
}
