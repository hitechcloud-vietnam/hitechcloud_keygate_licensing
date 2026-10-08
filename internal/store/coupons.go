package store

import (
	"context"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Coupons ───

func (s *Store) CreateCoupon(ctx context.Context, cpn *model.Coupon) error {
	if cpn.ID == "" {
		cpn.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(cpn).Exec(ctx)
	return err
}

func (s *Store) FindCouponByID(ctx context.Context, id string) (*model.Coupon, error) {
	c := new(model.Coupon)
	return c, s.DB.NewSelect().Model(c).Where("id = ?", id).Scan(ctx)
}

// FindCouponByCode looks a coupon up by its human code. Codes are
// stored normalized and matched normalized, so the lookup is
// case-insensitive either way; comparing lower(code) on both sides
// also finds a row written before normalization.
func (s *Store) FindCouponByCode(ctx context.Context, code string) (*model.Coupon, error) {
	c := new(model.Coupon)
	return c, s.DB.NewSelect().Model(c).
		Where("lower(code) = ?", strings.ToLower(coupon.NormalizeCode(code))).
		Scan(ctx)
}

// couponsListQuery builds the coupon catalogue listing. The sort is
// the handler's validated ordering; an empty one takes the listing's
// default, newest first.
func couponsListQuery(db bun.IDB, search string, sort Sort, dest *[]*model.Coupon) *bun.SelectQuery {
	if sort.Expr == "" {
		sort = Sort{Expr: "coupon.created_at", Desc: true}
	}
	q := db.NewSelect().Model(dest)
	if search != "" {
		q = q.Where("code ILIKE ?", "%"+search+"%")
	}
	return applySort(q, sort, "coupon.id")
}

func (s *Store) ListCoupons(ctx context.Context, search string, p Page, sort Sort) ([]*model.Coupon, int, error) {
	var out []*model.Coupon
	q := couponsListQuery(s.DB, search, sort, &out)
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

func (s *Store) UpdateCoupon(ctx context.Context, cpn *model.Coupon) error {
	cpn.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(cpn).WherePK().Exec(ctx)
	return err
}

func (s *Store) DeleteCoupon(ctx context.Context, id string) error {
	_, err := s.DB.NewDelete().Model((*model.Coupon)(nil)).Where("id = ?", id).Exec(ctx)
	return err
}

// IncrementCouponRedemptions moves the redemption counter after a
// coupon has been applied to an order. A raw increment rather than a
// read-modify-write: two checkouts landing at once must both count,
// and the engine has already refused the one that would exceed the
// cap.
func (s *Store) IncrementCouponRedemptions(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().
		Model((*model.Coupon)(nil)).
		Set("times_redeemed = times_redeemed + 1, updated_at = now()").
		Where("id = ?", id).
		Exec(ctx)
	return err
}
