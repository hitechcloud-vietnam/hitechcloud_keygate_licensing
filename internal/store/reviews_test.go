package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// reviewsTestDB is the TEST_DATABASE_URL-gated store for the review
// suite. Same contract as the store_test package's setupTestDB — no
// database, no test — kept local because this file also pins query
// shapes, which needs the unexported builders (package store, not
// store_test).
func reviewsTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

func reviewsSuffix() string { return time.Now().Format("150405.000000") }

// TestReviewTableAliases pins the aliases Bun generates for the
// review models — the bug class TestBunDefaultTableAliases pins for
// the commerce ones. Bun aliases by snake_case of the STRUCT name:
// model.Review is "review" (NOT the table name "product_reviews"), and
// relatedProductsQuery's correlation leans on model.Product being
// "product".
func TestReviewTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	for _, tc := range []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Review)(nil), `"review"`},
		{(*model.Product)(nil), `"product"`},
	} {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		if sqlText := string(raw); !strings.Contains(sqlText, "AS "+tc.wantAlias) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, "AS "+tc.wantAlias, sqlText)
		}
	}
}

// TestRelatedProductsQueryShape pins the rail statement without a
// database: the neighbour relation is an EXISTS over the real join
// table (its own SQL, immune to bun's model aliasing), the
// correlation into the outer table uses the "product" alias, the
// product itself is excluded, and the ordering is newest-first with
// the stable tiebreak.
func TestRelatedProductsQueryShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Product
	q := relatedProductsQuery(db, "p-self", 8, &dest)
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)

	for _, want := range []string{
		`FROM "products" AS "product"`, // the alias qualifiers must match
		"product.id <>",                // the product itself is never its own neighbour
		"product_categories mine",      // the relation is raw SQL: real table names
		"product_categories theirs",
		"theirs.category_id = mine.category_id",
		"mine.product_id =",              // bound parameter (rendered $n by pgdialect)
		"theirs.product_id = product.id", // correlated via the bun alias
		"product.created_at DESC NULLS LAST",
		"product.id DESC",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("related SQL does not contain %q; got:\n%s", want, sqlText)
		}
	}
	// The rail is capped in the statement itself, not just hoped for.
	if !strings.Contains(sqlText, "LIMIT") {
		t.Errorf("related SQL has no LIMIT; got:\n%s", sqlText)
	}
}

