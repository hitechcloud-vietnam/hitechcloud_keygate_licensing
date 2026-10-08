package store

import (
	"context"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Customer Webhooks (portal self-service endpoints) ───
//
// Naming: the plain CreateWebhook / FindWebhookByID / ListWebhooks /
// UpdateWebhook / DeleteWebhook names are already taken by the
// operator/product webhooks in webhook.go, so every method here carries
// the Customer qualifier. Same for the table (customer_webhooks) and the
// model (model.CustomerWebhook).
//
// Unlike a customer API key (a bearer credential, stored as a SHA-256
// hash), the webhook signing secret is stored in plaintext: the delivery
// signer needs the plaintext back to compute each HMAC. It is never
// returned by any read the API serialises (model.Secret has json:"-")
// and never logged. See the model doc for the storage rationale.

// CreateCustomerWebhook writes a new row. The raw secret is reduced to
// its display prefix and stored alongside the secret here, so a caller
// cannot persist a wrong-length prefix. w.ID is filled in when empty.
// On return w.Secret and w.SecretPrefix are set; the raw secret must be
// shown to the customer once and dropped.
func (s *Store) CreateCustomerWebhook(ctx context.Context, w *model.CustomerWebhook, rawSecret string) error {
	if w.ID == "" {
		w.ID = newID()
	}
	w.Secret = rawSecret
	w.SecretPrefix = model.CustomerWebhookDisplayPrefix(rawSecret)
	_, err := s.DB.NewInsert().Model(w).Exec(ctx)
	return err
}

// ListCustomerWebhooksByUser returns one page of a user's endpoints,
// newest first. Scoped to the owner: there is no "all users" variant on
// purpose — the portal only ever asks "mine".
func (s *Store) ListCustomerWebhooksByUser(ctx context.Context, userID string, p Page) ([]*model.CustomerWebhook, int, error) {
	var out []*model.CustomerWebhook
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

// FindCustomerWebhookByID returns the row whatever its state (active or
// not). Ownership is the caller's check: the portal handler compares
// UserID against the session user and answers 404 otherwise. The row
// carries the plaintext Secret so the dispatch path can sign with it.
func (s *Store) FindCustomerWebhookByID(ctx context.Context, id string) (*model.CustomerWebhook, error) {
	w := new(model.CustomerWebhook)
	return w, s.DB.NewSelect().Model(w).Where("id = ?", id).Scan(ctx)
}

// UpdateCustomerWebhook writes only the customer-mutable columns (url,
// events, active) plus the update stamp. The secret and its prefix are
// deliberately NOT in the column list: they are set at creation and
// never change, so a partial PATCH cannot accidentally overwrite them.
func (s *Store) UpdateCustomerWebhook(ctx context.Context, w *model.CustomerWebhook) error {
	w.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(w).
		Column("url", "events", "active", "updated_at").
		WherePK().Exec(ctx)
	return err
}

// DeleteCustomerWebhook hard-deletes the row (the portal delete is a
// hard delete — see the API contract). The audit trail lives in the
// audit_logs table, not in this row.
func (s *Store) DeleteCustomerWebhook(ctx context.Context, id string) error {
	_, err := s.DB.NewDelete().Model((*model.CustomerWebhook)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	return err
}

// CountCustomerWebhooksByUser counts a user's endpoints so the create
// path can enforce the per-user cap before minting a secret.
func (s *Store) CountCustomerWebhooksByUser(ctx context.Context, userID string) (int, error) {
	return s.DB.NewSelect().Model((*model.CustomerWebhook)(nil)).
		Where("user_id = ?", userID).Count(ctx)
}

// FindCustomerWebhooksForEvent returns every ACTIVE endpoint subscribed
// to the event, across all users — the fan-out set for the async
// dispatch helper. Inactive endpoints never receive real deliveries
// (a test fire is a separate, synchronous path).
func (s *Store) FindCustomerWebhooksForEvent(ctx context.Context, event string) ([]*model.CustomerWebhook, error) {
	var out []*model.CustomerWebhook
	err := s.DB.NewSelect().Model(&out).
		Where("active = true AND ? = ANY(events)", event).
		Scan(ctx)
	return out, err
}

// TouchCustomerWebhookLastDelivery stamps last_delivery_at after a
// delivery attempt (test or real). Best-effort: the caller has already
// done the important work, and a stamp failure must not fail the
// request or the dispatch. Never touches the secret.
func (s *Store) TouchCustomerWebhookLastDelivery(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().Model((*model.CustomerWebhook)(nil)).
		Set("last_delivery_at = now()").
		Where("id = ?", id).
		Exec(ctx)
	return err
}
