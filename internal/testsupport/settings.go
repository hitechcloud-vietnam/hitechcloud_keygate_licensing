// Package testsupport holds helpers the test suites of several
// packages need from each other. It is imported only by tests.
package testsupport

import (
	"context"
	"testing"

	"github.com/uptrace/bun"
)

// settingsLockKey is the advisory lock every test that writes a row of
// the settings table takes. Any number would do; it only has to be the
// same one everywhere.
const settingsLockKey = 918273645

// LockSettings serialises tests that write the global settings table,
// and releases the lock when the test ends.
//
// `go test ./...` runs packages in parallel and they all point at one
// database, so three packages flipping maintenance_features_enabled
// were stepping on each other: a test would set the switch on, and
// another package would set it off halfway through, failing the first
// one on a switch it never touched. Nothing in the product is wrong
// there — the setting is global by design — so the serialisation
// belongs here rather than in a per-test workaround.
//
// The lock is taken on a connection of its own: an advisory lock lives
// on a session, and a pooled query may land on any connection.
func LockSettings(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("settings lock: %v", err)
	}
	if _, err := conn.NewRaw("SELECT pg_advisory_lock(?)", settingsLockKey).Exec(ctx); err != nil {
		_ = conn.Close()
		t.Fatalf("settings lock: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.NewRaw("SELECT pg_advisory_unlock(?)", settingsLockKey).Exec(ctx); err != nil {
			t.Logf("settings unlock: %v", err)
		}
		_ = conn.Close()
	})
}