// The cap is the store's contract whatever a caller asks for.
func TestClampRelatedLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-1, RelatedProductsLimit},
		{0, RelatedProductsLimit},
		{1, 1},
		{8, 8},
		{9, RelatedProductsLimit},
		{1000, RelatedProductsLimit},
	} {
		if got := clampRelatedLimit(tc.in); got != tc.want {
			t.Errorf("clampRelatedLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The conflict detector answers on exactly the two spellings of a
// second review of one (product, customer) — the sentinel the write
// path folds the driver error into, and a raw unique violation that
// never passed through it — and on nothing else, or a genuine failure
// would be answered 409.
func TestIsReviewConflict(t *testing.T) {
	if IsReviewConflict(nil) {
		t.Error("nil reads as a review conflict")
	}
	if IsReviewConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as a review conflict")
	}
	if !IsReviewConflict(ErrReviewAlreadyExists) {
		t.Error("ErrReviewAlreadyExists is not recognised")
	}
	wrapped := fmt.Errorf("create review: %w", ErrReviewAlreadyExists)
	if !IsReviewConflict(wrapped) {
		t.Error("a wrapped ErrReviewAlreadyExists is not recognised")
	}
}

// One customer, one review per product — folded, so no spelling of an
// address evades the pair — and the write path folds a duplicate into
// the sentinel. A fresh review is always pending whatever the caller
// said.
func TestReviewCreateUniquenessAndPending(t *testing.T) {
	s := reviewsTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := reviewsSuffix()

	product := &model.Product{Name: "Review Target " + suffix, Slug: "review-target-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, product.ID)

	r := &model.Review{
		ProductID:     product.ID,
		CustomerEmail: "  Alice@Example.COM ",
		CustomerName:  "Alice",
		Rating:        5,
		Title:         "Great",
		Body:          "Loved it",
		Status:        model.ReviewStatusApproved, // ignored on purpose
	}
	if err := s.CreateReview(ctx, r); err != nil {
		t.Fatalf("create review: %v", err)
	}
	defer s.DeleteReview(ctx, r.ID)
	if r.Status != model.ReviewStatusPending {
		t.Errorf("created review status = %q, want pending (submissions cannot publish themselves)", r.Status)
	}
	if r.CustomerEmail != "alice@example.com" {
		t.Errorf("stored email = %q, want folded", r.CustomerEmail)
	}

	// Same product, same customer, different spelling — the same pair.
	dup := &model.Review{
		ProductID: product.ID, CustomerEmail: "ALICE@example.com", Rating: 1, Body: "changed my mind",
	}
	err := s.CreateReview(ctx, dup)
	if err == nil {
		t.Fatal("second review of one product by one customer was accepted")
	}
	if !IsReviewConflict(err) {
		t.Errorf("duplicate create: IsReviewConflict(%v) = false", err)
	}
}

// The public listing sees approved rows only; the admin queue sees
// what it filters for. The filter is the whole of the gate.
func TestReviewListApprovedOnly(t *testing.T) {
	s := reviewsTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := reviewsSuffix()

	product := &model.Product{Name: "Listed " + suffix, Slug: "listed-" + suffix, Type: "saas"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, product.ID)

	mk := func(email string) *model.Review {
		r := &model.Review{ProductID: product.ID, CustomerEmail: email, Rating: 3, Body: "ok"}
		if err := s.CreateReview(ctx, r); err != nil {
			t.Fatalf("create review %s: %v", email, err)
		}
		t.Cleanup(func() { s.DeleteReview(ctx, r.ID) })
		return r
	}
	pending := mk("pending-" + suffix + "@example.com")
	approved := mk("approved-" + suffix + "@example.com")
	rejected := mk("rejected-" + suffix + "@example.com")
	if err := s.UpdateReviewStatus(ctx, approved.ID, model.ReviewStatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := s.UpdateReviewStatus(ctx, rejected.ID, model.ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Public: approved only.
	revs, total, err := s.ListReviewsByProduct(ctx, product.ID, model.ReviewStatusApproved, Page{Limit: 50})
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	if total != 1 || len(revs) != 1 || revs[0].ID != approved.ID {
		t.Fatalf("approved list = %d rows (total %d), want just %s", len(revs), total, approved.ID)
	}

	// The queue: each status answers its rows, empty answers all.
	for _, tc := range []struct {
		status string
		want   int
	}{
		{model.ReviewStatusPending, 1},
		{model.ReviewStatusApproved, 1},
		{model.ReviewStatusRejected, 1},
		{"", 3},
	} {
		_, n, err := s.ListReviewsByProduct(ctx, product.ID, tc.status, Page{Limit: 50})
		if err != nil {
			t.Fatalf("list %q: %v", tc.status, err)
		}
		if n != tc.want {
			t.Errorf("list %q total = %d, want %d", tc.status, n, tc.want)
		}
	}

	// The admin queue spans the catalog and filters the same way.
	all, allTotal, err := s.ListAllReviews(ctx, model.ReviewStatusPending, Page{Limit: 200})
	if err != nil {
		t.Fatalf("list all pending: %v", err)
	}
	found := false
	for _, r := range all {
		if r.ID == pending.ID {
			found = true
		}
	}
	if allTotal < 1 || !found {
		t.Errorf("admin pending queue (total %d) does not carry the pending review", allTotal)
	}

	// A miss on the ownership lookup is ErrNoRows, not a silent nil.
	if _, err := s.FindReviewByProductAndEmail(ctx, product.ID, "nobody-"+suffix+"@example.com"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindReviewByProductAndEmail(miss) = %v, want sql.ErrNoRows", err)
	}
	// ...and ownership survives any spelling of the session address.
	spelled := "  " + strings.ToUpper("approved-"+suffix+"@example.com") + " "
	if got, err := s.FindReviewByProductAndEmail(ctx, product.ID, spelled); err != nil || got.ID != approved.ID {
		t.Errorf("FindReviewByProductAndEmail(folded) = (%v, %v), want %s", got, err, approved.ID)
	}
}

// The aggregate is drawn from approved rows only and rounds exactly
// like the model says — 4+5 stars is 45000 bps, computed twice (the
// singular and the batch form must agree).
func TestReviewRatingSummaries(t *testing.T) {
	s := reviewsTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := reviewsSuffix()

	product := &model.Product{Name: "Rated " + suffix, Slug: "rated-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, product.ID)

	mk := func(email string, rating int) *model.Review {
		r := &model.Review{ProductID: product.ID, CustomerEmail: email, Rating: rating, Body: "x"}
		if err := s.CreateReview(ctx, r); err != nil {
			t.Fatalf("create review: %v", err)
		}
		t.Cleanup(func() { s.DeleteReview(ctx, r.ID) })
		return r
	}
	four := mk("four-"+suffix+"@example.com", 4)
	five := mk("five-"+suffix+"@example.com", 5)
	// Noise the summary must not count: a rejected 1-star on the same
	// product, and approved 1-stars on other products.
	noise := mk("noise-"+suffix+"@example.com", 1)
	if err := s.UpdateReviewStatus(ctx, noise.ID, model.ReviewStatusRejected); err != nil {
		t.Fatalf("reject noise: %v", err)
	}
	other := &model.Product{Name: "Other " + suffix, Slug: "other-rated-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, other); err != nil {
		t.Fatalf("create other product: %v", err)
	}
	defer s.DeleteProduct(ctx, other.ID)
	otherReview := &model.Review{ProductID: other.ID, CustomerEmail: "other-" + suffix + "@example.com", Rating: 1, Body: "x"}
	if err := s.CreateReview(ctx, otherReview); err != nil {
		t.Fatalf("create other review: %v", err)
	}
	t.Cleanup(func() { s.DeleteReview(ctx, otherReview.ID) })
	if err := s.UpdateReviewStatus(ctx, otherReview.ID, model.ReviewStatusApproved); err != nil {
		t.Fatalf("approve other: %v", err)
	}

	// Approve the two real ratings last, so nothing else is approved
	// on this product.
	if err := s.UpdateReviewStatus(ctx, four.ID, model.ReviewStatusApproved); err != nil {
		t.Fatalf("approve 4: %v", err)
	}
	if err := s.UpdateReviewStatus(ctx, five.ID, model.ReviewStatusApproved); err != nil {
		t.Fatalf("approve 5: %v", err)
	}

	want := model.RatingAggregate{Count: 2, AverageBPS: 45000} // (4+5)/2 = 4.5 stars
	got, err := s.RatingSummaryForProduct(ctx, product.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got != want {
		t.Errorf("RatingSummaryForProduct = %+v, want %+v", got, want)
	}

	// Batch form: same numbers, one query, keyed by product id.
	batch, err := s.RatingSummariesForProducts(ctx, map[string]struct{}{
		product.ID: {}, other.ID: {}, "no-such-product": {},
	})
	if err != nil {
		t.Fatalf("batch summaries: %v", err)
	}
	if batch[product.ID] != want {
		t.Errorf("batch[%s] = %+v, want %+v", product.ID, batch[product.ID], want)
	}
	if batch[other.ID] != (model.RatingAggregate{Count: 1, AverageBPS: 10000}) {
		t.Errorf("batch[%s] = %+v, want {1 10000}", other.ID, batch[other.ID])
	}
	if _, ok := batch["no-such-product"]; ok {
		t.Error("batch carries an entry for a product with no approved reviews")
	}
	// The unreviewed product reads as the zero value, not an error.
	empty := &model.Product{Name: "Empty " + suffix, Slug: "empty-rated-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, empty); err != nil {
		t.Fatalf("create empty product: %v", err)
	}
	defer s.DeleteProduct(ctx, empty.ID)
	if z, err := s.RatingSummaryForProduct(ctx, empty.ID); err != nil || z != (model.RatingAggregate{}) {
		t.Errorf("summary of unreviewed product = (%+v, %v), want zero value", z, err)
	}
}

// The moderation writes: status stamps, reply set and cleared to NULL,
// the author's own edit, and a delete that answers ErrNoRows the
// second time.
func TestReviewModerationWrites(t *testing.T) {
	s := reviewsTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := reviewsSuffix()

	product := &model.Product{Name: "Moderated " + suffix, Slug: "moderated-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, product.ID)
	r := &model.Review{ProductID: product.ID, CustomerEmail: "mod-" + suffix + "@example.com", Rating: 2, Body: "meh"}
	if err := s.CreateReview(ctx, r); err != nil {
		t.Fatalf("create review: %v", err)
	}
	defer s.DeleteReview(ctx, r.ID)

	if err := s.UpdateReviewStatus(ctx, r.ID, model.ReviewStatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got, err := s.FindReviewByID(ctx, r.ID); err != nil || got.Status != model.ReviewStatusApproved {
		t.Fatalf("after approve: (%+v, %v)", got, err)
	}

	if err := s.UpdateReviewReply(ctx, r.ID, "Sorry to hear that"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if got, err := s.FindReviewByID(ctx, r.ID); err != nil || got.AdminReply != "Sorry to hear that" {
		t.Fatalf("after reply: (%+v, %v)", got, err)
	}
	// An empty reply clears to NULL, not empty string.
	if err := s.UpdateReviewReply(ctx, r.ID, ""); err != nil {
		t.Fatalf("clear reply: %v", err)
	}
	if got, err := s.FindReviewByID(ctx, r.ID); err != nil || got.AdminReply != "" {
		t.Fatalf("after clear: reply=%q err=%v, want empty (NULL)", got.AdminReply, err)
	}

	if err := s.UpdateReviewContent(ctx, r.ID, "edited", "better now"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, err := s.FindReviewByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("find after edit: %v", err)
	}
	if got.Title != "edited" || got.Body != "better now" {
		t.Errorf("after edit: title=%q body=%q", got.Title, got.Body)
	}
	if got.Status != model.ReviewStatusApproved {
		t.Errorf("the author's edit moved the status to %q — content edits must not touch moderation", got.Status)
	}

	// Writes to a review that is not there say so.
	for name, err := range map[string]error{
		"status": s.UpdateReviewStatus(ctx, "no-such-review", model.ReviewStatusApproved),
		"reply":  s.UpdateReviewReply(ctx, "no-such-review", "x"),
		"edit":   s.UpdateReviewContent(ctx, "no-such-review", "t", "b"),
		"delete": s.DeleteReview(ctx, "no-such-review"),
	} {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s of a missing review = %v, want sql.ErrNoRows", name, err)
		}
	}
	if err := s.DeleteReview(ctx, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteReview(ctx, r.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second delete = %v, want sql.ErrNoRows", err)
	}
}

// The related rail: same category, self excluded, newest first, and
// the store's cap holds whatever the caller asks for. Products in
// other categories — and products with no categories at all — are not
// neighbours.
func TestRelatedProductsExcludesSelfAndOtherCategories(t *testing.T) {
	s := reviewsTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := reviewsSuffix()

	mkProduct := func(label, slug string, created time.Time) *model.Product {
		p := &model.Product{Name: label + " " + suffix, Slug: slug + "-" + suffix, Type: "desktop"}
		if err := s.CreateProduct(ctx, p); err != nil {
			t.Fatalf("create product %s: %v", label, err)
		}
		t.Cleanup(func() { s.DeleteProduct(ctx, p.ID) })
		// Pin the ordering: two inserts in the same millisecond would
		// share now() and fall to the random-id tiebreak.
		if _, err := s.DB.NewRaw("UPDATE products SET created_at = ? WHERE id = ?", created, p.ID).Exec(ctx); err != nil {
			t.Fatalf("pin created_at: %v", err)
		}
		return p
	}

	cat := &model.Category{Name: "Rail " + suffix, Slug: "rail-" + suffix, Position: 1}
	if err := s.CreateCategory(ctx, cat); err != nil {
		t.Fatalf("create category: %v", err)
	}
	defer s.DeleteCategory(ctx, cat.ID)
	otherCat := &model.Category{Name: "Elsewhere " + suffix, Slug: "elsewhere-" + suffix, Position: 2}
	if err := s.CreateCategory(ctx, otherCat); err != nil {
		t.Fatalf("create other category: %v", err)
	}
	defer s.DeleteCategory(ctx, otherCat.ID)

	base := time.Now()
	self := mkProduct("Self", "self", base.Add(-4*time.Hour))
	older := mkProduct("Older", "older", base.Add(-3*time.Hour))
	newer := mkProduct("Newer", "newer", base.Add(-2*time.Hour))
	stranger := mkProduct("Stranger", "stranger", base.Add(-1*time.Hour))
	lonely := mkProduct("Lonely", "lonely", base)

	for _, p := range []*model.Product{self, older, newer} {
		if err := s.ReplaceProductCategories(ctx, p.ID, []string{cat.ID}); err != nil {
			t.Fatalf("categorize %s: %v", p.Name, err)
		}
	}
	if err := s.ReplaceProductCategories(ctx, stranger.ID, []string{otherCat.ID}); err != nil {
		t.Fatalf("categorize stranger: %v", err)
	}
	// lonely gets no categories at all.

	rel, err := s.RelatedProducts(ctx, self.ID, RelatedProductsLimit)
	if err != nil {
		t.Fatalf("related: %v", err)
	}
	if len(rel) != 2 {
		t.Fatalf("related = %d rows, want 2 (older + newer); got %+v", len(rel), names(rel))
	}
	if rel[0].ID != newer.ID || rel[1].ID != older.ID {
		t.Errorf("related order = %v, want newest first [%s %s]", names(rel), newer.Name, older.Name)
	}
	for _, r := range rel {
		if r.ID == self.ID {
			t.Error("the product is its own neighbour")
		}
		if r.ID == stranger.ID {
			t.Error("a product in another category leaked onto the rail")
		}
		if r.ID == lonely.ID {
			t.Error("an uncategorized product leaked onto the rail")
		}
	}

	// The cap holds at the store: ask for a hundred, get eight.
	if rel, err := s.RelatedProducts(ctx, self.ID, 100); err != nil || len(rel) > RelatedProductsLimit {
		t.Errorf("RelatedProducts(100) = %d rows (err %v), want at most %d", len(rel), err, RelatedProductsLimit)
	}
	// A product with no relatives reads as an empty rail, not an error.
	if rel, err := s.RelatedProducts(ctx, lonely.ID, RelatedProductsLimit); err != nil || len(rel) != 0 {
		t.Errorf("related of uncategorized product = %v (err %v), want empty", names(rel), err)
	}
}

func names(prods []*model.Product) []string {
	out := make([]string, 0, len(prods))
	for _, p := range prods {
		out = append(out, p.Name)
	}
	return out
}
