package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── Portal review submission ───

// fakeReviewPortalStore stands in for store.Store so the ownership
// rules run without a database. The own-review lookup is keyed by the
// (product, email) pair the handler passes — recording the pair is how
// the tests pin that the handler never strays from the session
// identity.
type fakeReviewPortalStore struct {
	product    *model.Product
	productErr error

	created   []*model.Review
	createErr error

	own       *model.Review
	ownErr    error
	gotOwnKey [2]string // product id, email

	contentWrites [][3]string // id, title, body
	deleted       []string
}

func (f *fakeReviewPortalStore) FindProductByID(_ context.Context, _ string) (*model.Product, error) {
	return f.product, f.productErr
}

func (f *fakeReviewPortalStore) FindProductBySlug(_ context.Context, _ string) (*model.Product, error) {
	return f.product, f.productErr
}

func (f *fakeReviewPortalStore) CreateReview(_ context.Context, r *model.Review) error {
	if f.createErr != nil {
		return f.createErr
	}
	r.ID = "r-new"
	r.CustomerEmail = model.NormalizeReviewEmail(r.CustomerEmail)
	r.Status = model.ReviewStatusPending // the store forces it; so does the fake
	f.created = append(f.created, r)
	return nil
}

func (f *fakeReviewPortalStore) FindReviewByProductAndEmail(_ context.Context, productID, email string) (*model.Review, error) {
	f.gotOwnKey = [2]string{productID, email}
	if f.ownErr != nil {
		return nil, f.ownErr
	}
	if f.own == nil {
		return nil, sql.ErrNoRows
	}
	cp := *f.own
	return &cp, nil
}

func (f *fakeReviewPortalStore) UpdateReviewContent(_ context.Context, id, title, body string) error {
	f.contentWrites = append(f.contentWrites, [3]string{id, title, body})
	return nil
}

