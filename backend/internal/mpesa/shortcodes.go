package mpesa

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
)

// The three callback paths Safaricom is pointed at. They are mounted on the root
// router, outside /api/v1, because a webhook arrives with no host that resolves an
// organization and no bearer token: the shortcode row is found by the token in the
// path and nothing else.
const (
	validationPathTemplate = "/integrations/mpesa/c2b/%s/validate"
	confirmPathTemplate    = "/integrations/mpesa/c2b/%s/confirm"
	stkCallbackTemplate    = "/integrations/mpesa/stk/%s/callback"

	// tokenPlaceholder is what the lookups endpoint shows instead of a secret, so a
	// screen can explain the URL shape without ever leaking a token twice.
	tokenPlaceholder = "TOKEN"

	maxConsumerKey     = 512
	maxConsumerSecret  = 512
	maxPasskey         = 512
	maxSecurityCredLen = 1024
)

func callbackURLs(base, token string) (validation, confirmation, stk string) {
	clean := strings.TrimRight(base, "/")
	return clean + fmt.Sprintf(validationPathTemplate, token),
		clean + fmt.Sprintf(confirmPathTemplate, token),
		clean + fmt.Sprintf(stkCallbackTemplate, token)
}

func (s *Service) Shortcodes(ctx context.Context, p *rbac.Principal, q ShortcodeQuery) (*ShortcodeListDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if err := normalizeShortcodeQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListShortcodes(ctx, p.OrgID, q)
	if err != nil {
		return nil, err
	}
	items := make([]ShortcodeDTO, 0, len(rows))
	for i := range rows {
		items = append(items, s.shortcodeDTO(&rows[i]))
	}
	return &ShortcodeListDTO{Shortcodes: items, Total: total}, nil
}

func (s *Service) Shortcode(ctx context.Context, p *rbac.Principal, id uint64) (*ShortcodeDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	row, err := s.repo.Shortcode(ctx, s.db, p.OrgID, id)
	if err != nil {
		return nil, notFound(err)
	}
	out := s.shortcodeDTO(row)
	return &out, nil
}

// Lookups is the vocabulary the shortcode and reconcile screens offer, plus the
// callback paths an operator pastes into the Daraja portal. The paths carry a
// placeholder rather than a token, because a token leaves this service once.
func (s *Service) Lookups(ctx context.Context, p *rbac.Principal) (*MpesaLookupsDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	base := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	placeholder := "{" + tokenPlaceholder + "}"
	_, gatewayDisabled := s.gateway.(disabledGateway)
	return &MpesaLookupsDTO{
		Environments:       append([]string{}, environments...),
		AccountTypes:       append([]string{}, accountTypes...),
		ReceiptStatuses:    append([]string{}, transStatuses...),
		PromptStatuses:     append([]string{}, stkStatuses...),
		ValidationPath:     base + fmt.Sprintf(validationPathTemplate, placeholder),
		ConfirmationPath:   base + fmt.Sprintf(confirmPathTemplate, placeholder),
		STKCallbackPath:    base + fmt.Sprintf(stkCallbackTemplate, placeholder),
		GatewayConfigured:  !gatewayDisabled,
		BillingConnected:   s.payments != nil,
		PaymentMethodCodes: []string{methodPaybill, methodTill, methodSTK},
	}, nil
}

