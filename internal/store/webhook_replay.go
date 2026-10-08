package store

import (
	"context"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// CreateWebhookReplayDelivery inserts a fresh delivery row that
// replays an earlier one. It is CreateWebhookDelivery plus the
// model.WebhookReplayDelivery.ReplayOf mark; the id is minted here so
// the caller can put it straight into the X-HiTechCloud-Delivery
// header it is about to send.
//
// The mark is written with the row, in one INSERT — a replay is never
// recorded first and marked after.
func (s *Store) CreateWebhookReplayDelivery(ctx context.Context, d *model.WebhookReplayDelivery) error {
	if d.ID == "" {
		d.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(d).Exec(ctx)
	return err
}
