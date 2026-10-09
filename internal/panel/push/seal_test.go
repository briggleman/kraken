package push

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// -update-vector rewrites testdata/vector.json with a fresh key pair and
// envelope. The committed vector is what the companion app's tests open, so
// regenerating it is a deliberate act, never a side effect of `go test`.
var updateVector = flag.Bool("update-vector", false, "regenerate testdata/vector.json")

func samplePayload() Payload {
	return Payload{
		V:        PayloadVersion,
		Class:    ClassAttend,
		Event:    EventServerCrashed,
		ServerID: "4866d26c-5b0e-4b8a-9d1e-6f3c2a7b9e01",
		NodeID:   "bd70f48c-2e4a-4f6b-8c3d-1a9e5f7b2c40",
		Title:    "dragonwilds-01",
		Body:     "stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing",
		Thread:   "server:4866d26c-5b0e-4b8a-9d1e-6f3c2a7b9e01",
		TSms:     1791555300000,
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	p := samplePayload()
	env, err := Seal(pub, p)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := Open(priv, env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != p {
		t.Fatalf("round trip changed the payload:\n got %+v\nwant %+v", got, p)
	}

	// The envelope is enc ‖ ciphertext: 32 bytes of key, then the plaintext
	// plus a 16-byte tag, nothing else.
	raw, _ := base64.StdEncoding.DecodeString(env)
	plain, _ := marshalPayload(p)
	if want := KeySize + len(plain) + 16; len(raw) != want {
		t.Fatalf("envelope is %d bytes, want %d", len(raw), want)
	}

	// Encapsulation is randomized: the same alert sealed twice must not give
	// the relay two identical ciphertexts to correlate.
	env2, _ := Seal(pub, p)
	if env2 == env {
		t.Fatal("two seals of the same payload produced the same envelope")
	}
}

func TestSealStampsAndChecksTheVersion(t *testing.T) {
	priv, pub, _ := GenerateKeyPair()
	p := samplePayload()
	p.V = 0
	env, err := Seal(pub, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(priv, env)
	if err != nil || got.V != PayloadVersion {
		t.Fatalf("a zero version should be sealed as %d, got %d (%v)", PayloadVersion, got.V, err)
	}
	p.V = 2
	if _, err := Seal(pub, p); err == nil {
		t.Fatal("Seal accepted a payload version it does not write")
	}
}

func TestSealOmitsAnAbsentServer(t *testing.T) {
	priv, pub, _ := GenerateKeyPair()
	env, err := Seal(pub, Payload{Class: ClassAttend, Event: EventNodeOffline, NodeID: "n1", Title: "abyss-lnx", Body: "went offline", Thread: "node:n1", TSms: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := OpenRaw(priv, env)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("server_id")) {
		t.Fatalf("a node event carried a server_id: %s", raw)
	}
}

func TestSealRejectsBadKeys(t *testing.T) {
	// The all-zero point and the point of order 8 are two of the small-order
	// X25519 points; Diffie-Hellman with either yields all zeroes.
	order8, _ := hex.DecodeString("e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800")
	cases := map[string][]byte{
		"empty":      nil,
		"short":      make([]byte, 31),
		"long":       make([]byte, 33),
		"zero point": make([]byte, 32),
		"order 8":    order8,
	}
	for name, key := range cases {
		if _, err := Seal(key, samplePayload()); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: Seal gave %v, want ErrBadKey", name, err)
		}
		if err := ValidatePublicKey(key); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: ValidatePublicKey gave %v, want ErrBadKey", name, err)
		}
	}
}

func TestOpenRefusesTheWrongKeyAndTampering(t *testing.T) {
	_, pub, _ := GenerateKeyPair()
	other, _, _ := GenerateKeyPair()
	env, _ := Seal(pub, samplePayload())
	if _, err := Open(other, env); err == nil {
		t.Fatal("opened an envelope with a key it was not sealed to")
	}

	priv, pub, _ := GenerateKeyPair()
	env, _ = Seal(pub, samplePayload())
	raw, _ := base64.StdEncoding.DecodeString(env)
	raw[len(raw)-1] ^= 1
	if _, err := Open(priv, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("opened a tampered envelope")
	}
	if _, err := Open(priv, "not base64!"); err == nil {
		t.Fatal("opened something that is not base64")
	}
	if _, err := Open(priv, base64.StdEncoding.EncodeToString(raw[:40])); err == nil {
		t.Fatal("opened an envelope too short to hold a key and a tag")
	}
}

// referenceOpen is RFC 9180 base-mode Open for DHKEM(X25519, HKDF-SHA256) /
// HKDF-SHA256 / ChaCha20-Poly1305, written out from the RFC's own pseudocode
// with nothing but HKDF, X25519 and the AEAD. It shares no code with
// crypto/hpke, so Seal agreeing with it is evidence the envelope is standard
// HPKE and not merely something Go can read back — and it is the recipe the
// app's decryption follows, step by step.
func referenceOpen(t *testing.T, skR []byte, envelope string) []byte {
	t.Helper()
	sealed, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatal(err)
	}
	enc, ct := sealed[:32], sealed[32:]

	i2osp2 := func(n int) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, uint16(n)); return b }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	kemSuite := cat([]byte("KEM"), i2osp2(0x0020))
	hpkeSuite := cat([]byte("HPKE"), i2osp2(0x0020), i2osp2(0x0001), i2osp2(0x0003))
	extract := func(suite, salt []byte, label string, ikm []byte) []byte {
		prk, err := hkdf.Extract(sha256.New, cat([]byte("HPKE-v1"), suite, []byte(label), ikm), salt)
		if err != nil {
			t.Fatal(err)
		}
		return prk
	}
	expand := func(suite, prk []byte, label string, info []byte, n int) []byte {
		out, err := hkdf.Expand(sha256.New, prk, string(cat(i2osp2(n), []byte("HPKE-v1"), suite, []byte(label), info)), n)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Decap: DH with the ephemeral key, then ExtractAndExpand over enc ‖ pkR.
	sk, err := ecdh.X25519().NewPrivateKey(skR)
	if err != nil {
		t.Fatal(err)
	}
	pkE, err := ecdh.X25519().NewPublicKey(enc)
	if err != nil {
		t.Fatal(err)
	}
	dh, err := sk.ECDH(pkE)
	if err != nil {
		t.Fatal(err)
	}
	eaePRK := extract(kemSuite, nil, "eae_prk", dh)
	shared := expand(kemSuite, eaePRK, "shared_secret", cat(enc, sk.PublicKey().Bytes()), 32)

	// KeySchedule, mode_base (0x00), no PSK.
	pskIDHash := extract(hpkeSuite, nil, "psk_id_hash", nil)
	infoHash := extract(hpkeSuite, nil, "info_hash", []byte(Info))
	ksc := cat([]byte{0x00}, pskIDHash, infoHash)
	secret := extract(hpkeSuite, shared, "secret", nil)
	key := expand(hpkeSuite, secret, "key", ksc, 32)
	nonce := expand(hpkeSuite, secret, "base_nonce", ksc, 12)

	// The first (and only) message uses sequence number 0, so the nonce is
	// the base nonce unchanged; there is no AAD.
	a, err := chacha20poly1305.New(key)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := a.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("reference open: %v", err)
	}
	return pt
}