func (f *fakeReviewPortalStore) DeleteReview(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// reviewPortalCtx builds the request context a session-authed portal
// review call arrives with: the session identity under "email" (and
// the name claim the review displays), a JSON body, and the product
// segment.
func reviewPortalCtx(t *testing.T, method, target, body, productID, email string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if email != "" {
		c.Set("email", email)
		c.Set("name", "Alice")
	}
	c.Params = gin.Params{{Key: "id", Value: productID}}
	return w, c
}

// Submission validation: rating 1..5, body required and bounded, title
// optional and bounded — and the row always starts pending with the
// folded session address as its author.
func TestReviewPortalCreateValidation(t *testing.T) {
	fake := &fakeReviewPortalStore{product: &model.Product{ID: "p1", Slug: "photo-suite"}}
	h := NewReviewPortalHandler(nil)
	h.store = fake

	// No session identity is 401 on every verb.
	for _, call := range []func(*gin.Context){h.Create, h.Update, h.Delete} {
		w, c := reviewPortalCtx(t, http.MethodPost, "/portal/products/p1/reviews", `{}`, "p1", "")
		call(c)
		if w.Code != 401 {
			t.Errorf("no session email: status = %d, want 401", w.Code)
		}
	}

	// An unknown product is a quiet 404 — and nothing is written.
	bad := &fakeReviewPortalStore{productErr: sql.ErrNoRows}
	hBad := NewReviewPortalHandler(nil)
	hBad.store = bad
	w, c := reviewPortalCtx(t, http.MethodPost, "/portal/products/ghost/reviews", `{"rating":5,"body":"hi"}`, "ghost", "alice@example.com")
	hBad.Create(c)
	if w.Code != 404 {
		t.Errorf("unknown product: status = %d, want 404", w.Code)
	}
	if len(bad.created) != 0 {
		t.Error("a review was written for a product that is not there")
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"rating 0", `{"rating":0,"body":"x"}`},
		{"rating 6", `{"rating":6,"body":"x"}`},
		{"no rating", `{"body":"x"}`},
		{"no body", `{"rating":4}`},
		{"empty body", `{"rating":4,"body":"   "}`},
		{"over-long title", `{"rating":4,"title":"` + strings.Repeat("t", 121) + `","body":"x"}`},
		{"over-long body", `{"rating":4,"body":"` + strings.Repeat("b", 4001) + `"}`},
		{"not json", `nope`},
	} {
		w, c := reviewPortalCtx(t, http.MethodPost, "/portal/products/p1/reviews", tc.body, "p1", "alice@example.com")
		h.Create(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
	}
	if len(fake.created) != 0 {
		t.Error("an invalid submission reached the store")
	}

	// The valid submission: folded author, session display name,
	// pending status, trimmed content.
	w, c = reviewPortalCtx(t, http.MethodPost, "/portal/products/p1/reviews",
		`{"rating":5,"title":"  Great  ","body":"  Loved it  "}`, "p1", "  ALICE@Example.COM ")
	h.Create(c)
	if w.Code != 201 {
		t.Fatalf("create: status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %d rows, want 1", len(fake.created))
	}
	got := fake.created[0]
	if got.CustomerEmail != "alice@example.com" {
		t.Errorf("author email = %q, want folded", got.CustomerEmail)
	}
	if got.CustomerName != "Alice" {
		t.Errorf("display name = %q, want the session's name claim", got.CustomerName)
	}
	if got.Status != model.ReviewStatusPending {
		t.Errorf("status = %q, want pending — submissions never publish themselves", got.Status)
	}
	if got.Title != "Great" || got.Body != "Loved it" {
		t.Errorf("content = (%q, %q), want trimmed", got.Title, got.Body)
	}

	// One review per (product, customer): the store's sentinel folds
	// to 409 DUPLICATE, the same code every duplicate handle answers.
	fake.createErr = store.ErrReviewAlreadyExists
	w2, c2 := reviewPortalCtx(t, http.MethodPost, "/portal/products/p1/reviews",
		`{"rating":1,"body":"again"}`, "p1", "alice@example.com")
	h.Create(c2)
	if w2.Code != 409 {
		t.Fatalf("duplicate: status = %d, want 409", w2.Code)
	}
	if code := reviewErrCode(t, w2); code != "DUPLICATE" {
		t.Errorf("duplicate code = %q, want DUPLICATE", code)
	}
}

// Ownership: the endpoints are keyed by the (product, session email)
// pair, so there is no request that names another customer's review —
// "not yours" and "never written" are the same quiet 404.
func TestReviewPortalOwnership(t *testing.T) {
	fake := &fakeReviewPortalStore{
		product: &model.Product{ID: "p1"},
		own: &model.Review{
			ID: "r-mine", ProductID: "p1", CustomerEmail: "alice@example.com",
			Rating: 4, Title: "Mine", Body: "my words",
		},
	}
	h := NewReviewPortalHandler(nil)
	h.store = fake

	// The lookup is by the session identity — nothing else.
	w, c := reviewPortalCtx(t, http.MethodPatch, "/portal/products/p1/reviews",
		`{"body":"rewritten"}`, "p1", "  ALICE@Example.COM ")
	h.Update(c)
	if w.Code != 200 {
		t.Fatalf("update: status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.gotOwnKey != [2]string{"p1", "  ALICE@Example.COM "} {
		t.Errorf("own lookup key = %v, want the raw session email + product id", fake.gotOwnKey)
	}
	if len(fake.contentWrites) != 1 || fake.contentWrites[0] != [3]string{"r-mine", "Mine", "rewritten"} {
		t.Errorf("content writes = %v, want the merged row once", fake.contentWrites)
	}

	// A title-only edit keeps the stored body; an empty body is
	// refused (the row shape has no wordless review).
	w2, c2 := reviewPortalCtx(t, http.MethodPatch, "/portal/products/p1/reviews",
		`{"title":"New title"}`, "p1", "alice@example.com")
	h.Update(c2)
	if w2.Code != 200 {
		t.Fatalf("title-only update: status = %d, want 200", w2.Code)
	}
	if got := fake.contentWrites[1]; got != [3]string{"r-mine", "New title", "my words"} {
		t.Errorf("title-only write = %v, want body kept", got)
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no fields", `{}`},
		{"empty body", `{"body":"   "}`},
		{"over-long title", `{"title":"` + strings.Repeat("t", 121) + `"}`},
	} {
		w, c := reviewPortalCtx(t, http.MethodPatch, "/portal/products/p1/reviews", tc.body, "p1", "alice@example.com")
		h.Update(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400", tc.name, w.Code)
		}
	}

	// Somebody else's review (or none at all) is the same quiet 404
	// on both mutating verbs — no existence oracle.
	stranger := &fakeReviewPortalStore{product: &model.Product{ID: "p1"}, ownErr: sql.ErrNoRows}
	hStranger := NewReviewPortalHandler(nil)
	hStranger.store = stranger
	for _, tc := range []struct {
		name string
		call func(*gin.Context)
		ctx  func() (*httptest.ResponseRecorder, *gin.Context)
	}{
		{"patch", hStranger.Update, func() (*httptest.ResponseRecorder, *gin.Context) {
			return reviewPortalCtx(t, http.MethodPatch, "/portal/products/p1/reviews", `{"body":"mine now"}`, "p1", "mallory@example.com")
		}},
		{"delete", hStranger.Delete, func() (*httptest.ResponseRecorder, *gin.Context) {
			return reviewPortalCtx(t, http.MethodDelete, "/portal/products/p1/reviews", "", "p1", "mallory@example.com")
		}},
	} {
		w, c := tc.ctx()
		tc.call(c)
		if w.Code != 404 {
			t.Errorf("cross-user %s: status = %d, want 404", tc.name, w.Code)
		}
		if len(stranger.contentWrites) != 0 || len(stranger.deleted) != 0 {
			t.Errorf("cross-user %s reached the store", tc.name)
		}
	}

	// The owner's delete takes exactly their own row down.
	w3, c3 := reviewPortalCtx(t, http.MethodDelete, "/portal/products/p1/reviews", "", "p1", "alice@example.com")
	h.Delete(c3)
	c3.Writer.WriteHeaderNow() // flush the recorded 204 (NoContent writes no body)
	if w3.Code != 204 {
		t.Fatalf("delete own: status = %d, want 204", w3.Code)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "r-mine" {
		t.Errorf("deleted = %v, want [r-mine]", fake.deleted)
	}
}

// ─── Public review surfaces on the marketplace ───

// fakeReviewMarket is the double for the two seams of
// MarketplaceHandler: the catalog (pre-existing) and the review reads
// this round adds. It records what the handler asked for — the
// approved-only filter is the whole of the public gate and is pinned
// here.
type fakeReviewMarket struct {
	product    *model.Product
	productErr error
	products   []*model.Product
	total      int

	reviews     []*model.Review
	reviewTotal int
	summary     model.RatingAggregate
	summaries   map[string]model.RatingAggregate
	related     []*model.Product

	gotReviewProduct string
	gotReviewStatus  string
	gotSummaryIDs    map[string]struct{}
	gotRelatedID     string
	gotRelatedLimit  int
}

func (f *fakeReviewMarket) FindProductByID(_ context.Context, _ string) (*model.Product, error) {
	return f.product, f.productErr
}

func (f *fakeReviewMarket) FindProductBySlug(_ context.Context, _ string) (*model.Product, error) {
	return f.product, f.productErr
}

func (f *fakeReviewMarket) ListCategoriesByPosition(_ context.Context) ([]*model.Category, error) {
	return nil, nil
}

func (f *fakeReviewMarket) ListMarketplaceProducts(_ context.Context, _ store.MarketplaceProductFilter) ([]*model.Product, int, error) {
	return f.products, f.total, nil
}

func (f *fakeReviewMarket) CategoriesForProducts(_ context.Context, _ []string) (map[string][]*model.Category, error) {
	return map[string][]*model.Category{}, nil
}

func (f *fakeReviewMarket) ActivePlansForProducts(_ context.Context, _ []string) (map[string][]*model.Plan, error) {
	return map[string][]*model.Plan{}, nil
}

func (f *fakeReviewMarket) ListReleases(_ context.Context, _ store.ReleaseFilter) ([]*model.Release, error) {
	return nil, nil
}

func (f *fakeReviewMarket) ListReviewsByProduct(_ context.Context, productID, status string, _ store.Page) ([]*model.Review, int, error) {
	f.gotReviewProduct, f.gotReviewStatus = productID, status
	return f.reviews, f.reviewTotal, nil
}

func (f *fakeReviewMarket) RatingSummaryForProduct(_ context.Context, _ string) (model.RatingAggregate, error) {
	return f.summary, nil
}

func (f *fakeReviewMarket) RatingSummariesForProducts(_ context.Context, ids map[string]struct{}) (map[string]model.RatingAggregate, error) {
	f.gotSummaryIDs = ids
	return f.summaries, nil
}

func (f *fakeReviewMarket) RelatedProducts(_ context.Context, productID string, limit int) ([]*model.Product, error) {
	f.gotRelatedID, f.gotRelatedLimit = productID, limit
	return f.related, nil
}

// mktReviewCtx builds the request context for one of the new public
// endpoints, with the product segment the route carries.
func mktReviewCtx(t *testing.T, method, target, productID string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Params = gin.Params{{Key: "id", Value: productID}}
	return w, c
}

// The public review list sees approved rows only — the filter is not
// a parameter, it is the gate — carries the aggregate beside the
// page, and NEVER the author's email: the field selection is the
// privacy boundary.
func TestMarketplaceListReviewsApprovedOnlyNoEmail(t *testing.T) {
	fake := &fakeReviewMarket{
		product: &model.Product{ID: "p1", Slug: "photo-suite"},
		reviews: []*model.Review{{
			ID: "r1", ProductID: "p1", CustomerEmail: "alice@example.com",
			CustomerName: "Alice", Rating: 5, Title: "Great", Body: "Loved it",
			Status: model.ReviewStatusApproved,
		}},
		reviewTotal: 1,
		summary:     model.RatingAggregate{Count: 2, AverageBPS: 45000},
	}
	h := &MarketplaceHandler{mk: fake, rv: fake}

	w, c := mktReviewCtx(t, http.MethodGet, "/public/marketplace/products/p1/reviews", "p1")
	h.ListReviews(c)
	if w.Code != 200 {
		t.Fatalf("list reviews: status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.gotReviewProduct != "p1" || fake.gotReviewStatus != model.ReviewStatusApproved {
		t.Errorf("review filter = (%q, %q), want (p1, approved) — the public side has no status knob",
			fake.gotReviewProduct, fake.gotReviewStatus)
	}
	body := w.Body.String()
	for _, want := range []string{
		`"reviews"`, `"customer_name":"Alice"`, `"rating":5`, `"body":"Loved it"`,
		`"rating_average_bps":45000`, `"rating_count":2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("review list is missing %s: %s", want, body)
		}
	}
	for _, leak := range []string{"alice@", "customer_email", "example.com"} {
		if strings.Contains(body, leak) {
			t.Errorf("review list leaks %q: %s", leak, body)
		}
	}

	// An unknown product is the quiet 404.
	miss := &fakeReviewMarket{productErr: sql.ErrNoRows}
	hMiss := &MarketplaceHandler{mk: miss, rv: miss}
	w2, c2 := mktReviewCtx(t, http.MethodGet, "/public/marketplace/products/ghost/reviews", "ghost")
	hMiss.ListReviews(c2)
	if w2.Code != 404 {
		t.Errorf("unknown product reviews: status = %d, want 404", w2.Code)
	}
}

// The related rail answers with the same product cards the listing
// renders — categories, plans, rating keys — and forwards the caller's
// limit (the store clamps it).
func TestMarketplaceRelatedProductsRail(t *testing.T) {
	fake := &fakeReviewMarket{
		product: &model.Product{ID: "p1", Slug: "photo-suite"},
		related: []*model.Product{{ID: "p2", Name: "Other", Slug: "other", Type: "desktop"}},
		summaries: map[string]model.RatingAggregate{
			"p2": {Count: 3, AverageBPS: 40000},
		},
	}
	h := &MarketplaceHandler{mk: fake, rv: fake}

	w, c := mktReviewCtx(t, http.MethodGet, "/public/marketplace/products/p1/related?limit=4", "p1")
	h.RelatedProducts(c)
	if w.Code != 200 {
		t.Fatalf("related: status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.gotRelatedID != "p1" {
		t.Errorf("related product = %q, want p1 (self is the anchor, the store excludes it)", fake.gotRelatedID)
	}
	if fake.gotRelatedLimit != 4 {
		t.Errorf("related limit = %d, want the caller's 4 (the store caps at 8)", fake.gotRelatedLimit)
	}
	body := w.Body.String()
	for _, want := range []string{`"products"`, `"slug":"other"`, `"rating_average_bps":40000`, `"rating_count":3`, `"total":1`} {
		if !strings.Contains(body, want) {
			t.Errorf("related body is missing %s: %s", want, body)
		}
	}
}

// The rating keys are additive on every product DTO — list cards and
// the detail alike — and drawn from the batch aggregate for the whole
// page in one call. A handler wired without the review seam renders
// the same keys at zero: "no published ratings yet", not a gap.
func TestMarketplaceProductDTOCarriesRatings(t *testing.T) {
	fake := &fakeReviewMarket{
		product: &model.Product{ID: "p1", Slug: "photo-suite", Name: "Photo Suite", Type: "desktop"},
		products: []*model.Product{
			{ID: "p1", Slug: "photo-suite", Name: "Photo Suite", Type: "desktop"},
			{ID: "p2", Slug: "other", Name: "Other", Type: "desktop"},
		},
		total: 2,
		summaries: map[string]model.RatingAggregate{
			"p1": {Count: 2, AverageBPS: 45000},
			// p2 has none — the zero value, no special case.
		},
		summary: model.RatingAggregate{Count: 2, AverageBPS: 45000},
	}
	h := &MarketplaceHandler{mk: fake, rv: fake}

	// The listing: one batch for the whole page.
	w, c := mktReviewCtx(t, http.MethodGet, "/marketplace/products", "")
	h.ListProducts(c)
	if w.Code != 200 {
		t.Fatalf("list: status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if len(fake.gotSummaryIDs) != 2 {
		t.Errorf("batched summary ids = %v, want both cards in ONE call", fake.gotSummaryIDs)
	}
	body := w.Body.String()
	for _, want := range []string{`"rating_average_bps":45000`, `"rating_count":2`, `"rating_average_bps":0`, `"rating_count":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("card body is missing %s: %s", want, body)
		}
	}

	// The detail.
	w2, c2 := mktReviewCtx(t, http.MethodGet, "/marketplace/products/photo-suite", "")
	c2.Params = gin.Params{{Key: "slug", Value: "photo-suite"}}
	h.GetProduct(c2)
	if w2.Code != 200 {
		t.Fatalf("detail: status = %d, want 200", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), `"rating_average_bps":45000`) {
		t.Errorf("detail body has no rating aggregate: %s", w2.Body.String())
	}

	// No review seam (the pre-review doubles): the keys are still
	// there, at the honest zero.
	plain := &MarketplaceHandler{mk: fake}
	w3, c3 := mktReviewCtx(t, http.MethodGet, "/marketplace/products", "")
	plain.ListProducts(c3)
	if !strings.Contains(w3.Body.String(), `"rating_average_bps":0`) {
		t.Errorf("seam-less card body has no rating keys: %s", w3.Body.String())
	}
	// ...and the review endpoints refuse rather than panic.
	w4, c4 := mktReviewCtx(t, http.MethodGet, "/public/marketplace/products/p1/reviews", "p1")
	plain.ListReviews(c4)
	if w4.Code != 500 {
		t.Errorf("seam-less review list status = %d, want 500", w4.Code)
	}
}

// The public review payload is a selection, not a scrub: whatever the
// model carries, this surface renders exactly the keys it names —
// and the author's address is not among them.
func TestReviewPublicJSONSelection(t *testing.T) {
	r := &model.Review{
		ID: "r1", ProductID: "p1", CustomerEmail: "alice@example.com",
		CustomerName: "Alice", Rating: 3, Title: "t", Body: "b",
		Status: model.ReviewStatusApproved, AdminReply: "thanks",
	}
	out := reviewPublicJSON(r)
	if _, ok := out["customer_email"]; ok {
		t.Error("the public review object carries customer_email")
	}
	if _, ok := out["status"]; ok {
		t.Error("the public review object carries the moderation status")
	}
	for _, key := range []string{"id", "customer_name", "rating", "title", "body", "admin_reply", "created_at"} {
		if _, ok := out[key]; !ok {
			t.Errorf("the public review object is missing %q", key)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "alice@example.com") {
		t.Errorf("the public review payload leaks the author: %s", raw)
	}
}
