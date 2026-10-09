package alerts

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/store"
)

// SystemActor is the audit actor for what the Panel does on its own: no user
// asked, no request carried it, so there is no IP, method or path either.
const SystemActor = "system"

// The audit actions for one alert to one device (docs/design/push-alerts.md).
const (
	AuditSent    = "push.sent"
	AuditFailed  = "push.failed"
	AuditDropped = "push.dropped"
)

// AuditAppender is the slice of the store the system audit writer needs.
type AuditAppender interface {
	AppendAudit(ctx context.Context, e *store.AuditEntry) error
}

// SystemAudit appends one audit entry for something the Panel did on its own.
// It follows the shape of the API's recordAuditDetail: the action is the
// stable name, then " — " and what a person reading the log needs to know. The
// detail must never carry a secret; for a push alert that means never the
// device token, the device key or the envelope.
//
// Like the API's audit writes, it does not inherit the caller's cancellation:
// the entry records something that already happened, and a pass that is
// shutting down still owes it. A short deadline of its own keeps a wedged store
// from holding the caller.
func SystemAudit(ctx context.Context, st AuditAppender, action, detail, targetType, targetID string, status int) error {
	if detail != "" {
		action += " — " + detail
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return st.AppendAudit(actx, &store.AuditEntry{
		ID:         uuid.NewString(),
		Time:       time.Now(),
		Actor:      SystemActor,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Status:     status,
	})
}
