package api

import (
	"testing"
	"time"
)

// The Add Node dialog's refresh control tells the operator that minting a new
// token stops the previous one working. These cover the half of that promise the
// Panel is responsible for.
func TestRevokeStopsATokenBeingRedeemed(t *testing.T) {
	b := newBootstrapRegistry()
	tok, _, err := b.issue("node-a", time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !b.revoke(tok) {
		t.Fatal("revoke reported the token was not live; it was just issued")
	}
	if _, err := b.redeem(tok); err == nil {
		t.Error("a revoked token was still redeemable — the refresh control's promise is false")
	}
}

// Scoped, not a sweep: an enrollment in flight elsewhere must survive, which is
// why the caller names the one token it is replacing.
func TestRevokeLeavesOtherOutstandingTokensAlone(t *testing.T) {
	b := newBootstrapRegistry()
	keep, _, _ := b.issue("node-a", time.Minute)
	drop, _, _ := b.issue("node-b", time.Minute)

	b.revoke(drop)

	if _, err := b.redeem(keep); err != nil {
		t.Errorf("revoking one token broke another enrollment: %v", err)
	}
}

func TestRevokeIsANoOpForUnknownAndEmptyTokens(t *testing.T) {
	b := newBootstrapRegistry()
	live, _, _ := b.issue("node-a", time.Minute)

	if b.revoke("") {
		t.Error(`revoke("") reported a hit`)
	}
	if b.revoke("deadbeef") {
		t.Error("revoke reported a hit for a token that never existed")
	}
	// A no-op must not disturb what is actually outstanding.
	if _, err := b.redeem(live); err != nil {
		t.Errorf("a no-op revoke consumed a live token: %v", err)
	}
}

// Revoking a token that was already redeemed must not resurrect or error: the
// handler calls revoke unconditionally when the client names a predecessor.
func TestRevokeAfterRedeemStaysRevoked(t *testing.T) {
	b := newBootstrapRegistry()
	tok, _, _ := b.issue("node-a", time.Minute)
	if _, err := b.redeem(tok); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if b.revoke(tok) {
		t.Error("revoke reported a hit for an already-redeemed token")
	}
	if _, err := b.redeem(tok); err == nil {
		t.Error("a redeemed token became redeemable again")
	}
}
