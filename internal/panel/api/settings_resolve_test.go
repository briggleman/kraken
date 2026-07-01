package api

import (
	"context"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// TestSessionTTLResolution covers the precedence: env > stored setting > default.
func TestSessionTTLResolution(t *testing.T) {
	ctx := context.Background()

	stored := memory.New()
	if err := stored.SaveSettings(ctx, &store.Settings{SessionTTLSeconds: 3600}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		srv  *Server
		want time.Duration
	}{
		{"stored value used", &Server{cfg: &config.Config{SessionTTL: 24 * time.Hour}, store: stored}, time.Hour},
		{"default when unset", &Server{cfg: &config.Config{SessionTTL: 24 * time.Hour}, store: memory.New()}, 24 * time.Hour},
		{"env wins and locks", &Server{cfg: &config.Config{SessionTTL: 2 * time.Hour, SessionTTLFromEnv: true}, store: stored}, 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.srv.sessionTTL(ctx); got != tc.want {
				t.Fatalf("sessionTTL = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAllowedOriginsResolution covers the same precedence for the WS allowlist.
func TestAllowedOriginsResolution(t *testing.T) {
	ctx := context.Background()

	stored := memory.New()
	if err := stored.SaveSettings(ctx, &store.Settings{AllowedOrigins: []string{"panel.example.com"}}); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: &config.Config{}, store: stored}
	if got := s.allowedOrigins(ctx); len(got) != 1 || got[0] != "panel.example.com" {
		t.Fatalf("stored origins not used: %v", got)
	}

	// Empty store → empty (caller applies dev defaults).
	sEmpty := &Server{cfg: &config.Config{}, store: memory.New()}
	if got := sEmpty.allowedOrigins(ctx); len(got) != 0 {
		t.Fatalf("expected empty origins, got %v", got)
	}

	// Env wins and locks (stored value ignored).
	sEnv := &Server{cfg: &config.Config{AllowedOrigins: []string{"env.example.com"}, AllowedOriginsFromEnv: true}, store: stored}
	if got := sEnv.allowedOrigins(ctx); len(got) != 1 || got[0] != "env.example.com" {
		t.Fatalf("env origins should win: %v", got)
	}
}
