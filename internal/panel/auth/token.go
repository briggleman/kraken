package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// NewSessionToken returns a cryptographically random, URL-safe opaque token
// used to identify an authenticated session. Tokens carry no embedded data;
// the server maps them to a session in its session store.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
