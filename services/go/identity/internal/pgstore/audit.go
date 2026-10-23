package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// AuditTimeout bounds one audit write. The auditor interface's Record carries
// no context and is called on the request path, so the write must not be able
// to stall a request for long; a slow or failed write is an audit failure, which
// the service counts (IdentityService.AuditFailures) without changing the
// outcome it describes.
const AuditTimeout = 2 * time.Second

// SecurityAuditLog is an app.SecurityAuditor that appends every event to
// identity_security_audit (migration 0005): the durable, append-only record
// ADR-0010 §10 and ADR-0012 (LEGAL_HOLD) require.
type SecurityAuditLog struct{ pool *pgxpool.Pool }

var _ app.SecurityAuditor = (*SecurityAuditLog)(nil)

// NewSecurityAuditLog returns an audit log on pool, which must be migrated.
func NewSecurityAuditLog(pool *pgxpool.Pool) *SecurityAuditLog { return &SecurityAuditLog{pool: pool} }

// Record implements app.SecurityAuditor: one INSERT, bounded by AuditTimeout.
// Only the event's type, time and random identifiers are written; the table's
// CHECK constraints refuse anything else.
func (l *SecurityAuditLog) Record(e app.SecurityEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), AuditTimeout)
	defer cancel()
	_, err := l.pool.Exec(ctx, `
		INSERT INTO identity_security_audit (event_type, occurred_at, request_id, session_id, locked_until)
		VALUES ($1, $2, $3, $4, $5)`,
		string(e.Type), us(e.At), nullIfEmpty(e.RequestID), nullIfEmpty(e.SessionID), nullTime(e.LockedUntil))
	if err != nil {
		return fmt.Errorf("pgstore: recording a security audit event: %w", Redact(err))
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := us(t)
	return &u
}
