package pii

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

// AccessEntry records a read of sensitive fields. Populate it from the
// authenticated request so the trail answers who saw which subject and why.
type AccessEntry struct {
	OrganizationID uint64
	UserID         uint64
	SubjectType    string
	SubjectID      uint64
	Fields         []string
	Reason         string
	IPAddress      string
	UserAgent      string
}

// AccessLog writes to pii_access_logs, which migration 000017 makes
// append-only. Failures are logged but do not block the read that triggered
// them; callers that require a hard audit gate should check the error.
type AccessLog struct {
	db  sqlx.ExtContext
	log *slog.Logger
}

func NewAccessLog(db sqlx.ExtContext, log *slog.Logger) *AccessLog {
	return &AccessLog{db: db, log: log}
}

func (l *AccessLog) Record(ctx context.Context, e AccessEntry) error {
	if l == nil || l.db == nil {
		return nil
	}
	var fields sql.NullString
	if len(e.Fields) > 0 {
		encoded, err := json.Marshal(e.Fields)
		if err != nil {
			return err
		}
		fields = sql.NullString{String: string(encoded), Valid: true}
	}
	_, err := l.db.ExecContext(ctx, `
		INSERT INTO pii_access_logs
			(organization_id, user_id, subject_type, subject_id, fields, reason, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableID(e.OrganizationID), nullableID(e.UserID),
		e.SubjectType, e.SubjectID, fields,
		nullableText(e.Reason, 100), nullableText(e.IPAddress, 45), nullableText(e.UserAgent, 255))
	if err != nil && l.log != nil {
		l.log.Error("record pii access", "error", err, "subject_type", e.SubjectType, "subject_id", e.SubjectID)
	}
	return err
}

func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableText(v string, max int) any {
	if v == "" {
		return nil
	}
	if runes := []rune(v); len(runes) > max {
		v = string(runes[:max])
	}
	return v
}
