// Package auth handles password hashing and session tokens for built-in accounts.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. Tuned for an interactive login on commodity hardware;
// adjust memory/time upward as hardware allows.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrMismatchedHash is returned when a password does not match the stored hash.
var ErrMismatchedHash = errors.New("auth: password does not match")

// HashPassword derives an argon2id hash and returns it in the standard PHC
// string format ($argon2id$v=19$m=...,t=...,p=...$salt$hash), which is
// self-describing and safe to store directly.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword reports whether password matches the PHC-encoded argon2id hash.
// It returns ErrMismatchedHash on a non-match and a different error if the
// encoded hash is malformed.
func VerifyPassword(password, encoded string) error {
	mem, time, threads, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, time, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatchedHash
	}
	return nil
}

func decodeHash(encoded string) (mem uint32, time uint32, threads uint8, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, errors.New("auth: invalid argon2id hash format")
	}
	var version int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("auth: parse version: %w", err)
	}
	if version != argon2.Version {
		return 0, 0, 0, nil, nil, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &time, &threads); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("auth: parse params: %w", err)
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("auth: decode salt: %w", err)
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("auth: decode hash: %w", err)
	}
	return mem, time, threads, salt, hash, nil
}
