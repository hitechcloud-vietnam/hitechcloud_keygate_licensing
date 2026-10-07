package store

import (
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// TestBunDefaultTableAliases pins the table alias Bun generates for the
// commerce models. Bun aliases a model by the snake_case of the STRUCT
// name ("orders" table, model.Order -> AS "order"), NOT by the table
// name. Hand-written WHERE/ORDER BY qualifiers must therefore use the
// struct-name alias (e.g. `"order".id`), not `orders.id` — otherwise the
// query fails on Postgres with "missing FROM-clause entry for table
// orders". This test is the regression pin for that exact bug class.
func TestBunDefaultTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string // alias Bun must generate
	}{
		{(*model.Order)(nil), `"order"`},
		{(*model.OrderItem)(nil), `"order_item"`},
		{(*model.Invoice)(nil), `"invoice"`},
		{(*model.Coupon)(nil), `"coupon"`},
		{(*model.TaxRate)(nil), `"tax_rate"`},
		{(*model.CustomerAPIKey)(nil), `"customer_api_key"`},
	}

	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		sqlText := string(raw)
		asClause := "AS " + tc.wantAlias
		if !strings.Contains(sqlText, asClause) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, asClause, sqlText)
		}
		t.Logf("%T -> %s", tc.model, sqlText)
	}
}
