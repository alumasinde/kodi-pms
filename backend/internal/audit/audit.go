package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

type Entry struct {
	OrgID      uint64
	UserID     uint64
	Action     string
	EntityType string
	EntityID   uint64
	OldValues  any
	NewValues  any
	IPAddress  string
	UserAgent  string
}

type Logger struct {
	db  *sqlx.DB
	log *slog.Logger
}

func NewLogger(db *sqlx.DB, log *slog.Logger) *Logger {
	return &Logger{db: db, log: log}
}

// Log writes one append-only audit row. Callers treat failure as non-fatal:
// the business transaction has already committed by this point.
func (l *Logger) Log(ctx context.Context, e Entry) {
	oldJSON, err := marshalNullable(e.OldValues)
	if err != nil {
		l.log.Warn("marshal audit old values", "action", e.Action, "error", err)
	}
	newJSON, err := marshalNullable(e.NewValues)
	if err != nil {
		l.log.Warn("marshal audit new values", "action", e.Action, "error", err)
	}
	_, err = l.db.ExecContext(ctx, `
		INSERT INTO audit_logs
			(organization_id, user_id, action, entity_type, entity_id, old_values, new_values, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableID(e.OrgID), nullableID(e.UserID), e.Action, e.EntityType, nullableID(e.EntityID),
		oldJSON, newJSON, truncate(e.IPAddress, 45), truncate(e.UserAgent, 255))
	if err != nil {
		l.log.Error("write audit log", "action", e.Action, "entity", e.EntityType, "error", err)
	}
}

func marshalNullable(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

func truncate(s string, n int) any {
	if s == "" {
		return nil
	}
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n])
	}
	return s
}
