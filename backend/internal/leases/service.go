package leases

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

const (
	defaultLeaseSort = "l.created_at DESC, l.id DESC"

	statusDraft            = "draft"
	statusPendingSignature = "pending_signature"
	statusActive           = "active"
	statusExpired          = "expired"
	statusTerminated       = "terminated"
	statusRenewed          = "renewed"
	unitVacant             = "vacant"
	unitOccupied           = "occupied"
	unitInactive           = "inactive"

	rolePrimary   = "primary"
	roleCoTenant  = "co_tenant"
	roleGuarantor = "guarantor"
	roleOccupant  = "occupant"

	maxLeaseNumber    = 50
	maxReason         = 255
	maxSearch         = 100
	maxLeaseTenants   = 20
	maxAmount         = 9999999999.99
	minDayOfMonth     = 1
	maxDayOfMonth     = 28
	defaultBillingDay = 1
	defaultDueDay     = 5
	defaultNoticeDays = 30
	maxNoticeDays     = 3650
	defaultMonths     = 12
	minMonths         = 1
	maxMonths         = 120

	dateLayout = "2006-01-02"

	statusMessage     = "must be one of draft, pending_signature, active, expired, terminated, renewed"
	tenantRoleMessage = "must be primary, co_tenant, guarantor or occupant"
	notEditable       = "Only a lease that has not been activated can be changed"
	alreadyHeld       = "That unit already has an active lease"
)

var (
	leaseStatuses = []string{
		statusDraft, statusPendingSignature, statusActive,
		statusExpired, statusTerminated, statusRenewed,
	}
	editableStatuses = map[string]bool{statusDraft: true, statusPendingSignature: true}
	tenantRoles      = []string{rolePrimary, roleCoTenant, roleGuarantor, roleOccupant}

	leaseSortColumns = map[string]string{
		"number":     "l.lease_number",
		"start_date": "l.start_date",
		"end_date":   "l.end_date",
		"rent":       "l.rent_amount",
		"deposit":    "l.deposit_amount",
		"status":     "l.status",
		"unit":       "u.number",
		"property":   "p.name",
		"created_at": "l.created_at",
		"updated_at": "l.updated_at",
	}
)

type Service struct {
	db    *sqlx.DB
	repo  *repository
	audit *audit.Logger
	log   *slog.Logger
}

func NewService(db *sqlx.DB, auditLog *audit.Logger, log *slog.Logger) *Service {
	return &Service{
		db:    db,
		repo:  &repository{db: db},
		audit: auditLog,
		log:   log,
	}
}

