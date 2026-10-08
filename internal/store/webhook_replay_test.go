package store

// §35 replay — DB-free pins for the replay row shape.
//
// model.WebhookReplayDelivery embeds model.WebhookDelivery and adds the
// replay_of column (added by 20261008_146000_perf_indexes). These pins
// prove bun FLATTENS the embedded struct in both directions — the
// INSERT writes webhook_deliveries.replay_of alongside every ordinary
// delivery column, and the JSON carries replay_of beside the delivery
// fields — so a replay row is one ordinary delivery row plus one mark,
// and the plain model.WebhookDelivery code paths are unaffected.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

func TestWebhookReplayDeliveryInsertShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	d := &model.WebhookReplayDelivery{
		WebhookDelivery: model.WebhookDelivery{
			ID:        "dlv_replay_1",
			WebhookID: "wh_1",
			Event:     "order.created",
			Payload:   map[string]any{"event": "order.created"},
			Status:    "pending",
		},
		ReplayOf: "dlv_orig_1",
	}

	raw, err := db.NewInsert().Model(d).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`INSERT INTO "webhook_deliveries"`,
		`"replay_of"`,
		`"webhook_id"`,
		`"event"`,
		`"status"`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("INSERT is missing %s — the embedded struct is not being flattened or the table is wrong; got:\n%s", want, sqlText)
		}
	}

	sel, err := db.NewSelect().Model(d).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build select: %v", err)
	}
	if sqlText := string(sel); !strings.Contains(sqlText, `"replay_of"`) {
		t.Errorf("SELECT does not read replay_of back; got:\n%s", sqlText)
	}

	// JSON: the replay mark rides beside the delivery fields so the
	// replay endpoint's 201 body shows its lineage.
	rawJSON, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(rawJSON), `"replay_of":"dlv_orig_1"`) {
		t.Errorf("JSON is missing the replay_of mark: %s", rawJSON)
	}
	for _, want := range []string{`"webhook_id":"wh_1"`, `"event":"order.created"`, `"status":"pending"`} {
		if !strings.Contains(string(rawJSON), want) {
			t.Errorf("JSON lost a delivery field (%s): %s", want, rawJSON)
		}
	}
}
