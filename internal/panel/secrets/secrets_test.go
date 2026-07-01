package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func key(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestStringRoundTrip(t *testing.T) {
	c, err := New(key(1))
	if err != nil {
		t.Fatal(err)
	}
	ct := c.EncryptString("cf-token-secret")
	if ct == "cf-token-secret" || !strings.HasPrefix(ct, stringPrefix) {
		t.Fatalf("expected prefixed ciphertext, got %q", ct)
	}
	if got := c.DecryptString(ct); got != "cf-token-secret" {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestStringEmptyAndLegacyPassthrough(t *testing.T) {
	c, _ := New(key(2))
	if c.EncryptString("") != "" {
		t.Fatal("empty should encrypt to empty")
	}
	// A value without the marker is legacy plaintext, returned unchanged.
	if got := c.DecryptString("legacy-plaintext"); got != "legacy-plaintext" {
		t.Fatalf("legacy passthrough failed: %q", got)
	}
}

func TestBytesRoundTripAndLegacy(t *testing.T) {
	c, _ := New(key(3))
	pem := []byte("-----BEGIN EC PRIVATE KEY-----\nabc\n-----END EC PRIVATE KEY-----\n")
	ct := c.EncryptBytes(pem)
	if bytes.Equal(ct, pem) {
		t.Fatal("ciphertext must differ from plaintext")
	}
	got, err := c.DecryptBytes(ct)
	if err != nil || !bytes.Equal(got, pem) {
		t.Fatalf("bytes round-trip failed: %v", err)
	}
	// Legacy plaintext PEM (no header) passes through.
	if back, err := c.DecryptBytes(pem); err != nil || !bytes.Equal(back, pem) {
		t.Fatalf("legacy bytes passthrough failed: %v", err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	a, _ := New(key(4))
	b, _ := New(key(5))
	ct := a.EncryptString("secret")
	if got := b.DecryptString(ct); got != "" {
		t.Fatalf("decrypt with wrong key should fail (empty), got %q", got)
	}
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}
