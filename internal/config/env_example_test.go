package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The .env.example file is the operator's complete environment
// reference. These tests pin it to the catalog so a new catalog entry
// can never ship without documenting its env fallback — and so a typo
// in .env.example cannot introduce a variable nothing reads.

// envExamplePath is the repo-root .env.example relative to this package.
const envExamplePath = "../../.env.example"

// envExampleLine matches an assignment line, commented or not:
// "# NAME=value" and "NAME=value" both count as documented.
var envExampleLine = regexp.MustCompile(`(?m)^\s*#?\s*([A-Z][A-Z0-9_]*)=`)

// envExampleActiveLine matches uncommented assignments only.
var envExampleActiveLine = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)=`)

func loadEnvExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(envExamplePath)
	if err != nil {
		t.Fatalf("read %s: %v", envExamplePath, err)
	}
	return string(b)
}

// legacyEnvVars are variables documented in .env.example that no
// catalog entry or bootstrap entry claims. Keep this list tiny and
// justified: every entry is a compatibility shim.
var legacyEnvVars = map[string]string{
	// Float-fraction quota threshold, superseded by the bps form of
	// app.quota_warning_threshold. Read by config.Load only.
	"QUOTA_WARNING_THRESHOLD": "legacy float form of app.quota_warning_threshold",
}

// TestEnvExampleDocumentsEveryCatalogEnvVar fails when a catalog entry
// or bootstrap variable is missing from .env.example.
func TestEnvExampleDocumentsEveryCatalogEnvVar(t *testing.T) {
	body := loadEnvExample(t)
	documented := map[string]bool{}
	for _, m := range envExampleLine.FindAllStringSubmatch(body, -1) {
		documented[m[1]] = true
	}

	for _, e := range Catalog() {
		if e.EnvVar == "" {
			continue
		}
		if !documented[e.EnvVar] {
			t.Errorf("catalog key %s: env fallback %s is not documented in .env.example", e.Key, e.EnvVar)
		}
	}
	for _, b := range BootstrapEnvVars {
		if !documented[b] {
			t.Errorf("bootstrap env var %s is not documented in .env.example", b)
		}
	}
}

// TestEnvExampleHasNoUnknownVariables fails when .env.example
// documents a variable nothing reads.
func TestEnvExampleHasNoUnknownVariables(t *testing.T) {
	body := loadEnvExample(t)
	known := map[string]bool{}
	for _, e := range Catalog() {
		if e.EnvVar != "" {
			known[e.EnvVar] = true
		}
	}
	for _, b := range BootstrapEnvVars {
		known[b] = true
	}
	for v := range legacyEnvVars {
		known[v] = true
	}

	seen := map[string]bool{}
	for _, m := range envExampleActiveLine.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if seen[name] {
			continue // first wins; duplicates are not this test's problem
		}
		seen[name] = true
		if !known[name] {
			t.Errorf(".env.example sets %s= but no catalog entry, bootstrap entry or legacy shim reads it", name)
		}
	}
}

// TestLegacyEnvVarsAreJustified keeps the shim list honest.
func TestLegacyEnvVarsAreJustified(t *testing.T) {
	for name, why := range legacyEnvVars {
		if strings.TrimSpace(why) == "" {
			t.Errorf("legacy env var %s has no justification", name)
		}
	}
}