// CreateShortcode registers a paybill or till and hands back the one secret this
// module issues: the callback token. It is generated here, stored as a keyed HMAC,
// and returned in full once; nothing reads it back out afterwards, so an operator
// who loses it rotates rather than digs.
func (s *Service) CreateShortcode(ctx context.Context, p *rbac.Principal, in CreateShortcodeInput, meta ClientMeta) (*SecretDTO, error) {
	if err := authorize(p, "mpesa.manage"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := &ShortcodeRow{
		OrganizationID: p.OrgID,
		Shortcode:      cleanShortcode(in.Shortcode, fields),
		AccountType:    cleanChoice(in.AccountType, typePaybill, accountTypes, "account_type", accountTypeMessage, fields),
		Environment:    cleanChoice(in.Environment, envSandbox, environments, "environment", environmentMessage, fields),
		InitiatorName:  optText(cleanLabel(valueOrEmpty(in.InitiatorName), maxInitiator, "initiator_name", fields)),
		IsActive:       1,
	}
	if in.IsActive != nil && !*in.IsActive {
		row.IsActive = 0
	}
	if err := sealCredentials(row, credentialsFromCreate(in), s.cipher, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	// Credentials and token are written in one transaction: a shortcode without a
	// token is a dead row no callback can reach, and a token without a shortcode is
	// a secret with no owner.
	token, err := s.issueToken(row)
	if err != nil {
		return nil, err
	}

	id, err := s.insertShortcode(ctx, row)
	if err != nil {
		return nil, err
	}
	row.ID = id

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_shortcodes.created", EntityType: "mpesa_shortcodes", EntityID: id,
		NewValues: map[string]any{
			"shortcode":       row.Shortcode,
			"account_type":    row.AccountType,
			"environment":     row.Environment,
			"has_credentials": row.HasCredentials(),
			"is_active":       flag(row.IsActive),
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.secretDTO(ctx, p.OrgID, id, token), nil
}

// UpdateShortcode changes the routing or replaces the credentials. Every credential
// is write-only: an omitted pointer keeps what is stored, and an empty string erases
// it, because an operator who revokes a consumer key in the Daraja portal has to be
// able to take the dead one out of this database.
func (s *Service) UpdateShortcode(ctx context.Context, p *rbac.Principal, id uint64, in UpdateShortcodeInput, meta ClientMeta) (*ShortcodeDTO, error) {
	if err := authorize(p, "mpesa.manage"); err != nil {
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

	row, err := s.repo.Shortcode(ctx, tx, p.OrgID, id)
	if err != nil {
		return nil, notFound(err)
	}
	old := s.shortcodeDTO(row)

	fields := map[string]string{}
	if in.Shortcode != nil {
		row.Shortcode = cleanShortcode(*in.Shortcode, fields)
	}
	if in.AccountType != nil {
		row.AccountType = cleanChoice(*in.AccountType, row.AccountType, accountTypes, "account_type", accountTypeMessage, fields)
	}
	if in.Environment != nil {
		row.Environment = cleanChoice(*in.Environment, row.Environment, environments, "environment", environmentMessage, fields)
	}
	if in.InitiatorName != nil {
		row.InitiatorName = optText(cleanLabel(*in.InitiatorName, maxInitiator, "initiator_name", fields))
	}
	if in.IsActive != nil {
		row.IsActive = 0
		if *in.IsActive {
			row.IsActive = 1
		}
	}
	if err := sealCredentials(row, credentialsFromUpdate(in), s.cipher, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	// A number that has money routed to it is not a label. Renumbering it, moving it
	// to the other environment, or turning a paybill into a till would re-point every
	// reference, receipt and prompt already attached to this row at something nobody
	// told the tenant about, so the row is deactivated and replaced instead.
	if row.Shortcode != old.Shortcode || row.Environment != old.Environment ||
		row.AccountType != old.AccountType {
		refs, receipts, prompts, err := s.repo.ShortcodeLinks(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if refs+receipts+prompts > 0 {
			return nil, httpx.Conflict("This shortcode already has " + linkPhrase(refs, receipts, prompts) +
				" attached; deactivate it and register the new paybill instead of moving these rows onto it")
		}
	}

	if err := s.repo.UpdateShortcode(ctx, tx, row); err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict(shortcodeTakenMessage(row.Shortcode, row.Environment))
		}
		if rejection := checkRejection(err, "the shortcode breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_shortcodes.updated", EntityType: "mpesa_shortcodes", EntityID: id,
		OldValues: map[string]any{"shortcode": old.Shortcode, "account_type": old.AccountType,
			"environment": old.Environment, "is_active": old.IsActive},
		NewValues: map[string]any{"shortcode": row.Shortcode, "account_type": row.AccountType,
			"environment": row.Environment, "is_active": flag(row.IsActive)},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.shortcodeDetail(ctx, p.OrgID, id)
}

// RotateToken throws away a callback token and issues a new one. It is its own
// action rather than a field of the update, because rotating has to be unambiguous:
// no request that edits a shortcode should quietly invalidate a URL that is live in
// the Daraja portal, and no operator should mistake this for a no-op.
func (s *Service) RotateToken(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) (*SecretDTO, error) {
	if err := authorize(p, "mpesa.manage"); err != nil {
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

	row, err := s.repo.Shortcode(ctx, tx, p.OrgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	token, err := s.issueToken(row)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateShortcode(ctx, tx, row); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_shortcodes.token_rotated", EntityType: "mpesa_shortcodes", EntityID: id,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.secretDTO(ctx, p.OrgID, id, token), nil
}

// DeleteShortcode removes routing. It refuses while receipts exist, because those
// rows point at this shortcode for the account reference that placed them, and
// deleting it would orphan them or take the queue's history with it.
func (s *Service) DeleteShortcode(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
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

	row, err := s.repo.Shortcode(ctx, tx, p.OrgID, id)
	if err != nil {
		return notFound(err)
	}
	refs, receipts, prompts, err := s.repo.ShortcodeLinks(ctx, tx, id)
	if err != nil {
		return err
	}
	if refs+receipts+prompts > 0 {
		return httpx.Conflict("This shortcode still has " + linkPhrase(refs, receipts, prompts) +
			"; deactivate it instead of deleting it")
	}
	if err := s.repo.DeleteShortcode(ctx, tx, p.OrgID, id); err != nil {
		if isForeignKeyRestrict(err) {
			return httpx.Conflict("This shortcode is still referenced by M-Pesa data")
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_shortcodes.deleted", EntityType: "mpesa_shortcodes", EntityID: id,
		OldValues: map[string]any{"shortcode": row.Shortcode, "account_type": row.AccountType,
			"environment": row.Environment},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

func (s *Service) insertShortcode(ctx context.Context, row *ShortcodeRow) (uint64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	id, err := s.repo.InsertShortcode(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return 0, httpx.Conflict(shortcodeTakenMessage(row.Shortcode, row.Environment))
		}
		if rejection := checkRejection(err, "the shortcode breaks a database rule"); rejection != nil {
			return 0, rejection
		}
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) shortcodeDetail(ctx context.Context, orgID, id uint64) (*ShortcodeDTO, error) {
	row, err := s.repo.Shortcode(ctx, s.db, orgID, id)
	if err != nil {
		return nil, notFound(err)
	}
	out := s.shortcodeDTO(row)
	return &out, nil
}

// secretDTO re-reads the row so the response carries what was stored, with the raw
// token and the three ready-to-paste URLs added on top.
func (s *Service) secretDTO(ctx context.Context, orgID, id uint64, token string) *SecretDTO {
	row, err := s.repo.Shortcode(ctx, s.db, orgID, id)
	if err != nil {
		s.log.Error("read shortcode after writing it", "org_id", orgID, "shortcode_id", id, "error", err)
		return nil
	}
	validation, confirmation, stk := callbackURLs(s.cfg.PublicBaseURL, token)
	return &SecretDTO{
		ShortcodeDTO:    s.shortcodeDTO(row),
		CallbackToken:   token,
		ValidationURL:   validation,
		ConfirmationURL: confirmation,
		STKCallbackURL:  stk,
	}
}

// issueToken mints a callback token and writes both stored forms of it onto the
// row, returning the plaintext for the one response that may carry it.
//
// The hash is what a webhook is matched against. The sealed copy exists for one
// reason: an STK push has to hand Safaricom a callback URL, and a token that only
// ever exists as a hash could never be put back into a URL again.
func (s *Service) issueToken(row *ShortcodeRow) (string, error) {
	if s.cipher == nil {
		return "", httpx.Conflict("No PII key is configured, so a callback token cannot be stored")
	}
	token, err := randomHex(tokenBytes)
	if err != nil {
		return "", err
	}
	sealed, err := s.cipher.Encrypt(token)
	if err != nil {
		return "", httpx.Conflict("The callback token could not be encrypted")
	}
	row.CallbackTokenHash = sql.NullString{String: hex.EncodeToString(s.cipher.Hash(token)), Valid: true}
	row.CallbackTokenEnc = sealed
	return token, nil
}

// callbackToken reads the stored token back for an outbound STK URL. A shortcode
// with no token, or one written before the PII key changed, cannot be used to
// collect money by prompt, and says so.
func (s *Service) callbackToken(row *ShortcodeRow) (string, error) {
	if s.cipher == nil || len(row.CallbackTokenEnc) == 0 {
		return "", httpx.Conflict("This shortcode has no callback token; rotate it before sending a prompt")
	}
	token, err := s.cipher.Decrypt(row.CallbackTokenEnc)
	if err != nil {
		return "", undecryptable(err)
	}
	return token, nil
}

// tokenHash is the form a callback token is looked up in. The webhook lookup is an
// equality test against this value, so an index scan of the table answers "is this
// token known" without ever revealing a token, and hex of the 32-byte HMAC is exactly
// the 64 characters the column holds. The token is also stored sealed
// (callback_token_enc) because an outbound STK prompt must name its callback URL; see
// the note in migration 000007 on what that costs.
func (s *Service) tokenHash(token string) (string, error) {
	if s.cipher == nil {
		return "", httpx.Conflict("No PII key is configured, so a callback token cannot be stored")
	}
	sum := s.cipher.Hash(token)
	if len(sum) == 0 {
		return "", httpx.Validation(map[string]string{"callback_token": "cannot be empty"})
	}
	return hex.EncodeToString(sum), nil
}

// shortcodeByToken resolves the webhook identity. An unknown token and a disabled
// shortcode get the same answer, because the only difference a caller can probe is
// whether a token exists at all.
func (s *Service) shortcodeByToken(ctx context.Context, token string) (*ShortcodeRow, error) {
	if strings.TrimSpace(token) == "" {
		return nil, httpx.ErrNotFound
	}
	hash, err := s.tokenHash(token)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.ShortcodeByToken(ctx, hash)
	if err != nil {
		return nil, notFound(err)
	}
	if row.IsActive == 0 {
		return nil, httpx.ErrNotFound
	}
	return row, nil
}

// credentials decrypts what an STK push needs. A shortcode whose credentials cannot
// be read is reported rather than guessed at: the usual cause is a rotated PII key,
// and the fix is to type the keys in again, not to retry the prompt.
func (s *Service) credentials(row *ShortcodeRow) (Credentials, error) {
	if s.cipher == nil || !row.HasCredentials() {
		return Credentials{}, httpx.Conflict("This shortcode has no Daraja credentials, so it cannot collect money")
	}
	key, err := s.cipher.Decrypt(row.ConsumerKeyEnc)
	if err != nil {
		return Credentials{}, undecryptable(err)
	}
	secret, err := s.cipher.Decrypt(row.ConsumerSecretEnc)
	if err != nil {
		return Credentials{}, undecryptable(err)
	}
	passkey, err := s.cipher.Decrypt(row.PasskeyEnc)
	if err != nil {
		return Credentials{}, undecryptable(err)
	}
	return Credentials{ConsumerKey: key, ConsumerSecret: secret, Passkey: passkey}, nil
}

func undecryptable(err error) error {
	return httpx.Conflict("The stored Daraja keys for this shortcode cannot be decrypted; re-enter them")
}

// credentialSet is the four secrets a caller may write, in the shape both create and
// update can hand over.
type credentialSet struct {
	consumerKey, consumerSecret, passkey, securityCredential *string
}

func credentialsFromCreate(in CreateShortcodeInput) credentialSet {
	return credentialSet{in.ConsumerKey, in.ConsumerSecret, in.Passkey, in.SecurityCredential}
}

func credentialsFromUpdate(in UpdateShortcodeInput) credentialSet {
	return credentialSet{in.ConsumerKey, in.ConsumerSecret, in.Passkey, in.SecurityCredential}
}

// sealCredentials encrypts whichever credentials the caller sent.
//
// The all-or-nothing rule is the reason this is one function: a shortcode with a
// consumer key but no passkey looks configured on every screen and then fails on the
// only call that matters, so a partial set is refused rather than stored.
func sealCredentials(row *ShortcodeRow, set credentialSet, cipher *pii.Cipher, fields map[string]string) error {
	supplied := 0
	for _, value := range []*string{set.consumerKey, set.consumerSecret, set.passkey} {
		if value != nil && strings.TrimSpace(*value) != "" {
			supplied++
		}
	}
	if supplied > 0 && supplied < 3 {
		together := "the consumer key, secret and passkey are set together"
		fields["consumer_key"] = together
		fields["consumer_secret"] = together
		fields["passkey"] = together
	}
	for _, item := range []struct {
		name    string
		value   *string
		max     int
		storage *[]byte
	}{
		{"consumer_key", set.consumerKey, maxConsumerKey, &row.ConsumerKeyEnc},
		{"consumer_secret", set.consumerSecret, maxConsumerSecret, &row.ConsumerSecretEnc},
		{"passkey", set.passkey, maxPasskey, &row.PasskeyEnc},
		{"security_credential", set.securityCredential, maxSecurityCredLen, &row.SecurityCredentialEnc},
	} {
		if item.value == nil {
			continue
		}
		if len([]rune(strings.TrimSpace(*item.value))) > item.max {
			fields[item.name] = "is too long to store"
		}
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	if !anyCredential(set) {
		return nil
	}
	if cipher == nil {
		return httpx.Conflict("No PII key is configured, so M-Pesa credentials cannot be stored")
	}
	for _, item := range []struct {
		value   *string
		storage *[]byte
	}{
		{set.consumerKey, &row.ConsumerKeyEnc},
		{set.consumerSecret, &row.ConsumerSecretEnc},
		{set.passkey, &row.PasskeyEnc},
		{set.securityCredential, &row.SecurityCredentialEnc},
	} {
		if item.value == nil {
			continue
		}
		plain := strings.TrimSpace(*item.value)
		if plain == "" {
			*item.storage = nil
			continue
		}
		sealed, err := cipher.Encrypt(plain)
		if err != nil {
			return httpx.Conflict("A credential could not be encrypted")
		}
		*item.storage = sealed
	}
	return nil
}

func anyCredential(set credentialSet) bool {
	return set.consumerKey != nil || set.consumerSecret != nil || set.passkey != nil || set.securityCredential != nil
}

func (s *Service) shortcodeDTO(row *ShortcodeRow) ShortcodeDTO {
	return ShortcodeDTO{
		ID:                row.ID,
		Shortcode:         row.Shortcode,
		AccountType:       row.AccountType,
		Environment:       row.Environment,
		InitiatorName:     nullText(row.InitiatorName),
		HasConsumerKey:    len(row.ConsumerKeyEnc) > 0,
		HasConsumerSecret: len(row.ConsumerSecretEnc) > 0,
		HasPasskey:        len(row.PasskeyEnc) > 0,
		HasSecurityCred:   len(row.SecurityCredentialEnc) > 0,
		HasCallbackToken:  row.CallbackTokenHash.Valid,
		IsActive:          flag(row.IsActive),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func normalizeShortcodeQuery(q *ShortcodeQuery) error {
	fields := map[string]string{}
	q.Search = truncate(q.Search, maxSearch)
	if q.Environment != "" && !inList(q.Environment, environments) {
		fields["environment"] = environmentMessage
	}
	if q.AccountType != "" && !inList(q.AccountType, accountTypes) {
		fields["account_type"] = accountTypeMessage
	}
	switch q.Status {
	case "", "active", "inactive":
	default:
		fields["status"] = "must be one of active, inactive"
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

// cleanShortcode keeps a paybill number to the digits Daraja expects. The value is
// put into a base64 business password and into an outbound URL, so nothing that is
// not a digit is allowed to reach either.
func cleanShortcode(value string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields["shortcode"] = "required"
	case len(value) < 4 || len([]rune(value)) > maxShortcode:
		fields["shortcode"] = "must be between 4 and 20 characters"
	case strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0:
		fields["shortcode"] = "must be digits only"
	}
	return value
}

// cleanChoice reads an enumerated field, defaulting when absent and recording the
// complaint when the value is outside the schema's vocabulary.
func cleanChoice(value, fallback string, allowed []string, name, message string, fields map[string]string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	if !inList(value, allowed) {
		fields[name] = message
		return fallback
	}
	return value
}

func cleanLabel(value string, max int, name string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > max {
		fields[name] = "must be at most " + strconv.Itoa(max) + " characters"
	}
	return value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// shortcodeTakenMessage explains the one unique key this module cannot satisfy by
// waiting: the sandbox paybill Safaricom hands out is the same for every account, so
// a second organization testing on it has to use production or give up.
func shortcodeTakenMessage(shortcode, environment string) string {
	if environment == envSandbox {
		return "Another organization already routes the sandbox shortcode " + shortcode +
			"; Safaricom gives every test account the same number, so only one can hold it"
	}
	return "Another organization already routes " + shortcode + " in " + environment
}

func linkPhrase(refs, receipts, prompts int) string {
	parts := make([]string, 0, 3)
	if refs > 0 {
		parts = append(parts, strconv.Itoa(refs)+" account reference"+plural(refs))
	}
	if receipts > 0 {
		parts = append(parts, strconv.Itoa(receipts)+" receipt"+plural(receipts))
	}
	if prompts > 0 {
		parts = append(parts, strconv.Itoa(prompts)+" prompt"+plural(prompts))
	}
	return strings.Join(parts, ", ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
