// Command seed populates a development database with demo data
// (plan §65): an owner user, a product with the six example plans,
// entitlements, multi-currency plan prices, coupons, and a demo customer
// with a license.
//
// It is IDEMPOTENT: every entity is looked up before it is written
// (products by slug, plans by (product, slug), coupons by code, users and
// licenses by email), so re-running the command is always safe. Plan prices
// are upserts keyed by (plan_id, currency) — re-running re-prices to the
// same values.
//
// Money discipline (plan §51): every amount below is int64 MINOR UNITS at
// the currency's ISO-4217 exponent. VND has exponent 0 (whole dong:
// 499000 = 499,000 VND), USD has exponent 2 (1999 = 19.99 USD).
//
// Seed data never contains real credentials (plan §65).
//
// Usage:
//
//	DATABASE_URL=postgres://… go run ./scripts/seed
//	go run ./scripts/seed -dsn postgres://… -admin-email admin@example.com
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/license"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── Seed shape ───

type priceSeed struct {
	currency    string
	amountMinor int64 // int64 minor units: VND exponent 0, USD exponent 2
	isDefault   bool
}

type entSeed struct {
	feature     string
	valueType   string // "bool" | "quota"
	value       string
	quotaPeriod string // "" | hourly | daily | monthly | yearly
	quotaUnit   string
}

type planSeed struct {
	name            string
	slug            string
	licenseType     string // perpetual | subscription | trial
	billingInterval string // "" | month | year  ("" for perpetual)
	maxActivations  int
	trialDays       int
	graceDays       int
	maxSeats        int
	updatesDays     int // perpetual maintenance window; 0 = updates for life
	sortOrder       int
	prices          []priceSeed
	entitlements    []entSeed
}

// demoProduct is the plan §65 example product with the six example plans.
var demoProduct = struct {
	name string
	slug string
	typ  string // desktop | saas | hybrid
}{
	name: "HiTechCloud Panel",
	slug: "hitechcloud-panel",
	typ:  model.ProductTypeDesktop,
}

var demoPlans = []planSeed{
	{
		name: "Free", slug: "free",
		licenseType: "trial", billingInterval: "", maxActivations: 1,
		trialDays: 14, graceDays: 7, maxSeats: 1, sortOrder: 0,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 0, isDefault: true},
			{currency: "USD", amountMinor: 0},
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "1000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
		},
	},
	{
		name: "Starter", slug: "starter",
		licenseType: "subscription", billingInterval: "month", maxActivations: 2,
		trialDays: 0, graceDays: 7, maxSeats: 3, sortOrder: 1,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 99000, isDefault: true}, // 99,000 VND
			{currency: "USD", amountMinor: 400},                    // 4.00 USD
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "10000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
		},
	},
	{
		name: "Professional", slug: "professional",
		licenseType: "subscription", billingInterval: "month", maxActivations: 5,
		trialDays: 0, graceDays: 7, maxSeats: 10, sortOrder: 2,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 199000, isDefault: true}, // 199,000 VND
			{currency: "USD", amountMinor: 800},                     // 8.00 USD
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "100000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
			{feature: "sso", valueType: "bool", value: "true"},
		},
	},
	{
		name: "Business", slug: "business",
		licenseType: "subscription", billingInterval: "month", maxActivations: 20,
		trialDays: 0, graceDays: 14, maxSeats: 50, sortOrder: 3,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 499000, isDefault: true}, // 499,000 VND
			{currency: "USD", amountMinor: 1999},                    // 19.99 USD
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "500000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
			{feature: "sso", valueType: "bool", value: "true"},
		},
	},
	{
		name: "Enterprise", slug: "enterprise",
		licenseType: "subscription", billingInterval: "year", maxActivations: 100,
		trialDays: 0, graceDays: 30, maxSeats: 500, sortOrder: 4,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 4990000, isDefault: true}, // 4,990,000 VND/yr
			{currency: "USD", amountMinor: 19999},                    // 199.99 USD/yr
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "2000000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
			{feature: "sso", valueType: "bool", value: "true"},
		},
	},
	{
		name: "Lifetime", slug: "lifetime",
		licenseType: "perpetual", billingInterval: "", maxActivations: 10,
		trialDays: 0, graceDays: 0, maxSeats: 10, updatesDays: 0, // 0 = updates for life
		sortOrder: 5,
		prices: []priceSeed{
			{currency: "VND", amountMinor: 9990000, isDefault: true}, // 9,990,000 VND
			{currency: "USD", amountMinor: 39999},                    // 399.99 USD
		},
		entitlements: []entSeed{
			{feature: "api_access", valueType: "bool", value: "true"},
			{feature: "api_calls", valueType: "quota", value: "1000000",
				quotaPeriod: "monthly", quotaUnit: "requests"},
			{feature: "sso", valueType: "bool", value: "true"},
		},
	},
}

