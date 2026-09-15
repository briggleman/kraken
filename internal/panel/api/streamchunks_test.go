package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// testAPI builds a bare Server for unit-testing helpers that only need the
// store and the logger. No seeding: nothing here goes through the router.
func testAPI(st store.Store) *Server {
	return New(&config.Config{Env: "test", SessionTTL: time.Hour}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// chunker returns a recv func that yields each of chunks in turn and then err.
func chunker(chunks []string, err error) func() ([]byte, error) {
	i := 0
	return func() ([]byte, error) {
		if i < len(chunks) {
			c := chunks[i]
			i++
			return []byte(c), nil
		}
		return nil, err
	}
}

// A stream that ends cleanly writes exactly what it was given, under the
// headers, with no panic.
func TestStreamChunksWholePayload(t *testing.T) {
	s := testAPI(memory.New())
	rec := httptest.NewRecorder()
	s.streamChunks(rec, chunker([]string{"abc", "def"}, io.EOF), "application/zip", "saves.zip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "abcdef" {
		t.Fatalf("body = %q, want the whole payload", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="saves.zip"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

// A stream that fails before its first chunk answers as an error, with no
// attachment header for a browser to save the error body under.
func TestStreamChunksFirstChunkFailure(t *testing.T) {
	s := testAPI(memory.New())
	rec := httptest.NewRecorder()
	s.streamChunks(rec, chunker(nil, errors.New("agent exploded")), "application/zip", "saves.zip")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("a failed stream set Content-Disposition %q", cd)
	}
}

// A stream that fails AFTER bytes are out must tear the connection down. The
// status is long since committed, so returning normally would let net/http
// finish the chunked response and hand the browser a truncated file as a
// completed download.
func TestStreamChunksAbortsOnMidStreamFailure(t *testing.T) {
	s := testAPI(memory.New())
	rec := httptest.NewRecorder()
	defer func() {
		rvr := recover()
		if rvr == nil {
			t.Fatal("a mid-stream failure returned normally — the truncated body would arrive as a clean 200")
		}
		if rvr != http.ErrAbortHandler {
			t.Fatalf("panicked with %v, want http.ErrAbortHandler", rvr)
		}
	}()
	s.streamChunks(rec, chunker([]string{"abc"}, errors.New("node went away")),
		"application/zip", "saves.zip", "server", "srv-1", "paths", 1)
}

// auditCtxRecorder notes whether the context the store was handed had already
// been cancelled.
type auditCtxRecorder struct {
	store.Store
	sawCancelled bool
	entries      int
}

func (a *auditCtxRecorder) AppendAudit(ctx context.Context, e *store.AuditEntry) error {
	a.sawCancelled = ctx.Err() != nil
	a.entries++
	return a.Store.AppendAudit(ctx, e)
}

// An audit row is written after the response, and the cases most worth
// recording are the ones where the client hung up first — a cancelled
// multi-GB download. The append must therefore not inherit the request's
// cancellation, or the store drops exactly those rows.
func TestAuditAppendOutlivesACancelledRequest(t *testing.T) {
	rec := &auditCtxRecorder{Store: memory.New()}
	s := testAPI(rec)

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/srv-1/files/raw", nil).WithContext(ctx)
	cancel() // the client walked away mid-download

	if !s.appendAudit(r, http.StatusOK, "", "GET /servers/{id}/files/raw — download token redeemed") {
		t.Fatal("the audit append reported failure on a cancelled request")
	}
	if rec.entries != 1 {
		t.Fatalf("the store saw %d entries, want 1", rec.entries)
	}
	if rec.sawCancelled {
		t.Fatal("the store was handed an already-cancelled context — a real store would drop the row")
	}
}
