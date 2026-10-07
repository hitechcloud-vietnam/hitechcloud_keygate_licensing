package store

import (
	"context"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Customer API Keys (portal self-service) ───
//
// Naming: the plain CreateAPIKey / FindAPIKeyByID / DeleteAPIKey /
// TouchAPIKey names are already taken by the operator/product
// server-to-server keys in admin.go + store.go, so every method here
// carries the Customer qualifier. Same for the table (customer_api_
// keys) and the model (model.CustomerAPIKey) — see the migration for
// the full story.
//
// Hashing at rest is store.HashAPIKey — SHA-256 hex of the raw secret,
// the exact recipe the product API keys and the license key hashes
// use. Plaintext secrets never reach these methods' SQL.

// CreateCustomerAPIKey writes a new row. The raw secret is hashed and
// reduced to its display prefix here, so a caller cannot accidentally
// persist either the plaintext or a wrong-length prefix. ak.ID is
// filled in when empty. On return ak.KeyHash and ak.KeyPrefix are set;
// the raw secret must be shown to the customer once and dropped.
func (s *Store) CreateCustomerAPIKey(ctx context.Context, ak *model.CustomerAPIKey, rawSecret string) error {
	if ak.ID == "" {
		ak.ID = newID()
	}
	ak.KeyHash = HashAPIKey(rawSecret)
	ak.KeyPrefix = model.CustomerAPIKeyDisplayPrefix(rawSecret)
	_, err := s.DB.NewInsert().Model(ak).Exec(ctx)
	return err
}

// ListCustomerAPIKeysByUser returns one page of a user's keys, newest
// first. Scoped to the owner: there is no "all users" variant on
// purpose — the portal only ever asks "mine".
func (s *Store) ListCustomerAPIKeysByUser(ctx context.Context, userID string, p Page) ([]*model.CustomerAPIKey, int, error) {
	var out []*model.CustomerAPIKey
	q := s.DB.NewSelect().Model(&out).
		Where("user_id = ?", userID).
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

// FindCustomerAPIKeyByID returns the row whatever its state (revoked
// or expired keys included) — the portal's Get/Revoke views must still
// show a dead key. Ownership is the caller's check: the portal handler
// compares UserID against the session user and answers 404 otherwise.
func (s *Store) FindCustomerAPIKeyByID(ctx context.Context, id string) (*model.CustomerAPIKey, error) {
	k := new(model.CustomerAPIKey)
	return k, s.DB.NewSelect().Model(k).Where("id = ?", id).Scan(ctx)
}

// FindCustomerAPIKeyByHash authenticates a presented secret: the
// caller hashes it with store.HashAPIKey and looks it up here. Only a
// USABLE key comes back — revoked and expired keys do not validate,
// so a stale credential fails exactly like a wrong one (sql.ErrNoRows)
// and cannot be probed for state through this path.
func (s *Store) FindCustomerAPIKeyByHash(ctx context.Context, keyHash string) (*model.CustomerAPIKey, error) {
	k := new(model.CustomerAPIKey)
	return k, s.DB.NewSelect().Model(k).
		Where("key_hash = ?", keyHash).
		Where("revoked_at IS NULL").
		Where("expires_at IS NULL OR expires_at > now()").
		Scan(ctx)
}

// TouchCustomerAPIKeyLastUsed stamps last_used_at after a successful
// authentication. Only usable keys are stamped: a revoked or expired
// key that somehow got looked up must not gain a fresh "last used"
// mark. Failure is the caller's to observe but is never worth failing
// a request over.
func (s *Store) TouchCustomerAPIKeyLastUsed(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().Model((*model.CustomerAPIKey)(nil)).
		Set("last_used_at = now()").
		Where("id = ?", id).
		Where("revoked_at IS NULL").
		Where("expires_at IS NULL OR expires_at > now()").
		Exec(ctx)
	return err
}

// RevokeCustomerAPIKey soft-revokes the key: revoked_at is stamped and
// the row stays for the audit trail (who made it, when it was last
// used, when it died). Idempotent — revoking an already-revoked key is
// not an error; the second call simply changes nothing.
func (s *Store) RevokeCustomerAPIKey(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().Model((*model.CustomerAPIKey)(nil)).
		Set("revoked_at = now()").
		Set("updated_at = now()").
		Where("id = ?", id).
		Where("revoked_at IS NULL").
		Exec(ctx)
	return err
}

// DeleteCustomerAPIKey hard-deletes the row. Not the portal path (the
// portal revokes, so the trail survives) — it exists for account
// erasure and tests.
func (s *Store) DeleteCustomerAPIKey(ctx context.Context, id string) error {
	_, err := s.DB.NewDelete().Model((*model.CustomerAPIKey)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	return err
}
