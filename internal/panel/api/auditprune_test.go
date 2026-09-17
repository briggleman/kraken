package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// seedAudit writes one entry aged `age` into the past.
func seedAudit(t *testing.T, st *memory.Store, age time.Duration) {
	t.Helper()
	e := &store.AuditEntry{
		ID:     uuid.NewString(),
		Time:   time.Now().Add(-age),
		Actor:  "admin",
		Action: "POST /servers",
		Method: http.MethodPost,
		Path:   "/api/v1/servers",
		Status: http.StatusCreated,
	}
	if err := st.AppendAudit(context.Background(), e); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
}

func auditCount(t *testing.T, st *memory.Store) int {
	t.Helper()
	got, err := st.ListAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return len(got)
}

const day = 24 * time.Hour

// A pass takes everything past the window and nothing inside it. The boundary
// matters as much as the extremes: an entry a day short of the window is the
// one an operator is most likely to still be reading.
func TestAuditPruneDropsOnlyWhatIsPastTheWindow(t *testing.T) {
	srv, st := newTestAPIWith(t, func(c *config.Config) { c.AuditRetentionDays = 90 })
	// The seeded Panel has already audited its own bootstrap, so count the
	// difference rather than the total.
	base := auditCount(t, st)

	seedAudit(t, st, 400*day) // long past
	seedAudit(t, st, 91*day)  // just past
	seedAudit(t, st, 89*day)  // just inside
	seedAudit(t, st, time.Minute)

	removed := srv.PruneAuditOnceForTest(context.Background())
	if removed != 2 {
		t.Fatalf("removed %d entries, want 2", removed)
	}
	if got, want := auditCount(t, st), base+2; got != want {
		t.Fatalf("%d entries left, want %d", got, want)
	}

	// Nothing older remains, so a second pass is a no-op rather than a second
	// bite at the same rows.
	if again := srv.PruneAuditOnceForTest(context.Background()); again != 0 {
		t.Fatalf("second pass removed %d entries, want 0", again)
	}
}

// Retention 0 is "keep forever": the pass must not touch even an entry years
// old, and StartAuditPruner must not start a loop at all.
func TestAuditPruneKeepsEverythingWhenRetentionIsZero(t *testing.T) {
	srv, st := newTestAPIWith(t, func(c *config.Config) { c.AuditRetentionDays = 0 })
	before := auditCount(t, st)
	seedAudit(t, st, 4000*day)

	if removed := srv.PruneAuditOnceForTest(context.Background()); removed != 0 {
		t.Fatalf("removed %d entries with retention off, want 0", removed)
	}
	if got, want := auditCount(t, st), before+1; got != want {
		t.Fatalf("%d entries left, want %d", got, want)
	}
}

// countingStore answers PruneAudit from a budget of "old" rows, a batch at a
// time, exactly as Postgres does with a LIMIT. Everything else on the Store
// interface is unreachable here, so the embedded nil is never dereferenced.
type countingStore struct {
	store.Store
	remaining int64
	calls     []int
}

func (c *countingStore) PruneAudit(_ context.Context, _ time.Time, limit int) (int64, error) {
	c.calls = append(c.calls, limit)
	n := int64(limit)
	if n > c.remaining {
		n = c.remaining
	}
	c.remaining -= n
	return n, nil
}

// A backlog larger than one batch is drained by looping, not by one enormous
// DELETE — and the loop stops on the first short pass rather than spinning.
func TestAuditPruneLoopsUntilABatchComesBackShort(t *testing.T) {
	st := &countingStore{remaining: int64(api.AuditPruneBatchForTest)*2 + 7}
	cfg := &config.Config{Env: "test", AuditRetentionDays: 90}
	srv := api.New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	removed := srv.PruneAuditOnceForTest(context.Background())
	if want := int64(api.AuditPruneBatchForTest)*2 + 7; removed != want {
		t.Fatalf("removed %d entries, want %d", removed, want)
	}
	// Two full batches, then the short one that ends the loop.
	if len(st.calls) != 3 {
		t.Fatalf("made %d passes, want 3: %v", len(st.calls), st.calls)
	}
	for i, got := range st.calls {
		if got != api.AuditPruneBatchForTest {
			t.Errorf("pass %d asked for %d rows, want %d", i, got, api.AuditPruneBatchForTest)
		}
	}
}

// The console quotes the window instead of a literal, so GET /audit has to
// carry it — including the 0 that means nothing is pruned.
func TestListAuditReportsTheRetentionWindow(t *testing.T) {
	for _, days := range []int{90, 7, 0} {
		h := newTestServerWith(t, func(c *config.Config) { c.AuditRetentionDays = days })
		token := login(t, h)
		rec := do(t, h, http.MethodGet, "/api/v1/audit", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /audit: status %d, body %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			RetentionDays *int `json:"retention_days"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.RetentionDays == nil || *resp.RetentionDays != days {
			t.Errorf("retention_days = %v, want %d", resp.RetentionDays, days)
		}
	}
}
