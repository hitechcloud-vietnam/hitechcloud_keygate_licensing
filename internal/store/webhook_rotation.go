package store

import (
	"context"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/crypto"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// RotateCustomerWebhookSecret replaces a customer webhook's signing
// secret and its display prefix in ONE write. The raw secret is sealed
// at rest with the same crypto.Seal path CreateCustomerWebhook uses
// (so a rotated secret is protected exactly like a created one), and
// the plaintext exists only in the response the rotate endpoint shows
// once.
//
// Deliberately NOT UpdateCustomerWebhook: that method's column list
// excludes the secret and its prefix on purpose so a partial PATCH can
// never overwrite them. This is the one write that may touch them, and
// it touches nothing else.
//
// The old secret is invalid the moment this commits: every read path
// (FindCustomerWebhookByID, FindCustomerWebhooksForEvent) opens what
// is stored, so the very next delivery is signed with the new secret.
// That is immediate invalidation — the single-secret HMAC scheme has
// no dual-verify window to exploit here.
func (s *Store) RotateCustomerWebhookSecret(ctx context.Context, id, rawSecret string) error {
	_, err := s.DB.NewUpdate().Model((*model.CustomerWebhook)(nil)).
		Set("secret = ?", crypto.Seal(rawSecret)).
		Set("secret_prefix = ?", model.CustomerWebhookDisplayPrefix(rawSecret)).
		Set("updated_at = now()").
		Where("id = ?", id).
		Exec(ctx)
	return err
}