var demoCoupons = []*model.Coupon{
	{
		Code:                      "WELCOME10",
		Type:                      model.CouponTypePercentOff,
		ValueBPS:                  1000, // 10% off
		MaxRedemptions:            0,    // unlimited
		MaxRedemptionsPerCustomer: 1,
		Stackable:                 true,
		Active:                    true,
	},
	{
		Code:                      "SAVE50K",
		Type:                      model.CouponTypeFixedOff,
		ValueMinor:                50000, // 50,000 VND off
		Currency:                  "VND",
		MinimumOrderMinor:         500000, // orders from 500,000 VND
		MaxRedemptions:            100,
		MaxRedemptionsPerCustomer: 1,
		Stackable:                 false,
		Active:                    true,
	},
}

// ─── Seeder ───

type seeder struct {
	s       *store.Store
	created int
	skipped int
}

func (sd *seeder) report(action, kind, name string) {
	switch action {
	case "created", "upserted":
		sd.created++
	default:
		sd.skipped++
	}
	log.Printf("%-8s %-14s %s", action, kind, name)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// ensureUser returns the user id, creating the user when absent.
func (sd *seeder) ensureUser(ctx context.Context, email, name, role string) (string, error) {
	existing, err := sd.s.FindUserByEmail(ctx, email)
	switch {
	case err == nil:
		sd.report("skipped", "user", email)
		return existing.ID, nil
	case !isNoRows(err):
		return "", fmt.Errorf("find user %s: %w", email, err)
	}
	u := &model.User{Email: email, Name: name, Role: role}
	if err := sd.s.UpsertUser(ctx, u); err != nil {
		return "", fmt.Errorf("create user %s: %w", email, err)
	}
	sd.report("created", "user", email)
	return u.ID, nil
}

// ensureProduct returns the product, creating it when absent (by slug).
func (sd *seeder) ensureProduct(ctx context.Context) (*model.Product, error) {
	existing, err := sd.s.FindProductBySlug(ctx, demoProduct.slug)
	switch {
	case err == nil:
		sd.report("skipped", "product", demoProduct.slug)
		return existing, nil
	case !isNoRows(err):
		return nil, fmt.Errorf("find product %s: %w", demoProduct.slug, err)
	}
	p := &model.Product{
		Name:           demoProduct.name,
		Slug:           demoProduct.slug,
		Type:           demoProduct.typ,
		RequireSigning: true,
	}
	if err := sd.s.CreateProduct(ctx, p); err != nil {
		return nil, fmt.Errorf("create product %s: %w", demoProduct.slug, err)
	}
	sd.report("created", "product", demoProduct.slug)
	return p, nil
}

// ensurePlan returns the plan id, creating the plan (and its entitlements
// and prices) when absent. Entitlements are skipped per feature when the
// plan already carries them.
func (sd *seeder) ensurePlan(ctx context.Context, productID string, seed planSeed) (string, error) {
	var plan *model.Plan
	plans, _, err := sd.s.ListPlans(ctx, productID, "", store.All)
	if err != nil {
		return "", fmt.Errorf("list plans for %s: %w", productID, err)
	}
	for _, p := range plans {
		if p.Slug == seed.slug {
			plan = p
			break
		}
	}
	if plan == nil {
		plan = &model.Plan{
			ProductID:       productID,
			Name:            seed.name,
			Slug:            seed.slug,
			LicenseType:     seed.licenseType,
			BillingInterval: seed.billingInterval,
			MaxActivations:  seed.maxActivations,
			TrialDays:       seed.trialDays,
			GraceDays:       seed.graceDays,
			LicenseModel:    "standard",
			FloatingTimeout: 30,
			UpdatesDays:     seed.updatesDays,
			MaxSeats:        seed.maxSeats,
			Active:          true,
			SortOrder:       seed.sortOrder,
		}
		if err := sd.s.CreatePlan(ctx, plan); err != nil {
			return "", fmt.Errorf("create plan %s: %w", seed.slug, err)
		}
		sd.report("created", "plan", seed.slug)
	} else {
		sd.report("skipped", "plan", seed.slug)
	}

	// Entitlements: skip any feature the plan already has.
	have := map[string]bool{}
	for _, e := range plan.Entitlements {
		have[e.Feature] = true
	}
	for _, es := range seed.entitlements {
		if have[es.feature] {
			sd.report("skipped", "entitlement", seed.slug+"/"+es.feature)
			continue
		}
		e := &model.Entitlement{
			PlanID:      plan.ID,
			Feature:     es.feature,
			ValueType:   es.valueType,
			Value:       es.value,
			QuotaPeriod: es.quotaPeriod,
			QuotaUnit:   es.quotaUnit,
		}
		if err := sd.s.CreateEntitlement(ctx, e); err != nil {
			return "", fmt.Errorf("create entitlement %s/%s: %w", seed.slug, es.feature, err)
		}
		sd.report("created", "entitlement", seed.slug+"/"+es.feature)
	}

	// Prices: SetPlanPrice is an upsert keyed by (plan_id, currency).
	for _, ps := range seed.prices {
		pp := &store.PlanPrice{
			PlanID:      plan.ID,
			Currency:    ps.currency,
			AmountMinor: ps.amountMinor,
			IsDefault:   ps.isDefault,
		}
		if err := sd.s.SetPlanPrice(ctx, pp); err != nil {
			return "", fmt.Errorf("set price %s/%s: %w", seed.slug, ps.currency, err)
		}
		sd.report("upserted", "price", fmt.Sprintf("%s/%s = %d", seed.slug, ps.currency, ps.amountMinor))
	}
	return plan.ID, nil
}

// ensureCoupons creates each demo coupon unless its code exists.
func (sd *seeder) ensureCoupons(ctx context.Context) error {
	for _, c := range demoCoupons {
		if existing, err := sd.s.FindCouponByCode(ctx, c.Code); err == nil {
			sd.report("skipped", "coupon", existing.Code)
			continue
		} else if !isNoRows(err) {
			return fmt.Errorf("find coupon %s: %w", c.Code, err)
		}
		row := *c // copy: TimesRedeemed etc. must not accumulate across runs
		if err := sd.s.CreateCoupon(ctx, &row); err != nil {
			return fmt.Errorf("create coupon %s: %w", c.Code, err)
		}
		sd.report("created", "coupon", row.Code)
	}
	return nil
}

// ensureDemoLicense issues the demo customer a license on the Professional
// plan when they hold none for this product yet.
func (sd *seeder) ensureDemoLicense(ctx context.Context, productID, planID, email string) error {
	licenses, err := sd.s.ListLicensesByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("list licenses for %s: %w", email, err)
	}
	for _, l := range licenses {
		if l.ProductID == productID {
			sd.report("skipped", "license", email)
			return nil
		}
	}
	validUntil := time.Now().AddDate(1, 0, 0)
	l := &model.License{
		ProductID:  productID,
		PlanID:     planID,
		Email:      email,
		LicenseKey: license.GenerateKey(""),
		Status:     model.StatusActive,
		ValidFrom:  time.Now(),
		ValidUntil: &validUntil,
		Notes:      "demo license seeded by scripts/seed",
	}
	if err := sd.s.CreateLicense(ctx, l); err != nil {
		return fmt.Errorf("create license for %s: %w", email, err)
	}
	sd.report("created", "license", email)
	return nil
}

