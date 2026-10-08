package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/crypto"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// openSSOSecret decrypts conn.OIDCClientSecret in place after a read. A
// nil pointer stays nil (the field is "cleared" / NULL); a legacy
// plaintext value passes through unchanged. Returns an error only when a
// sealed value cannot be opened (wrong or missing key).
func openSSOSecret(conn *model.SSOConnection) error {
	if conn.OIDCClientSecret == nil {
		return nil
	}
	secret, err := crypto.Open(*conn.OIDCClientSecret)
	if err != nil {
		return err
	}
	conn.OIDCClientSecret = &secret
	return nil
}

// sealSSOSecret returns the OIDC client secret sealed for storage, or nil
// when the field is nil (NULL). Symmetric with openSSOSecret.
func sealSSOSecret(conn *model.SSOConnection) *string {
	if conn.OIDCClientSecret == nil {
		return nil
	}
	sealed := crypto.Seal(*conn.OIDCClientSecret)
	return &sealed
}

// ─── Enterprise SSO + SCIM groundwork (Phase 8, slice 1) ───
//
// The persistence half of admin-managed SSO connection configuration
// (SAML + OIDC) and SCIM provisioning tokens. See model/sso.go for the
// full rationale and the documented future seams (SAML handshake, OIDC
// authorization-code flow, SCIM 2.0 /Users + /Groups sync). None of that
// runtime exists here — this is configuration and token lifecycle only.
//
// Token handling is the CustomerAPIKey recipe: a SCIM token is a bearer
// credential, stored as a SHA-256 hash with a display prefix; the
// plaintext is returned exactly once by CreateSCIMToken and never
// recoverable afterwards. The OIDC client secret, by contrast, is stored
// plaintext (it must be presented back to the IdP verbatim) — see the
// model's storage note.

// Refusals the unique indexes and the config CHECK give, folded out of
// the raw driver error so callers can answer 409 for a state conflict,
// 400 for bad config, and 500 for anything else without knowing what a
// SQLSTATE is. Each has a matching Is… question where a conflict can
// arrive in two spellings; both mirror the reseller/affiliate precedents.

var (
	// ErrSSONameTaken is the unique index on sso_connections.name
	// refusing a second connection with one display label.
	ErrSSONameTaken = errors.New("sso connection name already exists")

	// ErrSSODomainTaken is the unique index on sso_connections.domain
	// refusing a second connection for one email domain. One domain maps
	// to exactly one connection, so a login can never route ambiguously.
	ErrSSODomainTaken = errors.New("sso connection domain already exists")

	// ErrSSOInvalidConfig is the Go twin of the migration's
	// sso_connections_provider_config_check CHECK: the connection is
	// missing a setting its provider type requires (a saml connection
	// with no certificate, an oidc one with no issuer, …) or its domain
	// is not shaped like a domain. The handler validates first and
	// answers 400 with a human message; this is the store-side backstop
	// so a caller that skipped the handler cannot insert a row the
	// database would reject.
	ErrSSOInvalidConfig = errors.New("invalid sso connection configuration")

	// ErrSCIMTokenRevoked is RevokeSCIMToken refusing to re-revoke a
	// token that is already dead — the admin is told it was already
	// revoked rather than shown a silent no-op that looks like a fresh
	// action. It doubles as the refusal a future SCIM auth middleware
	// will surface for a revoked bearer token.
	ErrSCIMTokenRevoked = errors.New("scim token is revoked")
)

// The two unique-constraint names, for telling the two SSO conflicts
// apart from a raw 23505. Named explicitly in the migration
// (sso_connections_name_key / sso_connections_domain_key) so this match
// is reliable; the same substring technique as isStripePriceConflict and
// feedGateConflict in handler/admin.go.
const (
	ssoNameConstraint   = "sso_connections_name_key"
	ssoDomainConstraint = "sso_connections_domain_key"
)

