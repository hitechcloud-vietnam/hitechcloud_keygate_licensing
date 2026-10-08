package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Customer webhook store tests (integration: skip without a database,
// same guard as every other store test — see setupTestDB).

func createWebhookTestUser(t *testing.T, s *store.Store) string {
	t.Helper()
	id := store.NewID()
	if _, err := s.DB.NewRaw(
		"INSERT INTO users (id, email, name) VALUES (?, ?, ?)",
		id, "webhooks-"+id+"@example.com", "Webhook Tester",
	).Exec(context.Background()); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func deleteWebhookTestData(t *testing.T, s *store.Store, userIDs ...string) {
	t.Helper()
	for _, id := range userIDs {
		if _, err := s.DB.NewRaw("DELETE FROM customer_webhooks WHERE user_id = ?", id).Exec(context.Background()); err != nil {
			t.Fatalf("cleanup webhooks: %v", err)
		}
		if _, err := s.DB.NewRaw("DELETE FROM users WHERE id = ?", id).Exec(context.Background()); err != nil {
			t.Fatalf("cleanup user: %v", err)
		}
	}
}

// The whole life of an endpoint: create (secret + prefix derived), find
// by id, list scoped to the owner, update only the mutable columns (the
// secret survives untouched), stamp last-delivery, find-for-event
// (active + subscribed), count for the cap, hard-delete.
func TestCustomerWebhookLifecycle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	alice := createWebhookTestUser(t, s)
	bob := createWebhookTestUser(t, s)
	defer deleteWebhookTestData(t, s, alice, bob)

	secret, err := model.NewCustomerWebhookSecret()
	if err != nil {
		t.Fatalf("NewCustomerWebhookSecret: %v", err)
	}
	wh := &model.CustomerWebhook{
		UserID: alice,
		URL:    "https://example.com/hooks",
		Events: []string{"license.created", "quota.exceeded"},
		Active: true,
	}
	if err := s.CreateCustomerWebhook(ctx, wh, secret); err != nil {
		t.Fatalf("CreateCustomerWebhook: %v", err)
	}
	if wh.ID == "" {
		t.Fatal("CreateCustomerWebhook left the ID empty")
	}
	if wh.Secret != secret {
		t.Error("the row must carry the signing secret for the delivery signer")
	}
	if want := model.CustomerWebhookDisplayPrefix(secret); wh.SecretPrefix != want {
		t.Errorf("SecretPrefix = %q, want %q", wh.SecretPrefix, want)
	}

	got, err := s.FindCustomerWebhookByID(ctx, wh.ID)
	if err != nil {
		t.Fatalf("FindCustomerWebhookByID: %v", err)
	}
	if got.UserID != alice || got.URL != "https://example.com/hooks" || len(got.Events) != 2 {
		t.Errorf("round-trip = %+v, want alice / url / 2 events", got)
	}
	if got.Secret != secret {
		t.Error("find must return the signing secret (for the signer)")
	}
	if got.LastDeliveryAt != nil {
		t.Error("a fresh endpoint must have no last_delivery_at")
	}

	// List is scoped to the owner.
	bobSecret, _ := model.NewCustomerWebhookSecret()
	bobWh := &model.CustomerWebhook{UserID: bob, URL: "https://bob.example.com/h", Events: []string{"seat.added"}, Active: true}
	if err := s.CreateCustomerWebhook(ctx, bobWh, bobSecret); err != nil {
		t.Fatalf("CreateCustomerWebhook (bob): %v", err)
	}
	page := store.Page{Limit: 50, Offset: 0}
	aliceList, total, err := s.ListCustomerWebhooksByUser(ctx, alice, page)
	if err != nil {
		t.Fatalf("ListCustomerWebhooksByUser: %v", err)
	}
	if total != 1 || len(aliceList) != 1 || aliceList[0].ID != wh.ID {
		t.Errorf("alice list = %d rows %+v, want just hers", total, aliceList)
	}

	// Update touches only url/events/active — the secret is untouched.
	wh.URL = "https://example.com/new"
	wh.Events = []string{"license.created"}
	wh.Active = false
	if err := s.UpdateCustomerWebhook(ctx, wh); err != nil {
		t.Fatalf("UpdateCustomerWebhook: %v", err)
	}
	after, _ := s.FindCustomerWebhookByID(ctx, wh.ID)
	if after.URL != "https://example.com/new" || after.Active || len(after.Events) != 1 {
		t.Errorf("after update = %+v, want new url / inactive / 1 event", after)
	}
	if after.Secret != secret {
		t.Error("update must not disturb the signing secret")
	}

	// Find-for-event only returns active + subscribed rows.
	found, err := s.FindCustomerWebhooksForEvent(ctx, "license.created")
	if err != nil {
		t.Fatalf("FindCustomerWebhooksForEvent: %v", err)
	}
	for _, w := range found {
		if w.ID == wh.ID {
			t.Error("inactive endpoint must not be selected for real delivery")
		}
	}
	if err := s.UpdateCustomerWebhook(ctx, func() *model.CustomerWebhook {
		after.Active = true
		return after
	}()); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	found, _ = s.FindCustomerWebhooksForEvent(ctx, "license.created")
	active := false
	for _, w := range found {
		if w.ID == wh.ID {
			active = true
		}
	}
	if !active {
		t.Error("active + subscribed endpoint must be selected for delivery")
	}
	if found, _ := s.FindCustomerWebhooksForEvent(ctx, "release.yanked"); len(found) != 0 {
		t.Error("unsubscribed event must not select this endpoint")
	}

	// last_delivery_at stamps.
	if err := s.TouchCustomerWebhookLastDelivery(ctx, wh.ID); err != nil {
		t.Fatalf("TouchCustomerWebhookLastDelivery: %v", err)
	}
	touched, _ := s.FindCustomerWebhookByID(ctx, wh.ID)
	if touched.LastDeliveryAt == nil {
		t.Error("TouchCustomerWebhookLastDelivery must set last_delivery_at")
	}

	// Count feeds the per-user cap.
	if n, err := s.CountCustomerWebhooksByUser(ctx, alice); err != nil || n != 1 {
		t.Errorf("CountCustomerWebhooksByUser = %d, %v; want 1", n, err)
	}

	// Hard delete.
	if err := s.DeleteCustomerWebhook(ctx, wh.ID); err != nil {
		t.Fatalf("DeleteCustomerWebhook: %v", err)
	}
	if _, err := s.FindCustomerWebhookByID(ctx, wh.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find after delete = %v, want sql.ErrNoRows", err)
	}
}
