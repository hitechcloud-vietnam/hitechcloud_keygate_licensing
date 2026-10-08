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

// fakeReviewAdminStore stands in for store.Store so the moderation
// machine runs without a database. Writes flow through to the stored
// rows exactly like the real store, so "approve twice" is testable:
// the second call sees the status the first one wrote.
type fakeReviewAdminStore struct {
	reviews map[string]*model.Review

	listCalls     int
	listStatus    string
	listProductID string

	statusWrites [][2]string // {id, target}
	replyWrites  [][2]string // {id, reply}
	deleted      []string
	audits       []*model.AuditLog
}

func (f *fakeReviewAdminStore) rows() []*model.Review {
	out := make([]*model.Review, 0, len(f.reviews))
	for _, r := range f.reviews {
		out = append(out, r)
	}
	return out
}

func (f *fakeReviewAdminStore) FindReviewByID(_ context.Context, id string) (*model.Review, error) {
	if r, ok := f.reviews[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeReviewAdminStore) ListReviewsByProduct(_ context.Context, productID, status string, _ store.Page) ([]*model.Review, int, error) {
	f.listCalls++
	f.listProductID, f.listStatus = productID, status
	return f.rows(), len(f.rows()), nil
}

func (f *fakeReviewAdminStore) ListAllReviews(_ context.Context, status string, _ store.Page) ([]*model.Review, int, error) {
	f.listCalls++
	f.listProductID, f.listStatus = "", status
	return f.rows(), len(f.rows()), nil
}

func (f *fakeReviewAdminStore) UpdateReviewStatus(_ context.Context, id, status string) error {
	f.statusWrites = append(f.statusWrites, [2]string{id, status})
	if r, ok := f.reviews[id]; ok {
		r.Status = status
		return nil
	}
	return sql.ErrNoRows
}

func (f *fakeReviewAdminStore) UpdateReviewReply(_ context.Context, id, reply string) error {
	f.replyWrites = append(f.replyWrites, [2]string{id, reply})
	if r, ok := f.reviews[id]; ok {
		r.AdminReply = reply
		return nil
	}
	return sql.ErrNoRows
}

func (f *fakeReviewAdminStore) DeleteReview(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if _, ok := f.reviews[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.reviews, id)
	return nil
}

func (f *fakeReviewAdminStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func reviewAdminHandler(f *fakeReviewAdminStore) *ReviewAdminHandler {
	gin.SetMode(gin.TestMode)
	h := NewReviewAdminHandler(nil)
	h.rev = f
	return h
}

// reviewAdminCtx builds the request context a moderation call arrives
// with: admin session, optional JSON body, and the route's :id.
func reviewAdminCtx(t *testing.T, method, target, body, id string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, target, rd)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "admin-1")
	c.Params = gin.Params{{Key: "id", Value: id}}
	return w, c
}

func reviewErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error envelope: %v; body %s", err, w.Body.String())
	}
	return body.Error.Code
}