// IsSSONameConflict reports whether err is the name-taken refusal, in
// either spelling: the sentinel the write paths return, or a raw unique
// violation on the name constraint that reached the caller. Mirrors
// IsResellerEmailConflict, narrowed to the one constraint because this
// table carries two uniques.
func IsSSONameConflict(err error) bool {
	if errors.Is(err, ErrSSONameTaken) {
		return true
	}
	return isUniqueViolation(err) && strings.Contains(err.Error(), ssoNameConstraint)
}

// IsSSODomainConflict reports whether err is the domain-taken refusal,
// in either spelling (sentinel or a raw unique violation on the domain
// constraint).
func IsSSODomainConflict(err error) bool {
	if errors.Is(err, ErrSSODomainTaken) {
		return true
	}
	return isUniqueViolation(err) && strings.Contains(err.Error(), ssoDomainConstraint)
}

// ssoConflict folds a unique-constraint violation into the right typed
// refusal: the domain constraint names ErrSSODomainTaken, anything else
// (the name constraint) names ErrSSONameTaken. Callers only reach here
// when isUniqueViolation is already true.
func ssoConflict(err error) error {
	if strings.Contains(err.Error(), ssoDomainConstraint) {
		return ErrSSODomainTaken
	}
	return ErrSSONameTaken
}

// foldSSODomain canonicalises the connection's domain on the way into
// the database (see model.NormalizeSSODomain), so the row is stored
// folded whatever the caller passed — the unique index and
// FindSSOConnectionByDomain both depend on there being exactly one
// spelling per domain, and the store must not trust its caller to have
// folded it. A domain that is not shaped like one is
// ErrSSOInvalidConfig, never a silently-mangled value.
func foldSSODomain(conn *model.SSOConnection) error {
	d, err := model.NormalizeSSODomain(conn.Domain)
	if err != nil {
		return ErrSSOInvalidConfig
	}
	conn.Domain = d
	return nil
}

// CreateSSOConnection writes a new connection. The id is allocated here
// when the caller left it empty. The domain is folded and the provider
// config validated on the way in (both → ErrSSOInvalidConfig if bad), so
// a row the database would reject never reaches it. A duplicate name or
// domain is folded into ErrSSONameTaken / ErrSSODomainTaken so the
// handler can answer 409 for the two conflicts an admin can act on and
// 500 for the rest.
func (s *Store) CreateSSOConnection(ctx context.Context, conn *model.SSOConnection) error {
	if conn.ID == "" {
		conn.ID = newID()
	}
	if err := foldSSODomain(conn); err != nil {
		return err
	}
	if err := conn.Validate(); err != nil {
		return ErrSSOInvalidConfig
	}
	// Seal the OIDC client secret at rest (nil stays NULL = "cleared");
	// restore the plaintext on the struct so the caller still holds it.
	origSecret := conn.OIDCClientSecret
	conn.OIDCClientSecret = sealSSOSecret(conn)
	_, err := s.DB.NewInsert().Model(conn).Exec(ctx)
	conn.OIDCClientSecret = origSecret
	if isUniqueViolation(err) {
		return ssoConflict(err)
	}
	return err
}

// FindSSOConnectionByID returns one connection by primary key. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindSSOConnectionByID(ctx context.Context, id string) (*model.SSOConnection, error) {
	conn := new(model.SSOConnection)
	if err := s.DB.NewSelect().Model(conn).Where("id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	if err := openSSOSecret(conn); err != nil {
		return nil, err
	}
	return conn, nil
}

// FindSSOConnectionByDomain looks a connection up by the email domain it
// serves — the lookup a future login route will do on every sign-in. The
// domain is folded first (see model.NormalizeSSODomain), matching how it
// was stored, so the lookup cannot miss because of case or padding. A
// domain that is not shaped like one is an error (not a miss): the caller
// sent something that can never match any row.
func (s *Store) FindSSOConnectionByDomain(ctx context.Context, domain string) (*model.SSOConnection, error) {
	d, err := model.NormalizeSSODomain(domain)
	if err != nil {
		return nil, err
	}
	conn := new(model.SSOConnection)
	if err := s.DB.NewSelect().Model(conn).Where("domain = ?", d).Scan(ctx); err != nil {
		return nil, err
	}
	if err := openSSOSecret(conn); err != nil {
		return nil, err
	}
	return conn, nil
}