func TestSealAgreesWithTheReferenceImplementation(t *testing.T) {
	priv, pub, _ := GenerateKeyPair()
	p := samplePayload()
	env, err := Seal(pub, p)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := marshalPayload(p)
	if got := referenceOpen(t, priv, env); !bytes.Equal(got, want) {
		t.Fatalf("reference open disagrees:\n got %s\nwant %s", got, want)
	}
}

// vector is testdata/vector.json, the fixed test vector published for the
// companion app. HPKE encapsulation is randomized, so a vector cannot say
// "sealing this gives that"; it says "opening this envelope with this key
// gives exactly these bytes", which is the half the app implements.
type vector struct {
	Comment         string `json:"comment"`
	Suite           string `json:"suite"`
	Mode            string `json:"mode"`
	Info            string `json:"info"`
	PrivateKey      string `json:"private_key"`
	PublicKey       string `json:"public_key"`
	Plaintext       string `json:"plaintext"`
	PlaintextBase64 string `json:"plaintext_base64"`
	Envelope        string `json:"envelope"`
	Enc             string `json:"enc"`
	Ciphertext      string `json:"ciphertext"`
}

const vectorPath = "testdata/vector.json"

func TestFixedVector(t *testing.T) {
	if *updateVector {
		writeVector(t)
	}
	data, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.DecodeString
	priv, _ := b64(v.PrivateKey)
	pub, _ := b64(v.PublicKey)
	plain, _ := b64(v.PlaintextBase64)

	if v.Info != Info {
		t.Fatalf("the vector's info is %q, the code seals under %q", v.Info, Info)
	}
	if derived, err := PublicKeyOf(priv); err != nil || !bytes.Equal(derived, pub) {
		t.Fatalf("the vector's public key does not belong to its private key (%v)", err)
	}
	if string(plain) != v.Plaintext {
		t.Fatal("the vector's plaintext and plaintext_base64 disagree")
	}
	// The split fields are a convenience for the app developer; they must be
	// exactly the envelope cut at 32 bytes.
	enc, _ := b64(v.Enc)
	ct, _ := b64(v.Ciphertext)
	env, _ := b64(v.Envelope)
	if !bytes.Equal(append(append([]byte{}, enc...), ct...), env) || len(enc) != KeySize {
		t.Fatal("the vector's enc and ciphertext are not the envelope split at 32 bytes")
	}

	got, err := OpenRaw(priv, v.Envelope)
	if err != nil {
		t.Fatalf("open the committed envelope: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("the committed envelope opened to\n%s\nwant\n%s", got, plain)
	}
	if ref := referenceOpen(t, priv, v.Envelope); !bytes.Equal(ref, plain) {
		t.Fatal("the reference implementation opens the committed envelope to something else")
	}
	// And the plaintext is what this build would seal for the same alert, so
	// the vector cannot drift from the payload format without failing here.
	if p, err := Open(priv, v.Envelope); err != nil || p != samplePayload() {
		t.Fatalf("the committed envelope is not the sample payload: %+v (%v)", p, err)
	}
	if want, _ := marshalPayload(samplePayload()); !bytes.Equal(want, plain) {
		t.Fatalf("this build encodes the sample payload as\n%s\nbut the vector holds\n%s", want, plain)
	}
}

func writeVector(t *testing.T) {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := marshalPayload(samplePayload())
	env, err := Seal(pub, samplePayload())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(env)
	b64 := base64.StdEncoding.EncodeToString
	v := vector{
		Comment:         "Kraken push alert test vector. HPKE encapsulation is randomized, so this is an open-this vector: opening envelope with private_key must give exactly plaintext_base64. See README.md beside this file.",
		Suite:           "DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20-Poly1305",
		Mode:            "base",
		Info:            Info,
		PrivateKey:      b64(priv),
		PublicKey:       b64(pub),
		Plaintext:       string(plain),
		PlaintextBase64: b64(plain),
		Envelope:        env,
		Enc:             b64(raw[:KeySize]),
		Ciphertext:      b64(raw[KeySize:]),
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(vectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vectorPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", vectorPath)
}