func main() {
	var (
		dsn           = flag.String("dsn", os.Getenv("DATABASE_URL"), "database DSN (defaults to $DATABASE_URL)")
		adminEmail    = flag.String("admin-email", "admin@example.com", "owner user email")
		adminName     = flag.String("admin-name", "Demo Admin", "owner user name")
		customerEmail = flag.String("customer-email", "demo@example.com", "demo customer email")
		customerName  = flag.String("customer-name", "Demo Customer", "demo customer name")
	)
	flag.Parse()

	if *dsn == "" {
		log.Fatal("seed: no database DSN — set DATABASE_URL or pass -dsn")
	}

	ctx := context.Background()
	st, err := store.New(*dsn)
	if err != nil {
		log.Fatalf("seed: connect: %v", err)
	}
	defer st.Close()

	sd := &seeder{s: st}

	// 1. Users.
	if _, err := sd.ensureUser(ctx, *adminEmail, *adminName, model.RoleOwner); err != nil {
		log.Fatalf("seed: %v", err)
	}
	if _, err := sd.ensureUser(ctx, *customerEmail, *customerName, model.RoleUser); err != nil {
		log.Fatalf("seed: %v", err)
	}

	// 2. Product + plans + entitlements + prices.
	product, err := sd.ensureProduct(ctx)
	if err != nil {
		log.Fatalf("seed: %v", err)
	}
	planIDs := map[string]string{}
	for _, ps := range demoPlans {
		id, err := sd.ensurePlan(ctx, product.ID, ps)
		if err != nil {
			log.Fatalf("seed: %v", err)
		}
		planIDs[ps.slug] = id
	}

	// 3. Coupons.
	if err := sd.ensureCoupons(ctx); err != nil {
		log.Fatalf("seed: %v", err)
	}

	// 4. Demo customer license (Professional).
	if err := sd.ensureDemoLicense(ctx, product.ID, planIDs["professional"], *customerEmail); err != nil {
		log.Fatalf("seed: %v", err)
	}

	log.Printf("seed complete: %d created/upserted, %d skipped", sd.created, sd.skipped)
}
