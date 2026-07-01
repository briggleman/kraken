// Package secrets provides authenticated encryption (AES-256-GCM) for secrets
// stored at rest in the database — API keys/tokens and private keys the Panel
// must be able to decrypt to use. The master key lives outside the DB (env or the
// local config file). Values carry a version marker so legacy plaintext rows are
// detected and transparently passed through (and re-encrypted on next write).
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const stringPrefix = "enc:v1:" // marks an encrypted string value

var bytePrefix = []byte("ENC1") // marks an encrypted byte blob (PEM never starts with this)

// Cipher seals/opens secrets with a single AES-256-GCM key.
type Cipher struct{ aead cipher.AEAD }

// New returns a Cipher for a 32-byte (AES-256) key.
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) seal(plain []byte) []byte {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic("secrets: rand: " + err.Error()) // crypto/rand failure is unrecoverable
	}
	return c.aead.Seal(nonce, nonce, plain, nil) // output: nonce || ciphertext
}

func (c *Cipher) open(data []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("secrets: ciphertext too short")
	}
	return c.aead.Open(nil, data[:ns], data[ns:], nil)
}

// EncryptString returns "" unchanged, else a prefixed base64 ciphertext.
func (c *Cipher) EncryptString(s string) string {
	if s == "" {
		return ""
	}
	return stringPrefix + base64.StdEncoding.EncodeToString(c.seal([]byte(s)))
}

// DecryptString reverses EncryptString. A value without the marker is treated as
// legacy plaintext and returned unchanged; a corrupt/undecryptable value yields "".
func (c *Cipher) DecryptString(s string) string {
	if !strings.HasPrefix(s, stringPrefix) {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, stringPrefix))
	if err != nil {
		return ""
	}
	pt, err := c.open(raw)
	if err != nil {
		return ""
	}
	return string(pt)
}

// EncryptBytes seals a byte blob (e.g. a PEM key) with a magic header.
func (c *Cipher) EncryptBytes(plain []byte) []byte {
	if len(plain) == 0 {
		return plain
	}
	return append(append([]byte(nil), bytePrefix...), c.seal(plain)...)
}

// DecryptBytes reverses EncryptBytes; a blob without the header is legacy plaintext.
func (c *Cipher) DecryptBytes(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, bytePrefix) {
		return data, nil
	}
	return c.open(data[len(bytePrefix):])
}