// ListSSOConnections is the admin listing: one page of connections plus
// how many there are in total. Ordered by name with id as the tiebreaker
// so equal names cannot swap between two pages of one listing. Every row
// serialises WITHOUT the OIDC client secret (model.OIDCClientSecret is
// json:"-").
func (s *Store) ListSSOConnections(ctx context.Context, p Page) ([]*model.SSOConnection, int, error) {
	var out []*model.SSOConnection
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("name ASC, id ASC")
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	// Best-effort open: the listing never serialises the secret
	// (json:"-"), so a value we can't decrypt is left as-is.
	for _, conn := range out {
		_ = openSSOSecret(conn)
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// UpdateSSOConnection writes back the connection's CONFIG columns — the
// handler merged the patch into the row it read first, applying the
// merge convention the admin API documents: an omitted field keeps its
// stored value, an explicit "" clears a nullable field to NULL, and a
// value sets it (see handler/sso_admin.go's applySSOPatch). The domain is
// folded and the merged provider config re-validated on the way in, so a
// rename cannot put a connection into a state that could not have been
// created.
//
// enabled and created_at are deliberately NOT in the column list: the
// toggle has its own endpoint (SetSSOEnabled) and the creation instant is
// immutable. Excluding enabled here means a config edit cannot clobber a
// concurrent enable/disable. A rename onto a taken name or domain is the
// same ErrSSONameTaken / ErrSSODomainTaken the create path answers, so
// both doors report the conflict as 409.
func (s *Store) UpdateSSOConnection(ctx context.Context, conn *model.SSOConnection) error {
	if err := foldSSODomain(conn); err != nil {
		return err
	}
	if err := conn.Validate(); err != nil {
		return ErrSSOInvalidConfig
	}
	conn.UpdatedAt = time.Now()
	// Seal the OIDC client secret at rest (nil stays NULL = "cleared");
	// restore the plaintext on the struct after the write.
	origSecret := conn.OIDCClientSecret
	conn.OIDCClientSecret = sealSSOSecret(conn)
	_, err := s.DB.NewUpdate().Model(conn).
		Column("name", "provider_type", "domain",
			"saml_entity_id", "saml_sso_url", "saml_certificate",
			"oidc_issuer", "oidc_client_id", "oidc_client_secret", "oidc_scopes",
			"updated_at").
		WherePK().Exec(ctx)
	conn.OIDCClientSecret = origSecret
	if isUniqueViolation(err) {
		return ssoConflict(err)
	}
	return err
}

// DeleteSSOConnection hard-deletes the connection. The admin delete is a
// hard delete (there is no per-row history here — the audit trail lives
// in audit_logs, not in this row). A no-op delete answers sql.ErrNoRows
// so the caller can say 404 for a row that was never there rather than
// claim a deletion that did not happen.
func (s *Store) DeleteSSOConnection(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*model.SSOConnection)(nil)).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetSSOEnabled flips a connection's enabled flag and bumps its update
// stamp. This is the ONLY writer of enabled (the general update path
// excludes the column), so the toggle cannot be undone by a concurrent
// config edit. A missing row is sql.ErrNoRows (404).
func (s *Store) SetSSOEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.DB.NewUpdate().Model((*model.SSOConnection)(nil)).
		Set("enabled = ?", enabled).
		Set("updated_at = now()").
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ─── SCIM tokens ───

// CreateSCIMToken mints a new provisioning token. The plaintext is drawn
// here (model.NewSCIMToken), reduced to its SHA-256 hash and display
// prefix, and the row stores ONLY those two — a caller cannot persist a
// wrong hash or leak the plaintext. tok.ID is filled in when empty.
//
// The plaintext is RETURNED EXACTLY ONCE, as the first return value: the
// handler shows it to the admin in this one response and drops it. There
// is no endpoint, admin or otherwise, that can show it again — every read
// serialises the row, which has no plaintext field (TokenHash is
// json:"-").
func (s *Store) CreateSCIMToken(ctx context.Context, tok *model.SCIMToken) (string, error) {
	if tok.ID == "" {
		tok.ID = newID()
	}
	plaintext, prefix, err := model.NewSCIMToken()
	if err != nil {
		return "", err
	}
	tok.TokenHash = model.HashSCIMToken(plaintext)
	tok.TokenPrefix = prefix
	if _, err := s.DB.NewInsert().Model(tok).Exec(ctx); err != nil {
		return "", err
	}
	return plaintext, nil
}

// ListSCIMTokens is the admin listing: one page of tokens newest first,
// plus the total. The rows carry only the display prefix and timestamps —
// NEVER the hash (model.TokenHash is json:"-") and never the plaintext
// (which is not a field on the model at all).
func (s *Store) ListSCIMTokens(ctx context.Context, p Page) ([]*model.SCIMToken, int, error) {
	var out []*model.SCIMToken
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("created_at DESC, id DESC")
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// FindSCIMTokenByID returns one token whatever its state (revoked
// included) — an admin view must still show a dead token. The hash is on
// the row (json:"-" keeps it out of responses) so the future auth path can
// compare, but nothing serialises it.
func (s *Store) FindSCIMTokenByID(ctx context.Context, id string) (*model.SCIMToken, error) {
	tok := new(model.SCIMToken)
	return tok, s.DB.NewSelect().Model(tok).Where("id = ?", id).Scan(ctx)
}

// FindSCIMTokenByHash authenticates a presented bearer token: the future
// SCIM auth middleware hashes it with model.HashSCIMToken and looks it up
// here. Only a USABLE token comes back — a revoked token does not
// validate, so a stale credential fails exactly like a wrong one
// (sql.ErrNoRows) and cannot be probed for state through this path. This
// is the seam the SCIM 2.0 /Users + /Groups endpoints will sit behind.
func (s *Store) FindSCIMTokenByHash(ctx context.Context, keyHash string) (*model.SCIMToken, error) {
	tok := new(model.SCIMToken)
	return tok, s.DB.NewSelect().Model(tok).
		Where("token_hash = ?", keyHash).
		Where("revoked_at IS NULL").
		Scan(ctx)
}

// TouchSCIMTokenLastUsed stamps last_used_at after a successful
// authentication. Only usable tokens are stamped: a revoked token that
// somehow got looked up must not gain a fresh "last used" mark. Failure
// is the caller's to observe but is never worth failing a request over.
// This is the write the future SCIM auth middleware will call alongside
// FindSCIMTokenByHash.
func (s *Store) TouchSCIMTokenLastUsed(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().Model((*model.SCIMToken)(nil)).
		Set("last_used_at = now()").
		Where("id = ?", id).
		Where("revoked_at IS NULL").
		Exec(ctx)
	return err
}

// RevokeSCIMToken soft-revokes a token (revoked_at = now()), so the row
// keeps its audit trail: who created it, when it was last used, when it
// died. Revoking a token that is already revoked is refused with
// ErrSCIMTokenRevoked — the admin is told it was already dead rather than
// shown a silent no-op that looks like a fresh action. A missing token is
// sql.ErrNoRows (404).
func (s *Store) RevokeSCIMToken(ctx context.Context, id string) error {
	res, err := s.DB.NewUpdate().Model((*model.SCIMToken)(nil)).
		Set("revoked_at = now()").
		Where("id = ?", id).
		Where("revoked_at IS NULL").
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	// No live row updated. Distinguish "already revoked" from "no such
	// token" so the two earn their own answers (409 vs 404).
	if _, err := s.FindSCIMTokenByID(ctx, id); err != nil {
		return err // sql.ErrNoRows when absent
	}
	return ErrSCIMTokenRevoked
}

// DeleteSCIMToken hard-deletes the row (the admin delete is a hard
// delete; soft revoke is RevokeSCIMToken). The audit trail lives in
// audit_logs, not in this row. A no-op delete answers sql.ErrNoRows so
// the caller can say 404 rather than claim a deletion that did not
// happen.
func (s *Store) DeleteSCIMToken(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*model.SCIMToken)(nil)).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
