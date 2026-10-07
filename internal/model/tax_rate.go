package model

import (
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
)

// ─── Tax Rate ───
//
// One row per taxing jurisdiction ("US-CA", "VAT-VN"). The rate is an
// integer number of basis points (10_000 = 100%), the unit the pure
// engine in internal/tax computes in — money on this platform is int64
// minor units, and a fractional rate kept as a float would round
// differently in the table than it does at checkout. The row is
// configuration only; the arithmetic stays in the engine, reached
// through ToEngine.
type TaxRate struct {
	bun.BaseModel `bun:"table:tax_rates"`

	ID string `bun:",pk" json:"id"`
	// Jurisdiction labels the taxing authority, e.g. "US-CA" or
	// "VAT-VN". Unique: one rate per jurisdiction, so a lookup at
	// checkout can never be faced with two answers.
	Jurisdiction string `bun:",notnull,unique" json:"jurisdiction"`
	// BasisPoints is the rate: 10_000 = 100%, 0 = zero-rated. Only
	// whole basis points are representable; see tax.Rate.
	BasisPoints int64 `bun:",notnull" json:"basis_points"`
	// Inclusive says whether this rate is applied inclusive-of-price
	// (tax already contained in the gross) rather than added on top of
	// a net amount. Which side the rate applies to is a checkout
	// decision, so it travels with the call and not with the engine.
	Inclusive   bool   `bun:",notnull" json:"inclusive"`
	Country     string `bun:",notnull,default:''" json:"country"`
	Region      string `bun:",notnull,default:''" json:"region"`
	Description string `bun:",notnull,default:''" json:"description"`
	// Active rates only are offered at checkout; a deactivated row is
	// kept because orders that already used it still read from it.
	Active    bool      `bun:",notnull,default:true" json:"active"`
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ToEngine hands the row to the pure tax engine. The engine wants a
// number of basis points and a jurisdiction label and nothing else;
// Inclusive, Country and Region are checkout concerns and stay here.
func (r *TaxRate) ToEngine() tax.Rate {
	return tax.Rate{BasisPoints: r.BasisPoints, Jurisdiction: r.Jurisdiction}
}
