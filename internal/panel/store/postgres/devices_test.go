package postgres_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/briggleman/kraken/internal/panel/secrets"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/migrate"
	"github.com/briggleman/kraken/internal/panel/store/postgres"
)

// mkDeviceUser creates a user for devices to belong to (device_registrations
// has a foreign key to users) and removes it when the test ends.
func mkDeviceUser(t *testing.T, st *postgres.Store) string {
	t.Helper()
	ctx := context.Background()
	u := &store.User{ID: uuid.NewString(), Username: "dev-" + uuid.NewString()[:8], PasswordHash: "h", RoleID: "owner", CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteUser(ctx, u.ID) })
	return u.ID
}

func upsertDevice(t *testing.T, st *postgres.Store, id, user, token string, at time.Time) *store.Device {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 32)
	d, err := st.UpsertDevice(context.Background(), &store.Device{
		ID: id, UserID: user, Platform: store.DevicePlatformIOS, APNsToken: token,
		APNsEnvironment: store.APNsProduction, PublicKey: key, Name: "phone",
		Rules: store.DefaultDeviceRules(), CreatedAt: at, LastSeenAt: at,
	})
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	return d
}

// Push-alert devices (#348) through Postgres: the upsert's refresh, another
// user's registration of the same id as a row of its own, the token-guarded
// dead mark, the rules document, and the cascade from users. The devices go
// with their users when the test's cleanup deletes them.
func TestPostgresDevices(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	ann, bob := mkDeviceUser(t, st), mkDeviceUser(t, st)
	d1, d2 := uuid.NewString(), uuid.NewString()

	t0 := time.Now().UTC().Truncate(time.Microsecond) // Postgres keeps microseconds
	got := upsertDevice(t, st, d1, ann, "aa", t0)
	if got.UserID != ann || got.APNsToken != "aa" || !bytes.Equal(got.PublicKey, bytes.Repeat([]byte{7}, 32)) ||
		!got.Rules.Attend || !got.Rules.Healed || !got.Rules.Alive || got.Rules.AliveMutedServers == nil ||
		!got.CreatedAt.Equal(t0) || got.LastSentAt != nil || got.TokenInvalidAt != nil {
		t.Fatalf("new device = %+v", got)
	}
	upsertDevice(t, st, d2, ann, "cc", t0.Add(time.Second))

	muted := store.DeviceRules{Attend: true, Healed: true, AliveMutedServers: []string{"s1"}}
	if err := st.UpdateDeviceRules(ctx, ann, d1, muted); err != nil {
		t.Fatalf("UpdateDeviceRules: %v", err)
	}
	sent := t0.Add(time.Minute)
	if err := st.TouchDeviceSent(ctx, ann, d1, sent); err != nil {
		t.Fatalf("TouchDeviceSent: %v", err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, ann, d1, "stale", t0); ok || err != nil {
		t.Fatalf("marking a token that is not the device's: %v %v", ok, err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, bob, d1, "aa", t0); ok || err != nil {
		t.Fatalf("marking through another user: %v %v", ok, err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, "not-a-uuid", d1, "aa", t0); ok || err != nil {
		t.Fatalf("marking through a malformed user id: %v %v", ok, err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, ann, d1, "aa", t0); !ok || err != nil {
		t.Fatalf("marking the device's own token: %v %v", ok, err)
	}
	if got, _ = st.GetDevice(ctx, ann, d1); got.TokenInvalidAt == nil || !got.TokenInvalidAt.Equal(t0) {
		t.Fatalf("token_invalid_at = %v, want %v", got.TokenInvalidAt, t0)
	}

	// A refresh by the owner keeps the rules, creation and last send.
	t1 := t0.Add(time.Hour)
	got = upsertDevice(t, st, d1, ann, "bb", t1)
	if got.APNsToken != "bb" || got.TokenInvalidAt != nil || !got.LastSeenAt.Equal(t1) || !got.CreatedAt.Equal(t0) ||
		got.Rules.Alive || len(got.Rules.AliveMutedServers) != 1 || got.LastSentAt == nil || !got.LastSentAt.Equal(sent) {
		t.Fatalf("refresh = %+v", got)
	}

	if ds, err := st.ListDevicesByUser(ctx, ann); err != nil || len(ds) != 2 || ds[0].ID != d1 || ds[1].ID != d2 {
		t.Fatalf("ann's devices = %+v err=%v, want d1 then d2", ds, err)
	}
	if ds, err := st.ListDevicesByUser(ctx, "not-a-uuid"); err != nil || len(ds) != 0 {
		t.Fatalf("a malformed user id: %+v %v, want none", ds, err)
	}

	// Another user's registration of the same id is a row of its own, and
	// the first user's row is untouched.
	t2 := t1.Add(time.Hour)
	got = upsertDevice(t, st, d1, bob, "dd", t2)
	if got.UserID != bob || !got.Rules.Alive || len(got.Rules.AliveMutedServers) != 0 || got.LastSentAt != nil || !got.CreatedAt.Equal(t2) {
		t.Fatalf("bob's registration = %+v", got)
	}
	if a, err := st.GetDevice(ctx, ann, d1); err != nil || a.APNsToken != "bb" || a.Rules.Alive || !a.LastSeenAt.Equal(t1) || a.LastSentAt == nil {
		t.Fatalf("bob's registration changed ann's row: %+v err=%v", a, err)
	}
	if _, err := st.GetDevice(ctx, "not-a-uuid", d1); err != store.ErrNotFound {
		t.Fatalf("GetDevice with a malformed user id = %v, want ErrNotFound", err)
	}
	all, err := st.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	var seen int
	for _, d := range all {
		if d.ID == d1 || d.ID == d2 {
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("ListDevices holds %d of the three rows", seen)
	}

	// Deleting a user cascades to their devices.
	if err := st.DeleteUser(ctx, ann); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := st.GetDevice(ctx, ann, d2); err != store.ErrNotFound {
		t.Fatalf("a deleted user's device: %v, want ErrNotFound", err)
	}
	if _, err := st.GetDevice(ctx, bob, d1); err != nil {
		t.Fatalf("deleting ann took bob's registration of the same id: %v", err)
	}
	if n, err := st.DeleteDevicesByUser(ctx, bob); err != nil || n != 1 {
		t.Fatalf("DeleteDevicesByUser = %d %v, want 1", n, err)
	}
	if err := st.DeleteDevice(ctx, bob, d1); err != store.ErrNotFound {
		t.Fatalf("DeleteDevice of a gone device = %v", err)
	}
	if err := st.UpdateDeviceRules(ctx, bob, d1, muted); err != store.ErrNotFound {
		t.Fatalf("UpdateDeviceRules of a gone device = %v", err)
	}
	if err := st.TouchDeviceSent(ctx, bob, d1, sent); err != store.ErrNotFound {
		t.Fatalf("TouchDeviceSent of a gone device = %v", err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, bob, d1, "dd", t1); ok || err != nil {
		t.Fatalf("marking a gone device: %v %v", ok, err)
	}
}

// With a secrets key the APNs token is sealed at rest and clear on read, and
// the dead-token guard still compares the clear token.
func TestPostgresDeviceTokenEncryptionAtRest(t *testing.T) {
	url := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KRAKEN_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	if err := migrate.Up(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	cipher, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	st, err := postgres.New(ctx, url, postgres.WithCipher(cipher))
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(ctx) })

	const token = "740f4707bebcf74f9b7c25d48e3358945f6aa01da5ddb387462c7eaf61bb78ad"
	user := mkDeviceUser(t, st)
	id := uuid.NewString()
	if got := upsertDevice(t, st, id, user, token, time.Now().UTC()); got.APNsToken != token {
		t.Fatalf("the upsert's answer = %q, want the clear token", got.APNsToken)
	}
	var stored string
	if err := raw.QueryRow(ctx, `SELECT apns_token FROM device_registrations WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatalf("read raw token: %v", err)
	}
	if strings.Contains(stored, token) || !strings.HasPrefix(stored, "enc:v1:") {
		t.Fatalf("token at rest = %q, want it sealed", stored)
	}
	if got, err := st.GetDevice(ctx, user, id); err != nil || got.APNsToken != token {
		t.Fatalf("GetDevice token = %q err=%v", got.APNsToken, err)
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, user, id, token, time.Now()); !ok || err != nil {
		t.Fatalf("marking the sealed token dead: %v %v", ok, err)
	}
}

// The Panel's install id is written once and read back the same.
func TestPostgresPanelID(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	a, err := st.PanelID(ctx)
	if err != nil || a == "" {
		t.Fatalf("PanelID = %q, %v", a, err)
	}
	b, err := st.PanelID(ctx)
	if err != nil || b != a {
		t.Fatalf("PanelID changed: %q then %q (%v)", a, b, err)
	}
}
