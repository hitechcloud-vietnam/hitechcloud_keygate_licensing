package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

var validKeyRe = regexp.MustCompile(`^[a-z0-9_.]+$`)
var validEnvRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// TestCatalogEntriesComplete is the completeness contract every new
// catalog entry must satisfy: a typed key, an env fallback with a
// real name, operator-facing description, and a default that parses
// as its declared type.
func TestCatalogEntriesComplete(t *testing.T) {
	validTypes := map[string]bool{
		config.TypeString: true, config.TypeInt: true, config.TypeBool: true,
		config.TypeDuration: true, config.TypeSecret: true,
	}
	validCategories := map[string]bool{}
	for _, c := range config.CategoryOrder() {
		validCategories[c] = true
	}

	for _, e := range config.Catalog() {
		if !validKeyRe.MatchString(e.Key) {
			t.Errorf("key %q must match %s", e.Key, validKeyRe)
		}
		if !validTypes[e.Type] {
			t.Errorf("%s: unknown type %q", e.Key, e.Type)
		}
		if !validCategories[e.Category] {
			t.Errorf("%s: unknown category %q", e.Key, e.Category)
		}
		if e.EnvVar == "" || !validEnvRe.MatchString(e.EnvVar) {
			t.Errorf("%s: EnvVar %q must be a non-empty SCREAMING_SNAKE name", e.Key, e.EnvVar)
		}
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("%s: description is required", e.Key)
		}
		// The default must parse as its type: it is what readers fall
		// back to and what the migration seeds.
		switch e.Type {
		case config.TypeInt:
			if _, err := strconv.Atoi(e.Default); err != nil {
				t.Errorf("%s: default %q is not an int", e.Key, e.Default)
			}
		case config.TypeBool:
			if _, err := strconv.ParseBool(e.Default); err != nil {
				t.Errorf("%s: default %q is not a bool", e.Key, e.Default)
			}
		case config.TypeDuration:
			if _, err := time.ParseDuration(e.Default); err != nil {
				t.Errorf("%s: default %q is not a duration", e.Key, e.Default)
			}
		case config.TypeSecret:
			if e.Default != "" {
				t.Errorf("%s: secret defaults must be empty, got %q", e.Key, e.Default)
			}
		}
	}
}

// TestCatalogNoDuplicateKeys pins one row per key: the settings table
// keys on it, and two entries would fight over one value.
func TestCatalogNoDuplicateKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range config.Catalog() {
		if seen[e.Key] {
			t.Errorf("duplicate catalog key %q", e.Key)
		}
		seen[e.Key] = true
	}
}

// TestCatalogCategoriesCovered pins that every listed category has
// entries — the admin UI renders one section per category and an empty
// one would look like a bug to the operator.
func TestCatalogCategoriesCovered(t *testing.T) {
	for _, cat := range config.CategoryOrder() {
		if len(config.EntriesInCategory(cat)) == 0 {
			t.Errorf("category %q has no entries", cat)
		}
		if config.CategoryLabel(cat) == cat {
			t.Errorf("category %q has no display label", cat)
		}
	}
}

// TestCatalogSecretKeysRegisteredInStore pins the cross-package
// contract: every catalog key of type "secret" must be registered in
// the settings layer's secret map, or SetSetting would store the
// credential in the clear (store seals exactly
// store.IsSecretSettingKey keys).
func TestCatalogSecretKeysRegisteredInStore(t *testing.T) {
	n := 0
	for _, e := range config.Catalog() {
		if e.Type != config.TypeSecret {
			continue
		}
		n++
		if !store.IsSecretSettingKey(e.Key) {
			t.Errorf("secret key %q is NOT registered in store.settingSecretKeys — it would be stored in plaintext", e.Key)
		}
	}
	if n == 0 {
		t.Fatal("catalog declares no secrets — the registration pin would be vacuous")
	}
}

