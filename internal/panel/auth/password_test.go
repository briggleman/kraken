package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("abyss-key")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash is not PHC argon2id format: %q", hash)
	}
	if err := VerifyPassword("abyss-key", hash); err != nil {
		t.Fatalf("VerifyPassword (correct) = %v, want nil", err)
	}
	if err := VerifyPassword("wrong-key", hash); err != ErrMismatchedHash {
		t.Fatalf("VerifyPassword (wrong) = %v, want ErrMismatchedHash", err)
	}
}

func TestHashUniqueness(t *testing.T) {
	// Same password must produce different hashes (random salt).
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password should differ (salt not random?)")
	}
}

func TestVerifyMalformed(t *testing.T) {
	if err := VerifyPassword("x", "not-a-valid-hash"); err == nil {
		t.Fatal("expected error for malformed hash, got nil")
	}
}

func TestNewSessionToken(t *testing.T) {
	a, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	b, _ := NewSessionToken()
	if a == b {
		t.Fatal("session tokens should be unique")
	}
	if len(a) < 40 {
		t.Fatalf("session token looks too short: %d chars", len(a))
	}
}
