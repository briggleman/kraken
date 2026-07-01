package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Seed ensures the built-in roles exist and, if the instance has no users,
// creates the bootstrap admin from configuration. It is idempotent and safe to
// run on every startup.
func Seed(ctx context.Context, st store.Store, cfg *config.Config, logger *slog.Logger) error {
	for _, role := range rbac.BuiltinRoles() {
		r := role // capture
		if err := st.UpsertRole(ctx, &r); err != nil {
			return fmt.Errorf("seed role %s: %w", role.ID, err)
		}
	}

	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("seed: count users: %w", err)
	}
	if n > 0 {
		return nil
	}

	// Post-setup lockdown: if the operator disabled bootstrap (and didn't pin it
	// via env), don't re-create the bootstrap admin when the instance is empty.
	if !cfg.BootstrapFromEnv {
		if settings, serr := st.GetSettings(ctx); serr == nil && settings != nil && settings.BootstrapDisabled {
			logger.Warn("no users exist but bootstrap admin is disabled in settings; skipping bootstrap")
			return nil
		}
	}

	// No weak default: if no bootstrap password is configured, mint a strong
	// random one and log it once so the operator can sign in and rotate it.
	password := cfg.BootstrapAdminPassword
	generated := false
	if password == "" {
		p, err := randomPassword()
		if err != nil {
			return fmt.Errorf("seed: generate bootstrap password: %w", err)
		}
		password, generated = p, true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("seed: hash bootstrap password: %w", err)
	}
	admin := &store.User{
		ID:           uuid.NewString(),
		Username:     cfg.BootstrapAdminUser,
		Email:        "",
		PasswordHash: hash,
		RoleID:       rbac.RoleOwner,
		// First-run security: force a rotation off the bootstrap credential before
		// the account can do anything else.
		MustChangePassword: true,
		CreatedAt:          time.Now().UTC(),
	}
	if err := st.CreateUser(ctx, admin); err != nil {
		return fmt.Errorf("seed: create bootstrap admin: %w", err)
	}
	if generated {
		// This is the only time the generated password is ever visible.
		logger.Warn("created bootstrap admin with a generated password — sign in and rotate it now",
			"username", admin.Username, "password", password)
	} else {
		logger.Warn("created bootstrap admin user — change the password immediately",
			"username", admin.Username)
	}
	return nil
}

// randomPassword returns a 24-character URL-safe random password.
func randomPassword() (string, error) {
	b := make([]byte, 18) // 18 bytes → 24 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
