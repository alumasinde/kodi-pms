package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/auth"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
)

const (
	rolesCacheTTL     = time.Hour
	rolesCacheKeyBase = "lookup:user_roles:"
	defaultUserSort   = "u.first_name ASC, u.last_name ASC, u.id ASC"

	maxNameLength    = 100
	maxEmail         = 255
	maxPhone         = 20
	maxSearch        = 100
	maxAssignments   = 100
	minNationalIDLen = 6
	maxNationalIDLen = 10

	statusActive    = "active"
	statusInactive  = "inactive"
	statusSuspended = "suspended"
	statusInvited   = "invited"

	roleOrgAdmin    = "org_admin"
	roleSystemAdmin = "system_admin"

	assignManager   = "property_manager"
	assignCaretaker = "caretaker"
)

var (
	userStatuses      = []string{statusActive, statusInactive, statusSuspended, statusInvited}
	statusMessage     = "must be one of active, inactive, suspended, invited"
	assignmentMessage = "must be property_manager or caretaker"
	phoneRE           = regexp.MustCompile(`^\+[0-9]{7,19}$`)
	nationalIDRE      = regexp.MustCompile(`^[0-9]{6,10}$`)
	nationalIDStrip   = strings.NewReplacer(" ", "", "-", "")

	userSortColumns = map[string]string{
		"first_name":    "u.first_name",
		"last_name":     "u.last_name",
		"email":         "u.email",
		"status":        "u.status",
		"last_login_at": "u.last_login_at",
		"created_at":    "u.created_at",
		"updated_at":    "u.updated_at",
	}
)

type Service struct {
	db    *sqlx.DB
	repo  *repository
	store cache.Store
	audit *audit.Logger
	pii   *pii.Cipher
	log   *slog.Logger
}

func NewService(db *sqlx.DB, store cache.Store, auditLog *audit.Logger, piiCipher *pii.Cipher, log *slog.Logger) *Service {
	return &Service{
		db:    db,
		repo:  &repository{db: db},
		store: store,
		audit: auditLog,
		pii:   piiCipher,
		log:   log,
	}
}

