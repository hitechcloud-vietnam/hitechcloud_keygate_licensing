package model

import (
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
)

// ─── Coupon ───

// Coupon types — the persisted vocabulary of [Coupon.Type]. The admin
// API speaks "percent_off" / "fixed_off"; [Coupon.ToEngine] maps them
// onto the engine's own constants, which spell the fixed one
// differently. Anything else is refused at the boundary (see
// handler.normalizeCoupon), so a row is never written with a type the
// engine would reject at redemption.
const (
	CouponTypePercentOff = "percent_off"
	CouponTypeFixedOff   = "fixed_off"
)

// Coupon is one discount code, managed from the admin panel and
// redeemed at checkout. It is the persisted half of the pure engine in
// internal/coupon: this struct carries row identity and the redemption
// bookkeeping, and ToEngine turns it into the value the engine
// computes with. Nothing here discounts anything on its own.
//
// Money is int64 MINOR units (or basis points) — never float. The
// numeric columns carry no bun `default:N` annotations on purpose: bun
// turns a zero Go value on a `default:` field into the SQL DEFAULT on
// insert (see the note on Plan), and 0 is meaningful here — it is how
// "unlimited" and "no minimum" are spelled. The CREATE-TABLE defaults
// exist for plain SQL only; the writer is the source of truth.
type Coupon struct {
	bun.BaseModel `bun:"table:coupons"`

	ID string `bun:",pk" json:"id"`
	// Code is the human-facing code, stored normalized (upper-cased,
	// trimmed) so lookup and redemption match it case-insensitively.
	// Unique: it is the coupon's handle everywhere.
	Code string `bun:",notnull,unique" json:"code"`
	Type string `bun:",notnull" json:"type"`

	// ValueBPS is the discount size for percent_off, in basis points
	// where coupon.PercentBase (10000) is 100%. Meaningless for
	// fixed_off, stored as 0.
	ValueBPS int64 `bun:",notnull" json:"value_bps"`
	// ValueMinor is the discount size for fixed_off, in minor units of
	// Currency. Meaningless for percent_off, stored as 0.
	ValueMinor int64 `bun:",notnull" json:"value_minor"`
	// Currency is the currency of ValueMinor. Required for fixed_off;
	// for percent_off an empty value means "any order currency", a
	// non-empty one restricts the coupon to orders in that currency.
	Currency string `bun:",notnull,default:''" json:"currency,omitempty"`

	// StartsAt / EndsAt bound the validity window (the engine reads
	// both bounds as inclusive). Nil means no bound on that side.
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	// MaxRedemptions is the total redemption cap; 0 means unlimited.
	// TimesRedeemed is the count so far and is moved only by
	// store.IncrementCouponRedemptions — never edited by hand.
	MaxRedemptions int `bun:",notnull" json:"max_redemptions"`
	TimesRedeemed  int `bun:",notnull" json:"times_redeemed"`
	// MaxRedemptionsPerCustomer is the per-customer redemption cap;
	// 0 means unlimited.
	MaxRedemptionsPerCustomer int `bun:",notnull" json:"max_redemptions_per_customer"`
	// MinimumOrderMinor is the smallest order subtotal the coupon may
	// be applied to, in minor units; 0 means no minimum.
	MinimumOrderMinor int64 `bun:",notnull" json:"minimum_order_minor"`
	// AppliesTo restricts the coupon to order lines carrying one of
	// these product/plan codes, comma-separated. Empty means every
	// line is eligible.
	AppliesTo string `bun:",notnull,default:''" json:"applies_to,omitempty"`

	// Stackable reports whether the coupon may be combined with other
	// coupons. Active is the master switch: an inactive coupon never
	// validates. Both default to true at the API, but carry no bun
	// `default:true` — a deliberate false at creation has to survive
	// the insert, and a zero Go value on a `default:` field becomes
	// the SQL DEFAULT instead of the value asked for.
	Stackable bool `bun:",notnull" json:"stackable"`
	Active    bool `bun:",notnull" json:"active"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ToEngine converts the row into the pure discount definition the
// internal/coupon engine computes with. The persisted "fixed_off"
// spelling maps to the engine's TypeFixedAmountOff; a type neither
// constant names maps to the zero Type and Value, which the engine
// refuses to validate. Nil bounds become zero times, which the engine
// reads as "no bound" — same meaning as the NULL column.
func (c *Coupon) ToEngine() coupon.Coupon {
	out := coupon.Coupon{
		Code:                      c.Code,
		Currency:                  c.Currency,
		MaxRedemptions:            c.MaxRedemptions,
		TimesRedeemed:             c.TimesRedeemed,
		MaxRedemptionsPerCustomer: c.MaxRedemptionsPerCustomer,
		MinimumOrderAmount:        c.MinimumOrderMinor,
		Stackable:                 c.Stackable,
		Active:                    c.Active,
	}
	switch c.Type {
	case CouponTypePercentOff:
		out.Type = coupon.TypePercentOff
		out.Value = c.ValueBPS
	case CouponTypeFixedOff:
		out.Type = coupon.TypeFixedAmountOff
		out.Value = c.ValueMinor
	}
	if c.StartsAt != nil {
		out.StartsAt = *c.StartsAt
	}
	if c.EndsAt != nil {
		out.EndsAt = *c.EndsAt
	}
	// The engine matches these case-insensitively, but normalizing as
	// they are split drops the empties a trailing comma leaves behind.
	for _, code := range strings.Split(c.AppliesTo, ",") {
		if code = coupon.NormalizeCode(code); code != "" {
			out.AppliesTo = append(out.AppliesTo, code)
		}
	}
	return out
}
