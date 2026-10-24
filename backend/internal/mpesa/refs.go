package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Refs lists the account numbers tenants type on a paybill. It reads through the
// shortcode and the lease rather than either alone, because a reference that points
// at a paybill this organization no longer runs is a routing bug somebody should see.
func (s *Service) Refs(ctx context.Context, p *rbac.Principal, q RefQuery) (*RefListDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	q.Search = truncate(q.Search, maxSearch)
	rows, total, err := s.repo.ListRefs(ctx, p.OrgID, q)
	if err != nil {
		return nil, err
	}
	items := make([]AccountRefDTO, 0, len(rows))
	for i := range rows {
		items = append(items, accountRefDTO(&rows[i]))
	}
	return &RefListDTO{References: items, Total: total}, nil
}

// CreateRef binds one account number to one lease on one paybill.
//
// Both halves are checked before the write: the shortcode because an FK error would
// confirm ids the caller guessed, and the lease because a reference may only be made
// for a tenancy the caller is allowed to bill. The pair (shortcode, reference) is
// unique, which is what makes a confirmation's lookup answer rather than a guess.
func (s *Service) CreateRef(ctx context.Context, p *rbac.Principal, in CreateRefInput, meta ClientMeta) (*AccountRefDTO, error) {
	if err := authorize(p, "mpesa.manage"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	reference := cleanAccountRef(in.AccountReference, fields)
	if in.ShortcodeID == 0 {
		fields["shortcode_id"] = "required"
	}
	if in.LeaseID == 0 {
		fields["lease_id"] = "required"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	shortcode, err := s.repo.Shortcode(ctx, tx, p.OrgID, in.ShortcodeID)
	if err != nil {
		return nil, notFound(err)
	}
	if shortcode.IsActive == 0 {
		return nil, httpx.Conflict("This shortcode is deactivated, so a reference on it could never be paid")
	}
	lease, err := s.repo.GatewayLease(ctx, tx, p.OrgID, in.LeaseID, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	row := &AccountRefRow{
		OrganizationID:   p.OrgID,
		ShortcodeID:      in.ShortcodeID,
		LeaseID:          in.LeaseID,
		AccountReference: reference,
		IsActive:         1,
	}
	id, err := s.repo.InsertRef(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("This paybill already routes " + reference + " to another lease")
		}
		if rejection := checkRejection(err, "the account reference breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_account_refs.created", EntityType: "mpesa_account_refs", EntityID: id,
		NewValues: map[string]any{
			"shortcode_id":      in.ShortcodeID,
			"shortcode":         shortcode.Shortcode,
			"lease_id":          in.LeaseID,
			"lease_number":      lease.LeaseNumber,
			"account_reference": reference,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.refDetail(ctx, p.OrgID, id)
}

// DeleteRef stops routing an account number. Receipts already placed by that
// reference keep their lease: they recorded the match when they arrived, and a
// reference removed afterwards must not rewrite history.
func (s *Service) DeleteRef(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "mpesa.manage"); err != nil {
		return err
	}
	if id == 0 {
		return httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.Ref(ctx, tx, p.OrgID, id)
	if err != nil {
		return notFound(err)
	}
	if err := s.repo.DeleteRef(ctx, tx, p.OrgID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_account_refs.deleted", EntityType: "mpesa_account_refs", EntityID: id,
		OldValues: map[string]any{
			"shortcode_id":      row.ShortcodeID,
			"shortcode":         row.Shortcode,
			"lease_id":          row.LeaseID,
			"account_reference": row.AccountReference,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

func (s *Service) refDetail(ctx context.Context, orgID, id uint64) (*AccountRefDTO, error) {
	row, err := s.repo.Ref(ctx, s.db, orgID, id)
	if err != nil {
		return nil, notFound(err)
	}
	out := accountRefDTO(row)
	return &out, nil
}

func accountRefDTO(row *AccountRefRow) AccountRefDTO {
	return AccountRefDTO{
		ID:               row.ID,
		ShortcodeID:      row.ShortcodeID,
		Shortcode:        row.Shortcode,
		Environment:      row.Environment,
		LeaseID:          row.LeaseID,
		AccountReference: row.AccountReference,
		IsActive:         flag(row.IsActive),
		LeaseNumber:      nullText(row.LeaseNumber),
		UnitNumber:       nullText(row.UnitNumber),
		PropertyName:     nullText(row.PropertyName),
		TenantName:       nullText(row.TenantName),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

// cleanAccountRef trims the value a tenant types. It is kept in the caller's own
// case because that is how it is announced on a rent statement, while MySQL's
// case-insensitive collation is what makes it match either way.
func cleanAccountRef(value string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields["account_reference"] = "required"
	case len([]rune(value)) > maxAccountRef:
		fields["account_reference"] = "must be at most 100 characters"
	case strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0:
		fields["account_reference"] = "cannot contain control characters"
	}
	return value
}