func (s *Service) List(ctx context.Context, p *rbac.Principal, q ListQuery) (*UserListDTO, error) {
	if err := authorize(p, "users.view"); err != nil {
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

	rows, total, err := s.repo.List(ctx, p.OrgID, q)
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	roles, err := s.rolesByUser(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]UserSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, userSummaryDTO(&rows[i], roles[rows[i].ID]))
	}
	return &UserListDTO{Users: items, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, p *rbac.Principal, id uint64) (*UserDetailDTO, error) {
	if err := authorize(p, "users.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.detail(ctx, p.OrgID, id)
}

func (s *Service) Lookups(ctx context.Context, p *rbac.Principal) (*UserLookupsDTO, error) {
	if err := authorize(p, "users.view"); err != nil {
		return nil, err
	}
	roles, err := s.cachedRoles(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	return &UserLookupsDTO{Roles: roles}, nil
}

func (s *Service) Create(ctx context.Context, p *rbac.Principal, in CreateInput, meta ClientMeta) (*UserDetailDTO, error) {
	if err := authorize(p, "users.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	write := buildUserCreate(p.OrgID, in, fields)
	roleIDs := normalizeIDs(in.RoleIDs, fields)
	if len(roleIDs) == 0 && fields["role_ids"] == "" {
		fields["role_ids"] = "at least one role is required"
	}
	national, err := s.prepareNationalID(in.NationalID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.checkFreeIdentifiers(ctx, tx, p.OrgID, write.Email, textOf(write.Phone), national, 0, fields); err != nil {
		return nil, err
	}
	roles, err := s.resolveRoles(ctx, tx, p, roleIDs, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	id, err := s.repo.Insert(ctx, tx, write, hash, national)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("A user with this email or phone already exists")
		}
		return nil, err
	}
	if err := s.repo.ReplaceRoles(ctx, tx, id, roleIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "users.created", EntityType: "users", EntityID: id,
		NewValues: map[string]any{
			"email":                write.Email,
			"full_name":            fullName(write.FirstName, write.LastName),
			"status":               write.Status,
			"roles":                roleNames(roles),
			"national_id":          writeAuditNational(national),
			"must_change_password": write.MustChangePassword,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id)
}

func (s *Service) Update(ctx context.Context, p *rbac.Principal, id uint64, in UpdateInput, meta ClientMeta) (*UserDetailDTO, error) {
	if err := authorize(p, "users.update"); err != nil {
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

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	fields := map[string]string{}
	write, patch := applyUserPatch(row, in, fields)
	if id == p.UserID {
		if patch.rolesSet {
			fields["role_ids"] = "cannot be changed on your own account"
		}
		if patch.passwordSet {
			fields["password"] = "cannot be changed on your own account"
		}
		if patch.statusSet {
			fields["status"] = "cannot be changed on your own account"
		}
	}
	national, err := s.prepareNationalID(patch.nationalRaw, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if err := s.checkFreeIdentifiers(ctx, tx, p.OrgID, write.Email, textOf(write.Phone), national, id, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	var roles []RoleRow
	if patch.rolesSet {
		roles, err = s.resolveRoles(ctx, tx, p, patch.roleIDs, fields)
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
	}

	suspends := patch.statusSet && write.Status != statusActive
	if suspends || patch.rolesSet {
		admin, err := s.repo.IsOrgAdmin(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		stillAdmin := patch.rolesSet && !roleNamesContain(roleNames(roles), roleOrgAdmin)
		if admin && (suspends || stillAdmin) {
			return nil, s.guardLastAdmin(ctx, tx, p.OrgID, id)
		}
	}

	if patch.fieldsChanged {
		if err := s.repo.Update(ctx, tx, write); err != nil {
			if isDuplicateEntry(err) {
				return nil, httpx.Conflict("A user with this email or phone already exists")
			}
			return nil, err
		}
	}
	if patch.nationalSet {
		if err := s.repo.UpdateNationalID(ctx, tx, id, national); err != nil {
			return nil, err
		}
		patch.record("national_id", nullableValue(row.NationalIDLast4), nullableLast4(national))
	}
	if patch.rolesSet {
		oldRoles, err := s.repo.UserRoles(ctx, tx, []uint64{id})
		if err != nil {
			return nil, err
		}
		if !sameIDs(patch.roleIDs, roleIDsOf(oldRoles)) {
			if err := s.repo.ReplaceRoles(ctx, tx, id, patch.roleIDs); err != nil {
				return nil, err
			}
			patch.record("roles", roleNamesOf(oldRoles), roleNames(roles))
		}
	}
	if patch.passwordSet {
		hash, err := auth.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		if err := s.repo.SetPasswordTx(ctx, tx, id, hash); err != nil {
			return nil, err
		}
		patch.record("password", nil, "***")
	}
	if suspends || patch.passwordSet {
		if err := s.repo.RevokeSessionsTx(ctx, tx, id); err != nil {
			return nil, err
		}
		if suspends {
			if err := s.repo.BumpTokenVersionTx(ctx, tx, id); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if patch.rolesSet || suspends || patch.passwordSet {
		s.invalidate(ctx, p.OrgID, id, patch.rolesSet)
	}
	if patch.dirty() {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "users.updated", EntityType: "users", EntityID: id,
			OldValues: patch.old, NewValues: patch.new,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	return s.detail(ctx, p.OrgID, id)
}

func (s *Service) Delete(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "users.delete"); err != nil {
		return err
	}
	if id == 0 {
		return httpx.ErrNotFound
	}
	if id == p.UserID {
		return httpx.Conflict("You cannot delete your own account")
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	admin, err := s.repo.IsOrgAdmin(ctx, tx, id)
	if err != nil {
		return err
	}
	if admin && row.Status == statusActive {
		if err := s.guardLastAdmin(ctx, tx, p.OrgID, id); err != nil {
			return err
		}
	}
	if err := s.repo.SoftDeleteTx(ctx, tx, p.OrgID, id); err != nil {
		return err
	}
	if err := s.repo.RevokeSessionsTx(ctx, tx, id); err != nil {
		return err
	}
	if err := s.repo.BumpTokenVersionTx(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.invalidate(ctx, p.OrgID, id, false)
	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "users.deleted", EntityType: "users", EntityID: id,
		OldValues: map[string]any{"email": row.Email, "status": row.Status},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

func (s *Service) Assignments(ctx context.Context, p *rbac.Principal, id uint64) ([]AssignmentDTO, error) {
	if err := authorize(p, "users.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	if _, err := s.repo.Get(ctx, p.OrgID, id); errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return s.assignments(ctx, p, id, p.Scope)
}

func (s *Service) ReplaceAssignments(ctx context.Context, p *rbac.Principal, id uint64, in ReplaceAssignmentsInput, meta ClientMeta) ([]AssignmentDTO, error) {
	if err := authorize(p, "users.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	fields := map[string]string{}
	assignments, propertyIDs := normalizeAssignments(in.Assignments, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id); errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	} else if err != nil {
		return nil, err
	}

	if len(propertyIDs) > 0 {
		if !p.Scope.AllProperties && len(p.Scope.PropertyIDs) == 0 {
			return nil, httpx.Validation(map[string]string{"assignments": "no property is yours to assign"})
		}
		missing, err := s.repo.MissingProperties(ctx, tx, p.OrgID, propertyIDs, p.Scope)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			return nil, httpx.Validation(map[string]string{"assignments": "contains properties you cannot assign"})
		}
	}

	oldRows, err := s.repo.LiveAssignments(ctx, p.OrgID, id, rbac.Scope{OrgID: p.OrgID, AllProperties: true})
	if err != nil {
		return nil, err
	}
	if err := s.repo.ReplaceAssignments(ctx, tx, id, assignments); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if !sameAssignments(oldRows, assignments) {
		s.invalidate(ctx, p.OrgID, id, true)
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "users.assignments_updated", EntityType: "users", EntityID: id,
			OldValues: map[string]any{"properties": assignmentKeys(oldRows)},
			NewValues: map[string]any{"properties": inputKeys(assignments)},
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}
	return s.assignments(ctx, p, id, rbac.Scope{OrgID: p.OrgID, AllProperties: true})
}

func (s *Service) assignments(ctx context.Context, p *rbac.Principal, id uint64, scope rbac.Scope) ([]AssignmentDTO, error) {
	rows, err := s.repo.LiveAssignments(ctx, p.OrgID, id, scope)
	if err != nil {
		return nil, err
	}
	out := make([]AssignmentDTO, 0, len(rows))
	for i := range rows {
		out = append(out, assignmentDTO(&rows[i]))
	}
	return out, nil
}

func (s *Service) detail(ctx context.Context, orgID, id uint64) (*UserDetailDTO, error) {
	row, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	roles, err := s.rolesByUser(ctx, []uint64{id})
	if err != nil {
		return nil, err
	}
	return userDetailDTO(row, roles[id]), nil
}

func (s *Service) rolesByUser(ctx context.Context, ids []uint64) (map[uint64][]RoleDTO, error) {
	rows, err := s.repo.UserRoles(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64][]RoleDTO, len(ids))
	for _, r := range rows {
		out[r.UserID] = append(out[r.UserID], roleDTO(r.RoleID, r.RoleName, r.Description))
	}
	return out, nil
}

func (s *Service) cachedRoles(ctx context.Context, orgID uint64) ([]RoleDTO, error) {
	key := rolesCacheKeyBase + strconv.FormatUint(orgID, 10)
	if raw, err := s.store.Get(ctx, key); err == nil {
		var cached []RoleDTO
		if json.Unmarshal([]byte(raw), &cached) == nil {
			return cached, nil
		}
	}
	rows, err := s.repo.Roles(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]RoleDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, roleDTO(r.ID, r.Name, r.Description))
	}
	if b, err := json.Marshal(out); err == nil {
		if err := s.store.Set(ctx, key, string(b), rolesCacheTTL); err != nil {
			s.log.Debug("cache user roles", "key", key, "error", err)
		}
	}
	return out, nil
}

// resolveRoles checks that every requested role exists in this organization and
// that the caller may grant it.
func (s *Service) resolveRoles(ctx context.Context, q querier, p *rbac.Principal, roleIDs []uint64, fields map[string]string) ([]RoleRow, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	rows, err := s.repo.RolesByIDs(ctx, q, roleIDs, p.OrgID)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(roleIDs) {
		fields["role_ids"] = "contains unknown roles"
		return rows, nil
	}
	for _, r := range rows {
		if restrictedRole(r.Name) && !p.HasRole(r.Name) {
			fields["role_ids"] = fmt.Sprintf("you cannot grant the %s role", r.Name)
			return rows, nil
		}
	}
	return rows, nil
}

func (s *Service) checkFreeIdentifiers(ctx context.Context, q querier, orgID uint64, email, phone string, national *NationalIDValues, excludeID uint64, fields map[string]string) error {
	taken, err := s.repo.EmailTaken(ctx, q, orgID, email, excludeID)
	if err != nil {
		return err
	}
	if taken {
		return httpx.Conflict("A user with this email already exists")
	}
	phoneTaken, err := s.repo.PhoneTaken(ctx, q, orgID, phone, excludeID)
	if err != nil {
		return err
	}
	if phoneTaken {
		return httpx.Conflict("A user with this phone already exists")
	}
	if national.hasValue() {
		dup, err := s.repo.NationalIDTaken(ctx, q, orgID, national.hash, excludeID)
		if err != nil {
			return err
		}
		if dup {
			fields["national_id"] = "is already on another user"
		}
	}
	return nil
}

func (s *Service) guardLastAdmin(ctx context.Context, q querier, orgID, id uint64) error {
	count, err := s.repo.CountOtherActiveOrgAdmins(ctx, q, orgID, id)
	if err != nil {
		return err
	}
	if count == 0 {
		return httpx.Conflict("The organization must keep at least one active administrator")
	}
	return nil
}

// invalidate drops the cached principal so the next request reloads roles,
// status and token version.
func (s *Service) invalidate(ctx context.Context, orgID, userID uint64, rolesChanged bool) {
	if err := rbac.BumpUserAuthzVersion(ctx, s.store, userID); err != nil {
		s.log.Warn("bump user authz version", "user_id", userID, "error", err)
	}
	if rolesChanged {
		if err := rbac.BumpAuthzVersion(ctx, s.store, orgID); err != nil {
			s.log.Warn("bump org authz version", "org_id", orgID, "error", err)
		}
	}
}

func (s *Service) prepareNationalID(value string, fields map[string]string) (*NationalIDValues, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return &NationalIDValues{}, nil
	}
	if !nationalIDRE.MatchString(nationalIDStrip.Replace(value)) {
		fields["national_id"] = fmt.Sprintf("must be %d to %d digits", minNationalIDLen, maxNationalIDLen)
		return &NationalIDValues{}, nil
	}
	if s.pii == nil {
		return nil, errors.New("users: pii cipher is not configured")
	}
	enc, err := s.pii.Encrypt(value)
	if err != nil {
		return nil, err
	}
	return &NationalIDValues{ciphertext: enc, hash: s.pii.Hash(value), last4: pii.Last4(value)}, nil
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write user audit", "action", e.Action, "org_id", e.OrgID, "error", err)
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

func buildUserCreate(orgID uint64, in CreateInput, fields map[string]string) *UserWrite {
	write := &UserWrite{OrganizationID: orgID}
	write.FirstName = requiredText("first_name", in.FirstName, maxNameLength, fields)
	write.LastName = requiredText("last_name", in.LastName, maxNameLength, fields)
	write.Email = emailValue(in.Email, fields)
	write.Phone = phoneValue(in.Phone, fields)
	write.Status = statusActive
	if value := strings.ToLower(strings.TrimSpace(in.Status)); value != "" {
		if validStatus(value) {
			write.Status = value
		} else {
			fields["status"] = statusMessage
		}
	}
	if in.Password == "" {
		fields["password"] = "required"
	} else if err := auth.ValidatePassword(in.Password); err != nil {
		var e *httpx.Err
		if errors.As(err, &e) {
			for k, v := range e.Fields {
				fields[k] = v
			}
		} else {
			fields["password"] = "is not valid"
		}
	}
	// An administrator-created account has to prove it holds the password.
	write.MustChangePassword = in.MustChangePassword == nil || *in.MustChangePassword
	return write
}

type userPatch struct {
	old           map[string]any
	new           map[string]any
	roleIDs       []uint64
	nationalRaw   string
	rolesSet      bool
	nationalSet   bool
	passwordSet   bool
	statusSet     bool
	fieldsChanged bool
}

func (u *userPatch) record(field string, before, after any) {
	u.old[field] = before
	u.new[field] = after
}

func (u *userPatch) dirty() bool {
	return len(u.new) > 0
}

func applyUserPatch(row *UserRow, in UpdateInput, fields map[string]string) (*UserWrite, *userPatch) {
	write := &UserWrite{
		ID:                 row.ID,
		OrganizationID:     uint64(row.OrganizationID.Int64),
		Email:              row.Email,
		Phone:              row.Phone,
		FirstName:          row.FirstName,
		LastName:           row.LastName,
		Status:             row.Status,
		MustChangePassword: row.MustChangePassword,
	}
	patch := &userPatch{old: map[string]any{}, new: map[string]any{}}

	if value := strings.TrimSpace(in.Email); value != "" {
		next := emailValue(value, fields)
		if next != "" && next != write.Email {
			patch.record("email", write.Email, next)
			write.Email = next
			patch.fieldsChanged = true
		}
	}
	if value := strings.TrimSpace(in.FirstName); value != "" {
		next := requiredText("first_name", value, maxNameLength, fields)
		if next != "" && next != write.FirstName {
			patch.record("first_name", write.FirstName, next)
			write.FirstName = next
			patch.fieldsChanged = true
		}
	}
	if value := strings.TrimSpace(in.LastName); value != "" {
		next := requiredText("last_name", value, maxNameLength, fields)
		if next != "" && next != write.LastName {
			patch.record("last_name", write.LastName, next)
			write.LastName = next
			patch.fieldsChanged = true
		}
	}
	if in.Phone != nil {
		next := phoneValue(*in.Phone, fields)
		if fields["phone"] == "" && next != write.Phone {
			patch.record("phone", nullableValue(write.Phone), nullableValue(next))
			write.Phone = next
			patch.fieldsChanged = true
		}
	}
	if in.Status != nil {
		patch.statusSet = true
		value := strings.ToLower(strings.TrimSpace(*in.Status))
		switch {
		case value == "":
			fields["status"] = "required"
		case !validStatus(value):
			fields["status"] = statusMessage
		case value != write.Status:
			patch.record("status", write.Status, value)
			write.Status = value
			patch.fieldsChanged = true
		}
	}
	if in.MustChangePassword != nil {
		if *in.MustChangePassword != write.MustChangePassword {
			patch.record("must_change_password", write.MustChangePassword, *in.MustChangePassword)
			write.MustChangePassword = *in.MustChangePassword
			patch.fieldsChanged = true
		}
	}
	if in.Password != nil {
		patch.passwordSet = true
		if err := auth.ValidatePassword(*in.Password); err != nil {
			var e *httpx.Err
			if errors.As(err, &e) {
				for k, v := range e.Fields {
					fields[k] = v
				}
			} else {
				fields["password"] = "is not valid"
			}
		}
		// A password set by an administrator has to be replaced by its holder.
		if in.MustChangePassword == nil && !write.MustChangePassword {
			patch.record("must_change_password", write.MustChangePassword, true)
			write.MustChangePassword = true
			patch.fieldsChanged = true
		}
	}
	if len(in.NationalID) > 0 {
		patch.nationalSet = true
		if isJSONNull(in.NationalID) {
			patch.nationalRaw = ""
		} else {
			var value string
			if err := json.Unmarshal(in.NationalID, &value); err != nil {
				fields["national_id"] = "must be a string or null"
			}
			patch.nationalRaw = value
		}
	}
	if in.RoleIDs != nil {
		patch.rolesSet = true
		patch.roleIDs = normalizeIDs(*in.RoleIDs, fields)
		if len(patch.roleIDs) == 0 && fields["role_ids"] == "" {
			fields["role_ids"] = "must contain at least one role"
		}
	}
	return write, patch
}

func requiredText(field, value string, max int, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields[field] = "required"
	case utf8.RuneCountInString(value) > max:
		fields[field] = fmt.Sprintf("must be at most %d characters", max)
	}
	return value
}

func emailValue(value string, fields map[string]string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "":
		fields["email"] = "required"
	case utf8.RuneCountInString(value) > maxEmail:
		fields["email"] = fmt.Sprintf("must be at most %d characters", maxEmail)
	default:
		if addr, err := mail.ParseAddress(value); err != nil || !strings.EqualFold(addr.Address, value) {
			fields["email"] = "must be a valid email address"
		}
	}
	if _, bad := fields["email"]; bad {
		return ""
	}
	return value
}

func phoneValue(value string, fields map[string]string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	if utf8.RuneCountInString(value) > maxPhone || !phoneRE.MatchString(value) {
		fields["phone"] = "must be an E.164 number, e.g. +254712345678"
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func validStatus(value string) bool {
	for _, s := range userStatuses {
		if s == value {
			return true
		}
	}
	return false
}

func restrictedRole(name string) bool {
	return name == roleOrgAdmin || name == roleSystemAdmin
}

func normalizeIDs(ids []uint64, fields map[string]string) []uint64 {
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			fields["role_ids"] = "must contain positive identifiers"
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeAssignments(in []AssignmentInput, fields map[string]string) ([]AssignmentInput, []uint64) {
	if len(in) > maxAssignments {
		fields["assignments"] = fmt.Sprintf("must contain at most %d entries", maxAssignments)
		return nil, nil
	}
	seen := make(map[uint64]struct{}, len(in))
	out := make([]AssignmentInput, 0, len(in))
	ids := make([]uint64, 0, len(in))
	for i, item := range in {
		if item.PropertyID == 0 {
			fields[fmt.Sprintf("assignments[%d].property_id", i)] = "required"
			continue
		}
		if _, ok := seen[item.PropertyID]; ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "" {
			role = assignManager
		}
		if role != assignManager && role != assignCaretaker {
			fields[fmt.Sprintf("assignments[%d].role", i)] = assignmentMessage
			continue
		}
		seen[item.PropertyID] = struct{}{}
		out = append(out, AssignmentInput{PropertyID: item.PropertyID, Role: role})
		ids = append(ids, item.PropertyID)
	}
	return out, ids
}

func sameIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[uint64]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func sameAssignments(rows []AssignmentRow, inputs []AssignmentInput) bool {
	if len(rows) != len(inputs) {
		return false
	}
	current := make(map[uint64]string, len(rows))
	for _, r := range rows {
		current[r.PropertyID] = r.AssignmentRole
	}
	for _, in := range inputs {
		if role, ok := current[in.PropertyID]; !ok || role != in.Role {
			return false
		}
	}
	return true
}

func roleIDsOf(rows []UserRoleRow) []uint64 {
	out := make([]uint64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RoleID)
	}
	return out
}

func roleNamesOf(rows []UserRoleRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RoleName)
	}
	return out
}

func roleNames(rows []RoleRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func roleNamesContain(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func assignmentKeys(rows []AssignmentRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%d:%s", r.PropertyID, r.AssignmentRole))
	}
	return out
}

func inputKeys(inputs []AssignmentInput) []string {
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, fmt.Sprintf("%d:%s", in.PropertyID, in.Role))
	}
	return out
}

func writeAuditNational(values *NationalIDValues) any {
	if !values.hasValue() {
		return nil
	}
	return values.last4
}

func nullableLast4(values *NationalIDValues) any {
	if !values.hasValue() {
		return nil
	}
	return values.last4
}

func fullName(first, last string) string {
	return strings.TrimSpace(first + " " + last)
}

func textOf(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nullableValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

func roleDTO(id uint64, name string, description sql.NullString) RoleDTO {
	return RoleDTO{ID: id, Name: name, Description: textOf(description)}
}

func userSummaryDTO(row *UserRow, roles []RoleDTO) UserSummaryDTO {
	if roles == nil {
		roles = []RoleDTO{}
	}
	return UserSummaryDTO{
		ID:                 row.ID,
		FullName:           fullName(row.FirstName, row.LastName),
		FirstName:          row.FirstName,
		LastName:           row.LastName,
		Email:              row.Email,
		Phone:              textOf(row.Phone),
		Status:             row.Status,
		Roles:              roles,
		MustChangePassword: row.MustChangePassword,
		LastLoginAt:        timePointer(row.LastLoginAt),
		CreatedAt:          row.CreatedAt.UTC(),
		UpdatedAt:          row.UpdatedAt.UTC(),
	}
}

func userDetailDTO(row *UserRow, roles []RoleDTO) *UserDetailDTO {
	return &UserDetailDTO{
		UserSummaryDTO:  userSummaryDTO(row, roles),
		NationalIDLast4: textOf(row.NationalIDLast4),
		EmailVerifiedAt: timePointer(row.EmailVerifiedAt),
		PhoneVerifiedAt: timePointer(row.PhoneVerifiedAt),
	}
}

func assignmentDTO(row *AssignmentRow) AssignmentDTO {
	return AssignmentDTO{
		PropertyID:   row.PropertyID,
		PropertyName: row.PropertyName,
		PropertyCode: textOf(row.PropertyCode),
		Role:         row.AssignmentRole,
		AssignedAt:   row.AssignedAt.UTC(),
	}
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func isDuplicateEntry(err error) bool {
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return my.Number == 1062
	}
	return false
}
