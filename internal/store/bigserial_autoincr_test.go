package store

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// BIGSERIAL rows pin: every int64 primary key backed by a Postgres
// BIGSERIAL MUST carry the `autoincrement` bun tag. Without it bun
// sends an explicit id=0 in the INSERT column list — the first row
// lands as id 0 and the second INSERT dies on plan_prices_pkey (seen
// live in the seed run against a real database). TEXT/uuid keys
// generate their ids in the app and are unaffected.
func TestBigSerialPKsAreAutoIncrement(t *testing.T) {
	cases := []any{
		PlanPrice{},
		GatewayPayment{},
		model.Refund{},
	}
	for _, m := range cases {
		typ := reflect.TypeOf(m)
		field, ok := typ.FieldByName("ID")
		if !ok {
			t.Errorf("%s has no ID field", typ.Name())
			continue
		}
		tag := field.Tag.Get("bun")
		if !strings.Contains(tag, "pk") {
			t.Errorf("%s.ID bun tag %q lacks pk", typ.Name(), tag)
		}
		if !strings.Contains(tag, "autoincrement") {
			t.Errorf("%s.ID bun tag %q lacks autoincrement — bun would insert id=0 explicitly and collide on the second row", typ.Name(), tag)
		}
	}
}