// TestCatalogKeepsBootstrapEnvVarsOut pins the split: bootstrap
// variables stay in the environment and must never be catalogued, or
// config-in-DB would pretend to own key material the server needs
// before the database exists.
func TestCatalogKeepsBootstrapEnvVarsOut(t *testing.T) {
	byEnv := map[string]bool{}
	for _, e := range config.Catalog() {
		byEnv[e.EnvVar] = true
	}
	for _, b := range config.BootstrapEnvVars {
		if byEnv[b] {
			t.Errorf("bootstrap env var %s must not appear in the catalog", b)
		}
		if !config.IsBootstrapEnvVar(b) {
			t.Errorf("IsBootstrapEnvVar(%s) = false", b)
		}
	}
	if config.IsBootstrapEnvVar("SMTP_HOST") {
		t.Error("SMTP_HOST is not a bootstrap var")
	}
}

// TestDeprecatedEnvVars reports exactly the env-supplied non-bootstrap
// vars — that is the boot deprecation log's input.
func TestDeprecatedEnvVars(t *testing.T) {
	t.Setenv("PAY2S_PARTNER_CODE", "PC1")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("DATABASE_URL", "postgres://boot")
	got := config.DeprecatedEnvVars()
	want := map[string]bool{"PAY2S_PARTNER_CODE": true}
	for _, v := range got {
		if v == "SMTP_HOST" {
			t.Error("empty env values must not count as supplied")
		}
		if v == "DATABASE_URL" {
			t.Error("bootstrap vars must never be reported as deprecated")
		}
		if want[v] {
			delete(want, v)
		}
	}
	if len(want) != 0 {
		t.Errorf("DeprecatedEnvVars missed %v", want)
	}
}

// TestDeriveStripeLivemode pins the legacy rule: operators must opt
// INTO live mode; anything that does not start sk_live_/rk_live_ is
// test.
func TestDeriveStripeLivemode(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"sk_live_abc", true},
		{"rk_live_abc", true},
		{"sk_test_abc", false},
		{"rk_test_abc", false},
		{"", false},
		{"sk_live", false}, // no underscore boundary
	} {
		if got := config.DeriveStripeLivemode(tc.key); got != tc.want {
			t.Errorf("DeriveStripeLivemode(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// ─── Migration ↔ catalog drift pins ───
//
// The SQL files carry the catalog as literal rows. If the catalog and
// the migrations can drift, a fresh install and an upgraded one would
// disagree about what the defaults even are — so the pairs in both
// files are parsed and compared against the catalog here.

var sqlPairRe = regexp.MustCompile(`\('([^']*)',\s*'([^']*)'\)`)

func migrationSeeded(t *testing.T, name string) map[string]string {
	t.Helper()
	path := filepath.Join("..", "..", "db", "migrations", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	out := map[string]string{}
	for _, m := range sqlPairRe.FindAllStringSubmatch(string(raw), -1) {
		if _, dup := out[m[1]]; dup {
			t.Fatalf("%s: duplicate pair for %q", name, m[1])
		}
		out[m[1]] = m[2]
	}
	return out
}

// TestConfigCatalogMigrationsMatchCatalog pins:
//   - the up migration seeds EXACTLY the catalog keys, each with the
//     catalog default (secrets seed EMPTY — never a real value);
//   - the down migration names exactly the same (key, value) pairs, so
//     it removes the seeded rows and nothing else.
func TestConfigCatalogMigrationsMatchCatalog(t *testing.T) {
	up := migrationSeeded(t, "20261008_148000_config_catalog.up.sql")
	down := migrationSeeded(t, "20261008_148000_config_catalog.down.sql")

	for _, e := range config.Catalog() {
		want := e.Default
		if e.Type == config.TypeSecret {
			want = ""
		}
		got, ok := up[e.Key]
		if !ok {
			t.Errorf("up migration does not seed catalog key %q", e.Key)
			continue
		}
		if got != want {
			t.Errorf("up migration seeds %q = %q, want %q", e.Key, got, want)
		}
		if dgot, ok := down[e.Key]; !ok {
			t.Errorf("down migration does not remove catalog key %q", e.Key)
		} else if dgot != got {
			t.Errorf("down migration pair for %q = %q, up seeded %q", e.Key, dgot, got)
		}
	}
	for key := range up {
		if _, ok := config.FindEntry(key); !ok {
			t.Errorf("up migration seeds %q which is not in the catalog", key)
		}
	}
	for key := range down {
		if _, ok := config.FindEntry(key); !ok {
			t.Errorf("down migration removes %q which is not in the catalog", key)
		}
	}
}
