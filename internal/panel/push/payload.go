// Package push seals alert payloads to a device's key and hands them to the
// Kraken push relay. It is the Panel's half of the contract in
// docs/design/push-alerts.md: the plaintext the companion app decrypts, the
// HPKE scheme that keeps the relay blind to it, and the relay request with its
// retry and drop rules. It deliberately knows nothing about devices, rules,
// the store or HTTP handlers — the event pipeline decides who gets what and
// calls in here to seal and send it.
package push

// PayloadVersion is the plaintext's "v". It changes only for a change the app
// cannot read past; a new field is not one, because the app ignores fields it
// does not know.
const PayloadVersion = 1

// The alert classes. A class is what the app keys its interruption level,
// sound and grouping off, so the set is closed: a new kind of event joins one
// of these rather than inventing a fourth.
const (
	// ClassAttend is something that needs a human.
	ClassAttend = "attend"
	// ClassHealed is the fleet fixing itself; repetition is the signal.
	ClassHealed = "healed"
	// ClassAlive is social and quiet, grouped per server.
	ClassAlive = "alive"
)

// The events, each named in the plaintext so the app can say more than its
// class does. The design doc's table says where each one comes from.
const (
	EventNodeOffline     = "node_offline"
	EventNodePartial     = "node_partial"
	EventServerCrashed   = "server_crashed"
	EventWatchdogRestart = "watchdog_restart"
	EventCrashLoop       = "crash_loop"
	EventBackupFailed    = "backup_failed"
	EventPlayerJoined    = "player_joined"
	// EventTest is the alert POST /devices/{id}/test sends, always as attend.
	EventTest = "test"
)

// Payload is the plaintext sealed to a device: UTF-8 JSON, field names as the
// design doc spells them. ServerID and NodeID are omitted when empty, because
// a node event has no server and the app reads an absent id more honestly
// than an empty string it might try to open.
type Payload struct {
	V        int    `json:"v"`
	Class    string `json:"class"`
	Event    string `json:"event"`
	ServerID string `json:"server_id,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	// Title is the object's name: the server or the node.
	Title string `json:"title"`
	// Body is one sentence in the house voice, the same voice as the
	// console's system lines.
	Body string `json:"body"`
	// Thread groups notifications on the phone: per server for alive and
	// healed, per node for node events.
	Thread string `json:"thread"`
	// TSms is when the event happened, in Unix milliseconds — not when it
	// was sent, which a retry can push back by up to two minutes.
	TSms int64 `json:"ts_ms"`
}