// The moderation machine, in full: pending→approved|rejected,
// approved→rejected (unpublish), rejected→approved (republish), and
// every self-transition refused with 409 naming both statuses —
// "approve twice" surfaces instead of silently succeeding. Every
// successful move is audited as Entity "review".
func TestReviewAdminModerationTransitions(t *testing.T) {
	fake := &fakeReviewAdminStore{reviews: map[string]*model.Review{
		"r1": {ID: "r1", ProductID: "p1", Status: model.ReviewStatusPending, Body: "ok"},
	}}
	h := reviewAdminHandler(fake)

	// pending -> approved.
	w, c := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/r1/approve", "", "r1")
	h.Approve(c)
	if w.Code != 200 {
		t.Fatalf("approve pending: status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"approved"`) {
		t.Errorf("approve answer does not carry the new status: %s", w.Body.String())
	}

	// approve twice -> 409 with a clear, stable code.
	w2, c2 := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/r1/approve", "", "r1")
	h.Approve(c2)
	if w2.Code != 409 {
		t.Fatalf("approve twice: status = %d, want 409", w2.Code)
	}
	if code := reviewErrCode(t, w2); code != "REVIEW_TRANSITION_INVALID" {
		t.Errorf("approve twice code = %q, want REVIEW_TRANSITION_INVALID", code)
	}

	// approved -> rejected is the unpublish; rejected -> approved is
	// the republish; rejected -> rejected is refused like the rest.
	w3, c3 := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/r1/reject", "", "r1")
	h.Reject(c3)
	if w3.Code != 200 {
		t.Fatalf("unpublish: status = %d, want 200", w3.Code)
	}
	w4, c4 := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/r1/reject", "", "r1")
	h.Reject(c4)
	if w4.Code != 409 {
		t.Fatalf("reject twice: status = %d, want 409", w4.Code)
	}
	w5, c5 := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/r1/approve", "", "r1")
	h.Approve(c5)
	if w5.Code != 200 {
		t.Fatalf("republish: status = %d, want 200", w5.Code)
	}

	// An unknown review is 404 REVIEW_NOT_FOUND — and the write is
	// never attempted for it.
	w6, c6 := reviewAdminCtx(t, http.MethodPost, "/admin/reviews/ghost/approve", "", "ghost")
	h.Approve(c6)
	if w6.Code != 404 {
		t.Fatalf("approve missing: status = %d, want 404", w6.Code)
	}
	if code := reviewErrCode(t, w6); code != "REVIEW_NOT_FOUND" {
		t.Errorf("missing review code = %q, want REVIEW_NOT_FOUND", code)
	}
	for _, w := range fake.statusWrites {
		if w[0] == "ghost" {
			t.Error("a write was attempted for a review that is not there")
		}
	}

	// Every successful move audited (approve, unpublish, republish);
	// the refused moves audited nothing.
	if len(fake.audits) != 3 {
		t.Fatalf("audit entries = %d, want 3 (approve, reject, approve)", len(fake.audits))
	}
	for _, log := range fake.audits {
		if log.Entity != "review" || log.ActorID != "admin-1" || log.ActorType != "admin" {
			t.Errorf("audit entry = %+v, want Entity review + admin actor", log)
		}
	}
	if fake.audits[0].Action != "approved" || fake.audits[1].Action != "rejected" || fake.audits[2].Action != "approved" {
		t.Errorf("audit actions = %v, want [approved rejected approved]",
			[]string{fake.audits[0].Action, fake.audits[1].Action, fake.audits[2].Action})
	}
}

// The list: ?product_id narrows to one product's rows, ?status to one
// of the closed vocabulary, and a status outside it is refused with
// 400 before the store is touched.
func TestReviewAdminListFilters(t *testing.T) {
	fake := &fakeReviewAdminStore{reviews: map[string]*model.Review{
		"r1": {ID: "r1", ProductID: "p1", Status: model.ReviewStatusPending, Body: "ok"},
	}}
	h := reviewAdminHandler(fake)

	w, c := reviewAdminCtx(t, http.MethodGet, "/admin/reviews?status=maybe", "", "")
	h.List(c)
	if w.Code != 400 {
		t.Fatalf("bogus status: status = %d, want 400", w.Code)
	}
	if fake.listCalls != 0 {
		t.Errorf("bogus status reached the store %d times, want 0", fake.listCalls)
	}

	w2, c2 := reviewAdminCtx(t, http.MethodGet, "/admin/reviews?product_id=p1&status=pending&limit=5", "", "")
	h.List(c2)
	if w2.Code != 200 {
		t.Fatalf("filtered list: status = %d, want 200; body %s", w2.Code, w2.Body.String())
	}
	if fake.listProductID != "p1" || fake.listStatus != model.ReviewStatusPending {
		t.Errorf("list filter = (%q, %q), want (p1, pending)", fake.listProductID, fake.listStatus)
	}

	w3, c3 := reviewAdminCtx(t, http.MethodGet, "/admin/reviews?status=approved", "", "")
	h.List(c3)
	if w3.Code != 200 {
		t.Fatalf("unscoped list: status = %d, want 200", w3.Code)
	}
	if fake.listProductID != "" {
		t.Errorf("unscoped list was scoped to %q, want the whole queue", fake.listProductID)
	}

	// The shared list envelope: rows + total/limit/offset.
	var envelope struct {
		Data struct {
			Reviews []map[string]any `json:"reviews"`
			Total   int              `json:"total"`
			Limit   int              `json:"limit"`
			Offset  int              `json:"offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not the envelope: %v", err)
	}
	if envelope.Data.Total != 1 || envelope.Data.Limit != 5 {
		t.Errorf("envelope = %+v, want total 1 limit 5", envelope.Data)
	}
}

// The vendor reply: required, bounded, clearable — and audited.
func TestReviewAdminReply(t *testing.T) {
	fake := &fakeReviewAdminStore{reviews: map[string]*model.Review{
		"r1": {ID: "r1", ProductID: "p1", Status: model.ReviewStatusApproved, Body: "ok"},
	}}
	h := reviewAdminHandler(fake)

	w, c := reviewAdminCtx(t, http.MethodPut, "/admin/reviews/r1/reply", `{}`, "r1")
	h.Reply(c)
	if w.Code != 400 {
		t.Fatalf("missing admin_reply: status = %d, want 400", w.Code)
	}
	long := `{"admin_reply":"` + strings.Repeat("x", 4001) + `"}`
	w2, c2 := reviewAdminCtx(t, http.MethodPut, "/admin/reviews/r1/reply", long, "r1")
	h.Reply(c2)
	if w2.Code != 400 {
		t.Fatalf("over-long admin_reply: status = %d, want 400", w2.Code)
	}

	w3, c3 := reviewAdminCtx(t, http.MethodPut, "/admin/reviews/r1/reply", `{"admin_reply":"  Sorry to hear that  "}`, "r1")
	h.Reply(c3)
	if w3.Code != 200 {
		t.Fatalf("reply: status = %d, want 200; body %s", w3.Code, w3.Body.String())
	}
	if len(fake.replyWrites) != 1 || fake.replyWrites[0] != [2]string{"r1", "Sorry to hear that"} {
		t.Errorf("reply writes = %v, want the trimmed text once", fake.replyWrites)
	}

	// An empty reply clears — the store turns "" into NULL.
	w4, c4 := reviewAdminCtx(t, http.MethodPut, "/admin/reviews/r1/reply", `{"admin_reply":""}`, "r1")
	h.Reply(c4)
	if w4.Code != 200 {
		t.Fatalf("clear reply: status = %d, want 200", w4.Code)
	}
	if got := fake.replyWrites[1][1]; got != "" {
		t.Errorf("clear wrote %q, want empty (NULL at the store)", got)
	}

	if len(fake.audits) != 2 || fake.audits[0].Action != "replied" || fake.audits[1].Action != "replied" {
		t.Errorf("reply audits = %v, want two 'replied' entries", fake.audits)
	}

	w5, c5 := reviewAdminCtx(t, http.MethodPut, "/admin/reviews/ghost/reply", `{"admin_reply":"x"}`, "ghost")
	h.Reply(c5)
	if w5.Code != 404 {
		t.Fatalf("reply to missing review: status = %d, want 404", w5.Code)
	}
}

// The takedown: 204 and an audit entry when it existed, 404 when it
// did not (and no audit for a deletion that never happened).
func TestReviewAdminDelete(t *testing.T) {
	fake := &fakeReviewAdminStore{reviews: map[string]*model.Review{
		"r1": {ID: "r1", ProductID: "p1", Status: model.ReviewStatusApproved, Body: "spam"},
	}}
	h := reviewAdminHandler(fake)

	w, c := reviewAdminCtx(t, http.MethodDelete, "/admin/reviews/r1", "", "r1")
	h.Delete(c)
	c.Writer.WriteHeaderNow() // flush the recorded 204 (NoContent writes no body)
	if w.Code != 204 {
		t.Fatalf("delete: status = %d, want 204", w.Code)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deleted" || fake.audits[0].Entity != "review" {
		t.Errorf("delete audit = %+v, want Entity review Action deleted", fake.audits)
	}

	w2, c2 := reviewAdminCtx(t, http.MethodDelete, "/admin/reviews/r1", "", "r1")
	h.Delete(c2)
	if w2.Code != 404 {
		t.Fatalf("second delete: status = %d, want 404", w2.Code)
	}
	if len(fake.audits) != 1 {
		t.Errorf("a failed delete was audited: %v", fake.audits)
	}
}