func (s *Service) List(ctx context.Context, p *rbac.Principal, q ListQuery) (*LeaseListDTO, error) {
	if err := authorize(p, "leases.view"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearch {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearch)
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !validStatus(q.Status) {
		fields["status"] = statusMessage
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	rows, total, err := s.repo.List(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]LeaseSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, leaseSummaryDTO(&rows[i]))
	}
	return &LeaseListDTO{Leases: items, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, p *rbac.Principal, id uint64) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Tenants(ctx context.Context, p *rbac.Principal, id uint64) ([]LeaseTenantDTO, error) {
	if err := authorize(p, "leases.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	if _, err := s.repo.Get(ctx, p.OrgID, id, p.Scope); errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.repo.Roster(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return tenantDTOs(rows), nil
}

func (s *Service) Create(ctx context.Context, p *rbac.Principal, in CreateInput, meta ClientMeta) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := buildLeaseCreate(p.OrgID, p.UserID, in, fields)
	tenants := normalizeTenants(in.Tenants, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	unit, err := s.leaseableUnit(ctx, tx, p, row.UnitID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if err := s.checkTenants(ctx, tx, p.OrgID, tenants, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if held, err := s.repo.ActiveLeaseOnUnit(ctx, tx, unit.ID, 0); err != nil {
		return nil, err
	} else if held != "" {
		return nil, httpx.Conflict(alreadyHeld + " (" + held + ")")
	}

	if row.LeaseNumber == "" {
		number, err := s.repo.NextLeaseNumber(ctx, tx, p.OrgID, numberPrefix(nullText(unit.PropertyCode)), row.StartDate.Year())
		if err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(number) > maxLeaseNumber {
			return nil, httpx.Validation(map[string]string{
				"lease_number": fmt.Sprintf("generated number is longer than %d characters, send your own", maxLeaseNumber)})
		}
		row.LeaseNumber = number
	} else if taken, err := s.repo.LeaseNumberTaken(ctx, tx, p.OrgID, row.LeaseNumber, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, httpx.Conflict("A lease with that number already exists")
	}

	id, err := s.repo.Insert(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("A lease with that number already exists")
		}
		return nil, err
	}
	if err := s.repo.ReplaceRoster(ctx, tx, p.OrgID, id, tenants); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "leases.created", EntityType: "leases", EntityID: id,
		NewValues: map[string]any{
			"lease_number":   row.LeaseNumber,
			"unit_id":        row.UnitID,
			"start_date":     row.StartDate.Format(dateLayout),
			"end_date":       formatDate(row.EndDate),
			"rent_amount":    row.RentAmount,
			"deposit_amount": row.DepositAmount,
			"status":         row.Status,
			"tenants":        tenantIDs(tenants),
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Update(ctx context.Context, p *rbac.Principal, id uint64, in UpdateInput, meta ClientMeta) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !editableStatuses[row.Status] {
		return nil, httpx.Conflict(notEditable)
	}

	fields := map[string]string{}
	originalUnit := row.UnitID
	originalNumber := row.LeaseNumber
	oldValues, newValues := applyLeasePatch(row, in, fields)

	var tenants []TenantRef
	tenantsSet := in.Tenants != nil
	if tenantsSet {
		tenants = normalizeTenants(*in.Tenants, fields)
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if row.UnitID != originalUnit {
		unit, err := s.leaseableUnit(ctx, tx, p, row.UnitID, fields)
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
		if held, err := s.repo.ActiveLeaseOnUnit(ctx, tx, unit.ID, id); err != nil {
			return nil, err
		} else if held != "" {
			return nil, httpx.Conflict(alreadyHeld + " (" + held + ")")
		}
	}
	if row.LeaseNumber != originalNumber {
		taken, err := s.repo.LeaseNumberTaken(ctx, tx, p.OrgID, row.LeaseNumber, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("A lease with that number already exists")
		}
	}
	if tenantsSet {
		if err := s.checkTenants(ctx, tx, p.OrgID, tenants, fields); err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
	}

	if len(newValues) > 0 {
		if err := s.repo.Update(ctx, tx, row); err != nil {
			if isDuplicateEntry(err) {
				return nil, httpx.Conflict("A lease with that number already exists")
			}
			return nil, err
		}
	}
	if tenantsSet {
		oldRows, err := s.repo.Roster(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !sameRoster(oldRows, tenants) {
			if err := s.repo.ReplaceRoster(ctx, tx, p.OrgID, id, tenants); err != nil {
				return nil, err
			}
			oldValues["tenants"] = tenantIDs(rosterRefs(oldRows))
			newValues["tenants"] = tenantIDs(tenants)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if len(newValues) > 0 {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "leases.updated", EntityType: "leases", EntityID: id,
			OldValues: oldValues, NewValues: newValues,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Delete(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "leases.delete"); err != nil {
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

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Status != statusDraft {
		return httpx.Conflict("Only a draft lease can be deleted")
	}
	affected, err := s.repo.Delete(ctx, tx, p.OrgID, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if affected == 0 {
		return httpx.Conflict("Only a draft lease can be deleted")
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "leases.deleted", EntityType: "leases", EntityID: id,
		OldValues: map[string]any{"lease_number": row.LeaseNumber, "unit_id": row.UnitID},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

// Activate puts the lease in force: the unit becomes occupied and the deposit
// the tenant owes opens as a pending record, both in the same transaction.
// Occupancy is why only this path can set occupied.
func (s *Service) Activate(ctx context.Context, p *rbac.Principal, id uint64, in ActivateInput, meta ClientMeta) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.activate"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	fields := map[string]string{}
	signedAt, signedSet := parseTimestampField("signed_at", in.SignedAt, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != statusDraft && row.Status != statusPendingSignature {
		return nil, httpx.Conflict("Only a draft or pending-signature lease can be activated")
	}
	previous := row.Status

	unit, err := s.leaseableUnit(ctx, tx, p, row.UnitID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if held, err := s.repo.ActiveLeaseOnUnit(ctx, tx, unit.ID, row.ID); err != nil {
		return nil, err
	} else if held != "" {
		return nil, httpx.Conflict(alreadyHeld + " (" + held + ")")
	}
	roster, err := s.repo.Roster(ctx, tx, row.ID)
	if err != nil {
		return nil, err
	}
	if countRole(roster, rolePrimary) == 0 {
		fields["tenants"] = "a primary tenant is required to activate"
	}
	if row.RentAmount <= 0 {
		fields["rent_amount"] = "must be greater than zero to activate"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	hasDeposit, err := s.repo.HasDeposit(ctx, tx, p.OrgID, row.ID)
	if err != nil {
		return nil, err
	}

	signed := time.Now().UTC()
	if signedSet {
		signed = signedAt
	}
	row.Status = statusActive
	row.SignedAt = sql.NullTime{Time: signed, Valid: true}
	if err := s.repo.Update(ctx, tx, row); err != nil {
		return nil, err
	}
	depositOpened := pendingDeposit(row.DepositAmount, hasDeposit)
	if depositOpened {
		if err := s.repo.OpenDeposit(ctx, tx, p.OrgID, row.ID, row.DepositAmount); err != nil {
			return nil, err
		}
	}
	if err := s.repo.SetUnitStatus(ctx, tx, p.OrgID, row.UnitID, unitOccupied); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "leases.activated", EntityType: "leases", EntityID: id,
		OldValues: map[string]any{"status": previous},
		NewValues: map[string]any{"status": statusActive, "signed_at": signed.Format(time.RFC3339),
			"unit_id": row.UnitID, "deposit_opened": depositOpened},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// Terminate ends a live lease and frees the unit for the next tenancy.
func (s *Service) Terminate(ctx context.Context, p *rbac.Principal, id uint64, in TerminateInput, meta ClientMeta) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.terminate"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	fields := map[string]string{}
	reason := strings.TrimSpace(in.Reason)
	switch {
	case reason == "":
		fields["reason"] = "required"
	case utf8.RuneCountInString(reason) > maxReason:
		fields["reason"] = fmt.Sprintf("must be at most %d characters", maxReason)
	case strings.IndexFunc(reason, unicode.IsControl) >= 0:
		fields["reason"] = "cannot contain control characters"
	}
	at, atSet := parseTimestampField("terminated_at", in.TerminatedAt, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != statusActive && row.Status != statusExpired {
		return nil, httpx.Conflict("Only an active or expired lease can be terminated")
	}
	previous := row.Status
	if atSet && at.Before(row.StartDate) {
		return nil, httpx.Validation(map[string]string{"terminated_at": "cannot be before the lease start date"})
	}

	terminatedAt := time.Now().UTC()
	if atSet {
		terminatedAt = at
	}
	row.Status = statusTerminated
	row.TerminatedAt = sql.NullTime{Time: terminatedAt, Valid: true}
	row.TerminationReason = sql.NullString{String: reason, Valid: true}
	if err := s.repo.Update(ctx, tx, row); err != nil {
		return nil, err
	}
	if err := s.repo.SetUnitStatus(ctx, tx, p.OrgID, row.UnitID, unitVacant); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "leases.terminated", EntityType: "leases", EntityID: id,
		OldValues: map[string]any{"status": previous},
		NewValues: map[string]any{"status": statusTerminated, "reason": reason,
			"terminated_at": terminatedAt.Format(time.RFC3339)},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// Renew keeps one tenant group on the same unit without a gap: the old lease
// becomes renewed, which releases the generated active-unit key, and the new
// lease is inserted as active in the same transaction. Money already held stays
// recorded against the lease it was collected on, so renewal opens nothing.
func (s *Service) Renew(ctx context.Context, p *rbac.Principal, id uint64, in RenewInput, meta ClientMeta) (*LeaseDetailDTO, error) {
	if err := authorize(p, "leases.renew"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	fields := map[string]string{}
	start, startSet := parseOptionalDate("start_date", in.StartDate, fields)
	end, endSet := parseOptionalDate("end_date", in.EndDate, fields)
	signedAt, signedSet := parseTimestampField("signed_at", in.SignedAt, fields)
	months := defaultMonths
	if in.Months != nil {
		if *in.Months < minMonths || *in.Months > maxMonths {
			fields["months"] = fmt.Sprintf("must be between %d and %d", minMonths, maxMonths)
		} else {
			months = *in.Months
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != statusActive && row.Status != statusExpired {
		return nil, httpx.Conflict("Only an active or expired lease can be renewed")
	}

	switch {
	case startSet:
	case row.EndDate.Valid:
		start = row.EndDate.Time.AddDate(0, 0, 1)
	default:
		return nil, httpx.Validation(map[string]string{
			"start_date": "required because the current lease has no end date"})
	}
	if !endSet {
		end = start.AddDate(0, months, -1)
		endSet = true
	}
	if end.Before(start) {
		return nil, httpx.Validation(map[string]string{"end_date": "must be on or after the start date"})
	}

	unit, err := s.leaseableUnit(ctx, tx, p, row.UnitID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	rent := row.RentAmount
	if in.RentAmount != nil {
		rent = roundToCents(*in.RentAmount)
		if rent <= 0 || rent > maxAmount {
			return nil, httpx.Validation(map[string]string{
				"rent_amount": fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount)})
		}
	}
	deposit := row.DepositAmount
	if in.DepositAmount != nil {
		deposit = roundToCents(*in.DepositAmount)
		if deposit < 0 || deposit > maxAmount {
			return nil, httpx.Validation(map[string]string{
				"deposit_amount": fmt.Sprintf("must be between 0 and %.2f", maxAmount)})
		}
	}

	next := &LeaseRow{
		OrganizationID:   p.OrgID,
		UnitID:           row.UnitID,
		StartDate:        start,
		EndDate:          sql.NullTime{Time: end, Valid: endSet},
		RentAmount:       rent,
		DepositAmount:    deposit,
		BillingDay:       row.BillingDay,
		DueDay:           row.DueDay,
		NoticePeriodDays: row.NoticePeriodDays,
		Status:           statusActive,
		SignedAt:         sql.NullTime{Time: time.Now().UTC(), Valid: true},
		CreatedBy:        sql.NullInt64{Int64: int64(p.UserID), Valid: true},
	}
	if signedSet {
		next.SignedAt = sql.NullTime{Time: signedAt, Valid: true}
	}
	if in.LeaseNumber != nil {
		number := leaseNumber(*in.LeaseNumber, fields)
		if number == "" {
			fields["lease_number"] = "required"
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
		next.LeaseNumber = number
	} else {
		number, err := s.repo.NextLeaseNumber(ctx, tx, p.OrgID, numberPrefix(nullText(unit.PropertyCode)), start.Year())
		if err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(number) > maxLeaseNumber {
			return nil, httpx.Validation(map[string]string{
				"lease_number": fmt.Sprintf("generated number is longer than %d characters, send your own", maxLeaseNumber)})
		}
		next.LeaseNumber = number
	}

	roster, err := s.repo.Roster(ctx, tx, row.ID)
	if err != nil {
		return nil, err
	}
	if countRole(roster, rolePrimary) == 0 {
		return nil, httpx.Conflict("The lease being renewed has no primary tenant")
	}

	oldNumber := row.LeaseNumber
	row.Status = statusRenewed
	if err := s.repo.Update(ctx, tx, row); err != nil {
		return nil, err
	}

	newID, err := s.repo.Insert(ctx, tx, next)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("A lease with that number already exists")
		}
		return nil, err
	}
	if err := s.repo.ReplaceRoster(ctx, tx, p.OrgID, newID, rosterRefs(roster)); err != nil {
		return nil, err
	}
	if err := s.repo.SetUnitStatus(ctx, tx, p.OrgID, row.UnitID, unitOccupied); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "leases.renewed", EntityType: "leases", EntityID: newID,
		OldValues: map[string]any{"lease_id": row.ID, "lease_number": oldNumber, "status": statusRenewed},
		NewValues: map[string]any{
			"lease_number": next.LeaseNumber,
			"start_date":   next.StartDate.Format(dateLayout),
			"end_date":     formatDate(next.EndDate),
			"rent_amount":  next.RentAmount,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, newID, p.Scope)
}

func (s *Service) ReplaceTenants(ctx context.Context, p *rbac.Principal, id uint64, in ReplaceTenantsInput, meta ClientMeta) ([]LeaseTenantDTO, error) {
	if err := authorize(p, "leases.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	fields := map[string]string{}
	entries := normalizeTenants(in.Tenants, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.checkTenants(ctx, tx, p.OrgID, entries, fields); err != nil {
		return nil, err
	}
	// A lease already in force must keep exactly one primary tenant; a draft or
	// pending lease can be re-staffed freely.
	if !editableStatuses[row.Status] && countRefs(entries, rolePrimary) != 1 {
		fields["tenants"] = "exactly one primary tenant is required once the lease is in force"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	oldRows, err := s.repo.Roster(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	changed := !sameRoster(oldRows, entries)
	if changed {
		if err := s.repo.ReplaceRoster(ctx, tx, p.OrgID, id, entries); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if changed {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "leases.tenants_updated", EntityType: "leases", EntityID: id,
			OldValues: map[string]any{"tenants": tenantIDs(rosterRefs(oldRows))},
			NewValues: map[string]any{"tenants": tenantIDs(entries)},
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	rows, err := s.repo.Roster(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return tenantDTOs(rows), nil
}

// leaseableUnit returns the unit a lease may be written against, or records the
// reason it may not. A unit outside the caller's scope reads as unknown so the
// response cannot be used to probe other managers' buildings.
func (s *Service) leaseableUnit(ctx context.Context, q querier, p *rbac.Principal, unitID uint64, fields map[string]string) (*UnitRefRow, error) {
	unit, err := s.repo.UnitForScope(ctx, q, p.OrgID, unitID, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		fields["unit_id"] = "must be a unit you can manage"
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if unit.UnitDeletedAt.Valid || unit.PropertyDeletedAt.Valid {
		fields["unit_id"] = "the unit is archived"
		return unit, nil
	}
	if unit.Status == unitInactive {
		fields["unit_id"] = "the unit is marked inactive"
	}
	return unit, nil
}

func (s *Service) checkTenants(ctx context.Context, q querier, orgID uint64, entries []TenantRef, fields map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	missing, err := s.repo.MissingTenants(ctx, q, orgID, tenantIDs(entries))
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		fields["tenants"] = "contains tenants that are not in this organization"
	}
	return nil
}

func (s *Service) detail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*LeaseDetailDTO, error) {
	row, err := s.repo.Get(ctx, orgID, id, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	tenants, err := s.repo.Roster(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	deposits, err := s.repo.Deposits(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	out := leaseDetailDTO(row, tenantDTOs(tenants), depositDTOs(deposits))
	return &out, nil
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write lease audit", "action", e.Action, "org_id", e.OrgID, "error", err)
	}
}

func authorize(p *rbac.Principal, permission string) error {
	if p == nil || p.OrgID == 0 {
		return httpx.ErrUnauthorized
	}
	if !p.Can(permission) {
		return httpx.ErrForbidden
	}
	return nil
}

func buildLeaseCreate(orgID, createdBy uint64, in CreateInput, fields map[string]string) *LeaseRow {
	row := &LeaseRow{
		OrganizationID:   orgID,
		UnitID:           in.UnitID,
		Status:           statusDraft,
		BillingDay:       defaultBillingDay,
		DueDay:           defaultDueDay,
		NoticePeriodDays: defaultNoticeDays,
		CreatedBy:        sql.NullInt64{Int64: int64(createdBy), Valid: true},
	}
	if in.UnitID == 0 {
		fields["unit_id"] = "required"
	}
	if in.StartDate == "" {
		fields["start_date"] = "required"
	} else if start, ok := parseDate(in.StartDate); ok {
		row.StartDate = start
	} else {
		fields["start_date"] = "must be a date formatted as " + dateLayout
	}
	if in.EndDate != nil && *in.EndDate != "" {
		end, ok := parseDate(*in.EndDate)
		switch {
		case !ok:
			fields["end_date"] = "must be a date formatted as " + dateLayout
		case row.StartDate.IsZero():
		case end.Before(row.StartDate):
			fields["end_date"] = "must be on or after start_date"
		default:
			row.EndDate = sql.NullTime{Time: end, Valid: true}
		}
	}
	row.LeaseNumber = leaseNumber(in.LeaseNumber, fields)

	rent := roundToCents(in.RentAmount)
	if rent <= 0 || rent > maxAmount {
		fields["rent_amount"] = fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount)
	} else {
		row.RentAmount = rent
	}
	row.DepositAmount = 0
	if in.DepositAmount != nil {
		deposit := roundToCents(*in.DepositAmount)
		if deposit < 0 || deposit > maxAmount {
			fields["deposit_amount"] = fmt.Sprintf("must be between 0 and %.2f", maxAmount)
		} else {
			row.DepositAmount = deposit
		}
	}
	row.BillingDay = dayField("billing_day", in.BillingDay, defaultBillingDay, fields)
	row.DueDay = dayField("due_day", in.DueDay, defaultDueDay, fields)
	if in.NoticePeriodDays != nil {
		if *in.NoticePeriodDays < 0 || *in.NoticePeriodDays > maxNoticeDays {
			fields["notice_period_days"] = fmt.Sprintf("must be between 0 and %d", maxNoticeDays)
		} else {
			row.NoticePeriodDays = *in.NoticePeriodDays
		}
	}
	return row
}

func applyLeasePatch(row *LeaseRow, in UpdateInput, fields map[string]string) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, before, after any) {
		oldValues[name] = before
		newValues[name] = after
	}

	if in.UnitID != nil && *in.UnitID != row.UnitID {
		if *in.UnitID == 0 {
			fields["unit_id"] = "required"
		} else {
			record("unit_id", row.UnitID, *in.UnitID)
			row.UnitID = *in.UnitID
		}
	}
	if in.LeaseNumber != nil {
		number := leaseNumber(*in.LeaseNumber, fields)
		switch {
		case fields["lease_number"] != "":
		case number == "":
			fields["lease_number"] = "required"
		case number != row.LeaseNumber:
			record("lease_number", row.LeaseNumber, number)
			row.LeaseNumber = number
		}
	}
	if in.StartDate != nil {
		if start, ok := parseDate(*in.StartDate); !ok {
			fields["start_date"] = "must be a date formatted as " + dateLayout
		} else if !start.Equal(row.StartDate) {
			record("start_date", row.StartDate.Format(dateLayout), start.Format(dateLayout))
			row.StartDate = start
		}
	}
	if hasRaw(in.EndDate) {
		before := formatDate(row.EndDate)
		if isJSONNull(in.EndDate) {
			row.EndDate = sql.NullTime{}
		} else {
			var raw string
			if err := json.Unmarshal(in.EndDate, &raw); err != nil {
				fields["end_date"] = "must be a date formatted as " + dateLayout
			} else if end, ok := parseDate(raw); !ok {
				fields["end_date"] = "must be a date formatted as " + dateLayout
			} else {
				row.EndDate = sql.NullTime{Time: end, Valid: true}
			}
		}
		if fields["end_date"] == "" && before != formatDate(row.EndDate) {
			record("end_date", before, formatDate(row.EndDate))
		}
	}
	if fields["end_date"] == "" && row.EndDate.Valid && row.EndDate.Time.Before(row.StartDate) {
		fields["end_date"] = "must be on or after start_date"
	}
	if in.RentAmount != nil {
		rent := roundToCents(*in.RentAmount)
		if rent <= 0 || rent > maxAmount {
			fields["rent_amount"] = fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount)
		} else if rent != row.RentAmount {
			record("rent_amount", row.RentAmount, rent)
			row.RentAmount = rent
		}
	}
	if in.DepositAmount != nil {
		deposit := roundToCents(*in.DepositAmount)
		if deposit < 0 || deposit > maxAmount {
			fields["deposit_amount"] = fmt.Sprintf("must be between 0 and %.2f", maxAmount)
		} else if deposit != row.DepositAmount {
			record("deposit_amount", row.DepositAmount, deposit)
			row.DepositAmount = deposit
		}
	}
	if in.BillingDay != nil {
		if value := dayField("billing_day", in.BillingDay, row.BillingDay, fields); fields["billing_day"] == "" && value != row.BillingDay {
			record("billing_day", row.BillingDay, value)
			row.BillingDay = value
		}
	}
	if in.DueDay != nil {
		if value := dayField("due_day", in.DueDay, row.DueDay, fields); fields["due_day"] == "" && value != row.DueDay {
			record("due_day", row.DueDay, value)
			row.DueDay = value
		}
	}
	if in.NoticePeriodDays != nil {
		if *in.NoticePeriodDays < 0 || *in.NoticePeriodDays > maxNoticeDays {
			fields["notice_period_days"] = fmt.Sprintf("must be between 0 and %d", maxNoticeDays)
		} else if *in.NoticePeriodDays != row.NoticePeriodDays {
			record("notice_period_days", row.NoticePeriodDays, *in.NoticePeriodDays)
			row.NoticePeriodDays = *in.NoticePeriodDays
		}
	}
	return oldValues, newValues
}

// dayField keeps a 1-28 billing or due day; the schema rejects 29-31 so month
// ends are handled by the billing run, not by these columns.
func dayField(name string, value *int, fallback int, fields map[string]string) int {
	if value == nil {
		return fallback
	}
	if *value < minDayOfMonth || *value > maxDayOfMonth {
		fields[name] = fmt.Sprintf("must be between %d and %d", minDayOfMonth, maxDayOfMonth)
		return fallback
	}
	return *value
}

func leaseNumber(value string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
	case utf8.RuneCountInString(value) > maxLeaseNumber:
		fields["lease_number"] = fmt.Sprintf("must be at most %d characters", maxLeaseNumber)
	case strings.IndexFunc(value, unicode.IsControl) >= 0:
		fields["lease_number"] = "cannot contain control characters"
	}
	return value
}

// normalizeTenants keeps one entry per tenant, defaults the role to primary
// for the first one and refuses a second primary.
func normalizeTenants(refs []TenantRef, fields map[string]string) []TenantRef {
	if len(refs) > maxLeaseTenants {
		fields["tenants"] = fmt.Sprintf("cannot list more than %d tenants", maxLeaseTenants)
		return nil
	}
	seen := make(map[uint64]struct{}, len(refs))
	out := make([]TenantRef, 0, len(refs))
	primaries := 0
	for _, ref := range refs {
		if ref.TenantID == 0 {
			fields["tenants"] = "must use tenant identifiers greater than zero"
			continue
		}
		if _, ok := seen[ref.TenantID]; ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(ref.Role))
		if role == "" {
			role = rolePrimary
		}
		if !validRole(role) {
			fields["tenants"] = tenantRoleMessage
			continue
		}
		if role == rolePrimary {
			primaries++
			if primaries > 1 {
				fields["tenants"] = "only one tenant can be the primary tenant"
				continue
			}
		}
		seen[ref.TenantID] = struct{}{}
		out = append(out, TenantRef{TenantID: ref.TenantID, Role: role})
	}
	return out
}

func validStatus(value string) bool {
	for _, s := range leaseStatuses {
		if s == value {
			return true
		}
	}
	return false
}

func validRole(value string) bool {
	for _, r := range tenantRoles {
		if r == value {
			return true
		}
	}
	return false
}

func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(dateLayout, value); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func parseOptionalDate(name string, value *string, fields map[string]string) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	parsed, ok := parseDate(*value)
	if !ok {
		fields[name] = "must be a date formatted as " + dateLayout
	}
	return parsed, ok
}

func parseTimestampField(name string, value *string, fields map[string]string) (time.Time, bool) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return time.Time{}, false
	}
	parsed, ok := parseDate(*value)
	if !ok {
		fields[name] = "must be a date or an RFC3339 timestamp"
	}
	return parsed, ok
}

func numberPrefix(propertyCode string) string {
	code := strings.TrimSpace(propertyCode)
	if code == "" {
		return "L"
	}
	return code + "-L"
}

func formatDate(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.Format(dateLayout)
}

func tenantIDs(refs []TenantRef) []uint64 {
	out := make([]uint64, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.TenantID)
	}
	return out
}

func rosterRefs(rows []TenantRosterRow) []TenantRef {
	out := make([]TenantRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, TenantRef{TenantID: row.TenantID, Role: row.Role})
	}
	return out
}

func countRole(rows []TenantRosterRow, role string) int {
	total := 0
	for _, row := range rows {
		if row.Role == role {
			total++
		}
	}
	return total
}

func countRefs(rows []TenantRef, role string) int {
	total := 0
	for _, row := range rows {
		if row.Role == role {
			total++
		}
	}
	return total
}

func sameRoster(rows []TenantRosterRow, refs []TenantRef) bool {
	if len(rows) != len(refs) {
		return false
	}
	current := make(map[uint64]string, len(rows))
	for _, row := range rows {
		current[row.TenantID] = row.Role
	}
	for _, ref := range refs {
		if role, ok := current[ref.TenantID]; !ok || role != ref.Role {
			return false
		}
	}
	return true
}

func roundToCents(value float64) float64 {
	return math.Round(value*100) / 100
}

func hasRaw(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte{})
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func nullText(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

func isDuplicateEntry(err error) bool {
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return my.Number == 1062
	}
	return false
}

func leaseSummaryDTO(row *LeaseViewRow) LeaseSummaryDTO {
	return LeaseSummaryDTO{
		ID:            row.ID,
		LeaseNumber:   row.LeaseNumber,
		Unit:          UnitRefDTO{ID: row.UnitID, Number: row.UnitNumber, Property: propertyDTO(row)},
		StartDate:     row.StartDate.UTC(),
		EndDate:       timePointer(row.EndDate),
		RentAmount:    row.RentAmount,
		DepositAmount: row.DepositAmount,
		Status:        row.Status,
		PrimaryTenant: nullText(row.PrimaryTenant),
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
}

func leaseDetailDTO(row *LeaseViewRow, tenants []LeaseTenantDTO, deposits []DepositDTO) LeaseDetailDTO {
	return LeaseDetailDTO{
		LeaseSummaryDTO:   leaseSummaryDTO(row),
		BillingDay:        row.BillingDay,
		DueDay:            row.DueDay,
		NoticePeriodDays:  row.NoticePeriodDays,
		SignedAt:          timePointer(row.SignedAt),
		TerminatedAt:      timePointer(row.TerminatedAt),
		TerminationReason: nullText(row.TerminationReason),
		Tenants:           tenants,
		Deposits:          deposits,
	}
}

func propertyDTO(row *LeaseViewRow) PropertyRefDTO {
	return PropertyRefDTO{ID: row.PropertyID, Name: row.PropertyName, Code: nullText(row.PropertyCode)}
}

func tenantDTOs(rows []TenantRosterRow) []LeaseTenantDTO {
	out := make([]LeaseTenantDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, LeaseTenantDTO{
			TenantID: row.TenantID,
			Role:     row.Role,
			Name:     row.FullName,
			Email:    row.Email,
			Phone:    nullText(row.Phone),
		})
	}
	return out
}

func depositDTOs(rows []DepositRow) []DepositDTO {
	out := make([]DepositDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, DepositDTO{
			ID:       row.ID,
			Amount:   row.Amount,
			DatePaid: timePointer(row.DatePaid),
			Status:   row.Status,
		})
	}
	return out
}
