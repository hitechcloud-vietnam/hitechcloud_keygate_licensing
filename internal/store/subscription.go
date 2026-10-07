package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// SyncLicenseSubscriptionIn keeps a licence's subscription row in step
// with a plan change, on the caller's transaction.
//
// Issuance creates that row for trial and subscription plans and
// stamps the licence's status on it. Nothing put it right afterwards:
// the licence write touches only the licences table, and the hourly
// SyncSubscriptionStatuses repairs expired, canceled and revoked
// alone. A licence that left a trial plan therefore read as "active"
// in one table and "trialing" in the other for good — and every
// subscription query saw the trial.
//
// The row is created when the new plan is one that would have had it,
// and kept (not deleted) when the plan no longer is: it may carry the
// provider's identifiers, which are not ours to throw away.
//
// validUntil is the licence's own deadline, and the trial window is
// derived from it rather than computed again from the plan's days.
// The licence is what the expiry job reads, so a second reckoning
// here could only disagree with it: moving between two trial plans
// (or picking the plan the licence is already on) leaves
// licenses.valid_until alone, and a freshly computed trial_end would
// have the subscription view showing a trial that runs later than the
// date the licence actually expires on.
func SyncLicenseSubscriptionIn(ctx context.Context, db bun.IDB, licenseID string, plan *model.Plan, status string, validUntil *time.Time) error {
	if plan == nil {
		return nil
	}
	// Newest first, as FindSubscriptionByLicense reads it: nothing
	// stops a licence having more than one row (license_id carries an
	// index, not a unique constraint), and an unordered LIMIT 1 would
	// happily update a historical one while the portal went on
	// reading the current subscription's stale plan and status.
	sub := new(model.Subscription)
	err := db.NewSelect().Model(sub).Where("license_id = ?", licenseID).
		OrderExpr("created_at DESC").Limit(1).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if plan.LicenseType != "subscription" && plan.LicenseType != "trial" {
			return nil
		}
		sub = &model.Subscription{ID: newID(), LicenseID: licenseID, PlanID: plan.ID, Status: status}
		if plan.LicenseType == "trial" && validUntil != nil {
			now := time.Now()
			sub.TrialStart, sub.TrialEnd = &now, validUntil
		}
		_, err = db.NewInsert().Model(sub).Exec(ctx)
		return err
	case err != nil:
		return err
	}
	sub.PlanID, sub.Status = plan.ID, status
	cols := []string{"plan_id", "status", "updated_at"}
	switch {
	case plan.LicenseType == "trial" && validUntil != nil:
		// The end is the licence's; the start is when this trial began
		// — which is not now if the licence was already trialling.
		if !model.SameEnd(sub.TrialEnd, validUntil) {
			if sub.TrialStart == nil {
				now := time.Now()
				sub.TrialStart = &now
				cols = append(cols, "trial_start")
			}
			sub.TrialEnd = validUntil
			cols = append(cols, "trial_end")
		}
	case sub.TrialStart != nil || sub.TrialEnd != nil:
		// The trial belonged to the plan the licence has left.
		sub.TrialStart, sub.TrialEnd = nil, nil
		cols = append(cols, "trial_start", "trial_end")
	}
	sub.UpdatedAt = time.Now()
	_, err = db.NewUpdate().Model(sub).Column(cols...).WherePK().Exec(ctx)
	return err
}

func (s *Store) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	if sub.ID == "" {
		sub.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(sub).Exec(ctx)
	return err
}

func (s *Store) FindSubscriptionByID(ctx context.Context, id string) (*model.Subscription, error) {
	sub := new(model.Subscription)
	return sub, s.DB.NewSelect().Model(sub).
		Relation("License").Relation("Plan").
		Where("subscription.id = ?", id).Scan(ctx)
}

func (s *Store) FindSubscriptionByLicense(ctx context.Context, licenseID string) (*model.Subscription, error) {
	sub := new(model.Subscription)
	return sub, s.DB.NewSelect().Model(sub).
		Relation("Plan").
		Where("subscription.license_id = ?", licenseID).
		OrderExpr("subscription.created_at DESC").Limit(1).Scan(ctx)
}

func (s *Store) FindSubscriptionByExternal(ctx context.Context, provider, externalID string) (*model.Subscription, error) {
	sub := new(model.Subscription)
	return sub, s.DB.NewSelect().Model(sub).
		Relation("License").Relation("Plan").
		Where("subscription.payment_provider = ? AND subscription.external_id = ?", provider, externalID).Scan(ctx)
}

func (s *Store) ListSubscriptionsByUser(ctx context.Context, userID string) ([]*model.Subscription, error) {
	var out []*model.Subscription
	err := s.DB.NewSelect().Model(&out).
		Relation("License").Relation("Plan").Relation("Plan.Product").
		Where("subscription.user_id = ?", userID).
		OrderExpr("subscription.created_at DESC").Scan(ctx)
	return out, err
}

func (s *Store) UpdateSubscription(ctx context.Context, sub *model.Subscription, cols ...string) error {
	sub.UpdatedAt = time.Now()
	cols = append(cols, "updated_at")
	_, err := s.DB.NewUpdate().Model(sub).Column(cols...).WherePK().Exec(ctx)
	return err
}

func (s *Store) ListSubscriptions(ctx context.Context, productID, status string, offset, limit int) ([]*model.Subscription, int, error) {
	q := s.DB.NewSelect().Model((*model.Subscription)(nil)).
		Relation("License").Relation("Plan").Relation("Plan.Product").
		OrderExpr("subscription.created_at DESC")
	if productID != "" {
		q = q.Where("subscription.plan_id IN (SELECT id FROM plans WHERE product_id = ?)", productID)
	}
	if status != "" {
		q = q.Where("subscription.status = ?", status)
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	var out []*model.Subscription
	err = q.Offset(offset).Limit(limit).Scan(ctx, &out)
	return out, total, err
}
