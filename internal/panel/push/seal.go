package push

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Info is the HPKE info string every alert is sealed under. It binds the
// ciphertext to this protocol and its version, so an envelope cannot be
// replayed into some other use of the same device key.
const Info = "kraken-push-v1"

// KeySize is the length of a raw X25519 key, public or private, and of the
// encapsulated key at the front of every envelope.
const KeySize = 32

// The suite is DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20-Poly1305 in
// base mode — the one CryptoKit names HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly.
// ChaCha20-Poly1305 over AES-GCM because it is the suite both ends have in
// their standard library without a hardware caveat.
var (
	kem  = hpke.DHKEM(ecdh.X25519())
	kdf  = hpke.HKDFSHA256()
	aead = hpke.ChaCha20Poly1305()
)

// ErrBadKey is returned for a key that is not 32 bytes, or a public key that
// is one of the small-order X25519 points no honest device would ever send.
var ErrBadKey = errors.New("push: not a usable X25519 key")

// ValidatePublicKey reports whether pub is a raw X25519 public key a payload
// can be sealed to. Every 32-byte string decodes as an X25519 point, so the
// real test is the Diffie-Hellman itself: the handful of small-order points
// produce an all-zero shared secret, which Go refuses, and sealing to one would
// fail on every alert. Checking at registration turns that into one clear
// refusal instead of a device that silently never hears anything.
func ValidatePublicKey(pub []byte) error {
	if len(pub) != KeySize {
		return fmt.Errorf("%w: the public key is %d bytes, want %d", ErrBadKey, len(pub), KeySize)
	}
	pk, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	probe, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if _, err := probe.ECDH(pk); err != nil {
		return fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	return nil
}

// Seal encrypts p to the device's raw X25519 public key and returns the
// envelope the relay carries: standard base64 of enc ‖ ciphertext, where enc
// is the 32-byte encapsulated key. Encapsulation is randomized, so sealing the
// same payload twice gives two different envelopes; one seal per device per
// alert is the contract.
//
// A zero V is stamped with PayloadVersion. Any other version is refused,
// because the app treats v as the one field that can make it stop reading.
func Seal(devicePublicKey []byte, p Payload) (string, error) {
	if err := ValidatePublicKey(devicePublicKey); err != nil {
		return "", err
	}
	if p.V == 0 {
		p.V = PayloadVersion
	}
	if p.V != PayloadVersion {
		return "", fmt.Errorf("push: payload version %d, this Panel seals version %d", p.V, PayloadVersion)
	}
	plaintext, err := marshalPayload(p)
	if err != nil {
		return "", err
	}
	pk, err := kem.NewPublicKey(devicePublicKey)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	sealed, err := hpke.Seal(pk, kdf, aead, []byte(Info), plaintext)
	if err != nil {
		return "", fmt.Errorf("push: seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open is Seal's inverse: it decodes an envelope with the device's raw X25519
// private key and parses the plaintext. The Panel never holds a device's
// private key, so this exists for the tests, the stub relay and as the
// reference the companion app's own decryption is checked against.
func Open(devicePrivateKey []byte, envelope string) (Payload, error) {
	plaintext, err := OpenRaw(devicePrivateKey, envelope)
	if err != nil {
		return Payload{}, err
	}
	var p Payload
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return Payload{}, fmt.Errorf("push: the plaintext is not a payload: %w", err)
	}
	if p.V != PayloadVersion {
		return Payload{}, fmt.Errorf("push: payload version %d, this build reads version %d", p.V, PayloadVersion)
	}
	return p, nil
}

// OpenRaw decrypts an envelope and returns the plaintext bytes exactly as they
// were sealed, without parsing them — what the fixed test vector compares.
func OpenRaw(devicePrivateKey []byte, envelope string) ([]byte, error) {
	if len(devicePrivateKey) != KeySize {
		return nil, fmt.Errorf("%w: the private key is %d bytes, want %d", ErrBadKey, len(devicePrivateKey), KeySize)
	}
	sk, err := kem.NewPrivateKey(devicePrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	sealed, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return nil, fmt.Errorf("push: the envelope is not standard base64: %w", err)
	}
	// Anything shorter than the encapsulated key plus a Poly1305 tag cannot be
	// an envelope; saying so is clearer than whatever the AEAD would report.
	if len(sealed) < KeySize+16 {
		return nil, fmt.Errorf("push: the envelope is %d bytes, too short to hold a key and a tag", len(sealed))
	}
	plaintext, err := hpke.Open(sk, kdf, aead, []byte(Info), sealed)
	if err != nil {
		return nil, fmt.Errorf("push: open: %w", err)
	}
	return plaintext, nil
}

// GenerateKeyPair makes a fresh raw X25519 key pair: what a device generates
// for itself, and what krakenctl hands a developer standing in for one.
func GenerateKeyPair() (privateKey, publicKey []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// PublicKeyOf derives the raw public key that belongs to a raw private key.
func PublicKeyOf(privateKey []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	return k.PublicKey().Bytes(), nil
}

// marshalPayload is json.Marshal without HTML escaping. The body is a sentence
// that can carry "<", ">" or "&", and the app gains nothing from seeing them
// as < sequences — they only make the plaintext longer and the test
// vector harder to read.
func marshalPayload(p Payload) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, fmt.Errorf("push: encode payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
