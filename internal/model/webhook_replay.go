package model

import "github.com/uptrace/bun"

// WebhookReplayDelivery is one webhook delivery row that REPLAYS an
// earlier delivery, marked with the id of the row it replays.
//
// Why a second type instead of a field on WebhookDelivery: model.go is
// shared core (plan §108 — no two agents may touch it), so the §35
// replay marker lives here, in its own additive file, exactly as
// WebhookDelivery.ReplayOf would have if the shared struct could be
// edited. Bun inlines the embedded WebhookDelivery (verified: the
// INSERT covers every ordinary delivery column plus replay_of), while
// the plain model.WebhookDelivery reads and writes every existing code
// path uses are untouched by the extra column.
//
// The bun.BaseModel tag is REQUIRED: without it bun would name the
// table after this wrapper (webhook_replay_deliveries); with it both
// types map to the one webhook_deliveries table. The type is
// INSERT-scoped to marking replays — no query in this codebase joins
// through it.
//
// A replay is dispatched with the ORIGINAL stored payload bytes (so a
// receiver's idempotency keys keep matching), a fresh
// X-HiTechCloud-Delivery id, the CURRENT signing secret, and an
// X-HiTechCloud-Replay: true marker header. The lineage is row
// metadata only — it never enters the signed body.
type WebhookReplayDelivery struct {
	bun.BaseModel `bun:"table:webhook_deliveries"`
	WebhookDelivery
	// ReplayOf is the id of the original delivery this row re-fires.
	ReplayOf string `bun:"column:replay_of" json:"replay_of"`
}
