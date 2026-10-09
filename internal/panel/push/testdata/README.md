# Push alert test vector

`vector.json` is a fixed test vector for the companion app's decryption of
Kraken push alerts. The contract it belongs to is
[docs/design/push-alerts.md](../../../../docs/design/push-alerts.md).

## The scheme

- **HPKE** (RFC 9180), **base mode** (no PSK, no sender authentication).
- **Ciphersuite:** DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20-Poly1305
  (KEM `0x0020`, KDF `0x0001`, AEAD `0x0003`).
- **info:** the ASCII bytes `kraken-push-v1`.
- **AAD:** none. One message per HPKE context, so sequence number 0.
- **Keys:** raw 32-byte X25519 keys, standard base64 (with padding).

## The envelope

The `payload` field the relay delivers (APNs custom key `k`) is standard base64
of `enc ‖ ciphertext`:

- `enc` is the **first 32 bytes**: the sender's ephemeral X25519 public key.
- `ciphertext` is **everything after that**: the sealed plaintext followed by
  the 16-byte Poly1305 tag.

The plaintext is UTF-8 JSON (`v`, `class`, `event`, `server_id`, `node_id`,
`title`, `body`, `thread`, `ts_ms`). `server_id` and `node_id` are left out
when the alert has none (a node event has no server). Ignore unknown fields.

## Using the vector

HPKE encapsulation is randomized, so the Panel never produces the same
envelope twice. The vector is therefore an **open** vector, not a seal vector:
opening `envelope` with `private_key` must give exactly the bytes in
`plaintext_base64` (`plaintext` is the same bytes as a JSON string, for
reading). `enc` and `ciphertext` are the envelope already split at byte 32, to
check your own split against. `public_key` belongs to `private_key`.

In CryptoKit (iOS 17+):

```swift
import CryptoKit

let sealed = Data(base64Encoded: vector.envelope)!
let enc = sealed.prefix(32)
let ciphertext = sealed.dropFirst(32)

let privateKey = try Curve25519.KeyAgreement.PrivateKey(
    rawRepresentation: Data(base64Encoded: vector.privateKey)!)
var recipient = try HPKE.Recipient(
    privateKey: privateKey,
    ciphersuite: .Curve25519_SHA256_ChachaPoly,
    info: Data("kraken-push-v1".utf8),
    encapsulatedKey: enc)
let plaintext = try recipient.open(ciphertext)
assert(plaintext == Data(base64Encoded: vector.plaintextBase64)!)
```

`open(_:)` is `mutating`, hence `var`. The initializer and the suite name were
checked against Apple's CryptoKit documentation (both iOS 17.0+).

## Regenerating

`go test ./internal/panel/push -run TestFixedVector -update-vector` writes a new
key pair and envelope. Don't do it casually: the app's tests open this file, so
a new vector means updating theirs too. The Go test opens the committed
envelope with both `crypto/hpke` and a from-the-RFC reference implementation,
and checks that the plaintext matches what this build would seal.
