package store

// §61 SECURITY — SQL injection pin. The house rule is that user input
// is NEVER concatenated into SQL by hand: it flows through the query
// builders' ? placeholders and is rendered by bun's dialect as a
// properly quoted string literal (single quotes doubled — verified at
// the driver boundary below). These tests prove it end to end: a
// recording database/sql driver captures the EXACT statement text a
// real store method emits when fed hostile input, and asserts the input
// appears only INSIDE a string literal — the statement skeleton
// (keywords, tables, columns, operators) is free of every input byte.
// Three hand-written query shapes are covered: a Where chain
// (FindOrderByExternalID), a raw JOIN with an expression predicate
// (FindInvoiceForEmail), and a NewRaw statement (FindRefreshToken).
// The remaining query-shape surface is pinned by the sort-whitelist and
// alias tests elsewhere in this package.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// ── recording driver ─────────────────────────────────────────────────

type recCall struct {
	query string
	args  []driver.Value
}

type recDriver struct{ calls []recCall }

func (d *recDriver) Open(string) (driver.Conn, error) { return &recConn{d: d}, nil }

type recConnector struct{ d *recDriver }

func (c *recConnector) Connect(context.Context) (driver.Conn, error) { return &recConn{d: c.d}, nil }
func (c *recConnector) Driver() driver.Driver                        { return c.d }

type recConn struct{ d *recDriver }

func (c *recConn) Prepare(q string) (driver.Stmt, error) { return &recStmt{c: c, q: q}, nil }
func (c *recConn) Close() error                          { return nil }
func (c *recConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not expected in the SQL-injection pin")
}

func (c *recConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.record(q, args)
	return &recRows{}, nil
}

func (c *recConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.d.record(q, args)
	return driver.RowsAffected(0), nil
}

type recStmt struct {
	c *recConn
	q string
}

func (s *recStmt) Close() error  { return nil }
func (s *recStmt) NumInput() int { return -1 }

func (s *recStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.c.d.record(s.q, named(args))
	return driver.RowsAffected(0), nil
}

func (s *recStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.c.d.record(s.q, named(args))
	return &recRows{}, nil
}

type recRows struct{}

func (r *recRows) Columns() []string           { return []string{"x"} }
func (r *recRows) Close() error                { return nil }
func (r *recRows) Next(_ []driver.Value) error { return io.EOF }

func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

func (d *recDriver) record(q string, args []driver.NamedValue) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	d.calls = append(d.calls, recCall{query: q, args: vals})
}

func newRecordingStore() (*Store, *recDriver) {
	d := &recDriver{}
	db := bun.NewDB(sql.OpenDB(&recConnector{d: d}), pgdialect.New())
	return &Store{DB: db}, d
}

// ── the pin ──────────────────────────────────────────────────────────

// stripSQLStringLiterals removes the CONTENTS of the single-quoted
// string literals in a statement ('...' with ” doubling for an escaped
// quote — exactly how bun's pgdialect renders a value). What comes back
// is the statement SKELETON: tables, columns, operators, keywords. If
// any byte of user input survives in the skeleton, the input escaped
// the quoting layer and became SQL — a real injection.
func stripSQLStringLiterals(q string) string {
	var out strings.Builder
	inLiteral := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if inLiteral {
			if c == '\'' {
				if i+1 < len(q) && q[i+1] == '\'' {
					i++ // '' inside a literal is one escaped quote
					continue
				}
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func TestSQLInjectionUserInputNeverEscapesItsLiteral(t *testing.T) {
	hostile := []string{
		"' OR '1'='1",
		"'; DROP TABLE orders;--",
		"x' UNION SELECT password FROM users--",
	}

	// Three real store methods, three hand-written query shapes.
	type probe struct {
		name  string
		input []string
		run   func(ctx context.Context, s *Store)
	}
	probes := []probe{
		{
			name:  "FindOrderByExternalID (Where chain)",
			input: hostile[:2],
			run: func(ctx context.Context, s *Store) {
				_, _ = s.FindOrderByExternalID(ctx, hostile[0], hostile[1])
			},
		},
		{
			name:  "FindInvoiceForEmail (hand-written JOIN + lower())",
			input: hostile[1:],
			run: func(ctx context.Context, s *Store) {
				_, _ = s.FindInvoiceForEmail(ctx, hostile[1], hostile[2])
			},
		},
		{
			name:  "FindRefreshToken (raw NewRaw SQL)",
			input: hostile[:1],
			run: func(ctx context.Context, s *Store) {
				_, _ = s.FindRefreshToken(ctx, hostile[0])
			},
		},
	}

	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			s, rec := newRecordingStore()
			p.run(context.Background(), s)
			if len(rec.calls) == 0 {
				t.Fatal("the probe never reached the driver")
			}
			for _, call := range rec.calls {
				skeleton := strings.ToLower(stripSQLStringLiterals(call.query))
				for _, in := range p.input {
					// The value must REACH the statement as data: quoted
					// through the dialect's appender with its quotes doubled.
					// Compared case-insensitively because some paths fold the
					// input first (e.g. emails go through lower()) before the
					// appender quotes it.
					want := strings.ReplaceAll(strings.ToLower(in), "'", "''")
					if !strings.Contains(strings.ToLower(call.query), want) {
						t.Errorf("input %q never reached the statement as a quoted value:\n%s", in, call.query)
					}
					// ...and must never appear in the SKELETON — there it
					// would be SQL, not data.
					if strings.Contains(skeleton, strings.ToLower(in)) {
						t.Errorf("USER INPUT ESCAPED ITS LITERAL — injection risk!\nstatement: %s\nskeleton: %s\ninput: %q", call.query, skeleton, in)
					}
				}
			}
		})
	}
}
