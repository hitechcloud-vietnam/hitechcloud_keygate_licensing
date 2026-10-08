package sso

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ─── userName eq "…" filter ───

func TestParseSCIMUserNameFilter(t *testing.T) {
	cases := []struct {
		name    string
		filter  string
		want    string
		wantErr bool
	}{
		{"empty is no filter", "", "", false},
		{"plain eq", `userName eq "alice@acme.com"`, "alice@acme.com", false},
		{"surrounding space", `  userName eq "a@b.co"  `, "a@b.co", false},
		{"case-insensitive attr", `USERNAME eq "a@b.co"`, "a@b.co", false},
		{"case-insensitive op", `userName EQ "a@b.co"`, "a@b.co", false},
		{"other attribute", `externalId eq "x"`, "", true},
		{"unsupported op", `userName sw "a"`, "", true},
		{"unquoted value", `userName eq a@b.co`, "", true},
		{"unsupported complex", `userName eq "a" and active eq "true"`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSCIMUserNameFilter(tc.filter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseSCIMUserNameFilter(%q) err = %v, wantErr %v", tc.filter, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ParseSCIMUserNameFilter(%q) = %q, want %q", tc.filter, got, tc.want)
			}
		})
	}
}

// ─── pagination ───

func TestParseSCIMPage(t *testing.T) {
	cases := []struct {
		name                          string
		start, count                  string
		wantStart, wantCount, wantOff int
	}{
		{"defaults", "", "", 1, DefaultSCIMCount, 0},
		{"start 5", "5", "", 5, DefaultSCIMCount, 4},
		{"count 10", "", "10", 1, 10, 0},
		{"count clamped to max", "", "9999", 1, MaxSCIMCount, 0},
		{"count 0 falls back", "", "0", 1, DefaultSCIMCount, 0},
		{"negative count falls back", "", "-3", 1, DefaultSCIMCount, 0},
		{"bad start defaults", "abc", "20", 1, 20, 0},
		{"start 0 defaults to 1", "0", "", 1, DefaultSCIMCount, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseSCIMPage(tc.start, tc.count)
			if p.StartIndex != tc.wantStart || p.Count != tc.wantCount || p.Offset != tc.wantOff {
				t.Fatalf("ParseSCIMPage(%q,%q) = %+v, want start=%d count=%d off=%d",
					tc.start, tc.count, p, tc.wantStart, tc.wantCount, tc.wantOff)
			}
		})
	}
}

// ─── name round-trip ───

func TestSplitName(t *testing.T) {
	cases := []struct {
		in            string
		given, family string
	}{
		{"Alice Smith", "Alice", "Smith"},
		{"Alice van der Berg", "Alice van der", "Berg"},
		{"Alice", "Alice", ""},
		{"", "", ""},
		{"  Alice   Smith  ", "Alice", "Smith"},
	}
	for _, tc := range cases {
		given, family := SplitName(tc.in)
		if given != tc.given || family != tc.family {
			t.Fatalf("SplitName(%q) = (%q,%q), want (%q,%q)", tc.in, given, family, tc.given, tc.family)
		}
	}
}

func TestJoinName(t *testing.T) {
	cases := []struct {
		given, family, want string
	}{
		{"Alice", "Smith", "Alice Smith"},
		{"Alice", "", "Alice"},
		{"", "Smith", "Smith"},
		{"  Alice ", "  Smith  ", "Alice Smith"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := JoinName(tc.given, tc.family); got != tc.want {
			t.Fatalf("JoinName(%q,%q) = %q, want %q", tc.given, tc.family, got, tc.want)
		}
	}
}

func TestSplitJoinRoundTrip(t *testing.T) {
	for _, name := range []string{"Alice Smith", "Alice van der Berg", "Alice"} {
		given, family := SplitName(name)
		if got := JoinName(given, family); got != name {
			t.Fatalf("round trip %q -> (%q,%q) -> %q", name, given, family, got)
		}
	}
}

// ─── resource / list / error shapes ───

func TestSCIMUserResource(t *testing.T) {
	created := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	u := SCIMUserResource("scim-1", "ext-1", "alice@acme.com", "Alice", "Smith",
		true, created, updated, "https://x/scim/v2/Users/scim-1")

	if u.ID != "scim-1" || u.ExternalID != "ext-1" || u.UserName != "alice@acme.com" {
		t.Fatalf("identity fields wrong: %+v", u)
	}
	if u.Name == nil || u.Name.GivenName != "Alice" || u.Name.FamilyName != "Smith" {
		t.Fatalf("name wrong: %+v", u.Name)
	}
	if !u.Active {
		t.Fatal("want active")
	}
	if u.Meta == nil || u.Meta.ResourceType != SCIMResourceTypeUser {
		t.Fatalf("meta.resourceType wrong: %+v", u.Meta)
	}
	if u.Meta.Created != created.Format(time.RFC3339) || u.Meta.LastModified != updated.Format(time.RFC3339) {
		t.Fatalf("meta timestamps wrong: %+v", u.Meta)
	}
	if u.Meta.Location != "https://x/scim/v2/Users/scim-1" {
		t.Fatalf("meta.location wrong: %q", u.Meta.Location)
	}
}

func TestSCIMUserResourceOmitsEmptyName(t *testing.T) {
	u := SCIMUserResource("i", "", "a@b.co", "", "", true, time.Time{}, time.Time{}, "loc")
	if u.Name != nil {
		t.Fatalf("expected no name, got %+v", u.Name)
	}
}

func TestNewSCIMListNilResources(t *testing.T) {
	l := NewSCIMList(nil, 0, 1, 100)
	if l.Resources == nil || len(l.Resources) != 0 {
		t.Fatalf("Resources should be empty non-nil: %#v", l.Resources)
	}
	if l.TotalResults != 0 || l.StartIndex != 1 || l.ItemsPerPage != 0 {
		t.Fatalf("envelope wrong: %+v", l)
	}
	if !containsStr(l.Schemas, SCIMListSchema) {
		t.Fatalf("schemas wrong: %v", l.Schemas)
	}
}

func TestNewSCIMErrorStatusIsString(t *testing.T) {
	e := NewSCIMError(404, "user not found")
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	status, ok := raw["status"].(string)
	if !ok {
		t.Fatalf("status must serialize as a string per RFC 7644, got %T", raw["status"])
	}
	if status != "404" {
		t.Fatalf("status = %q, want \"404\"", status)
	}
	if !containsStr(e.Schemas, SCIMErrorSchema) {
		t.Fatalf("schemas wrong: %v", e.Schemas)
	}
}

// ─── PATCH application ───

func baseDraft() *SCIMUser {
	return SCIMUserResource("scim-1", "ext-1", "alice@acme.com", "Alice", "Smith",
		true, time.Time{}, time.Time{}, "loc")
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplySCIMPatchReplaceActive(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Schemas: []string{SCIMPatchSchema}, Operations: []SCIMPatchOp{
		{Op: "replace", Path: "active", Value: mustJSON(t, false)},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.Active {
		t.Fatal("active should be false")
	}
}

func TestApplySCIMPatchReplaceUserName(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "replace", Path: "userName", Value: mustJSON(t, "bob@acme.com")},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.UserName != "bob@acme.com" {
		t.Fatalf("userName = %q", u.UserName)
	}
}

func TestApplySCIMPatchReplaceNameObject(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "replace", Path: "name", Value: mustJSON(t, map[string]string{"givenName": "Bob", "familyName": "Jones"})},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.Name == nil || u.Name.GivenName != "Bob" || u.Name.FamilyName != "Jones" {
		t.Fatalf("name = %+v", u.Name)
	}
}

func TestApplySCIMPatchReplaceNameSubAttrs(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "replace", Path: "name.givenName", Value: mustJSON(t, "Bob")},
		{Op: "replace", Path: "name.familyName", Value: mustJSON(t, "Jones")},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.Name == nil || u.Name.GivenName != "Bob" || u.Name.FamilyName != "Jones" {
		t.Fatalf("name = %+v", u.Name)
	}
}

func TestApplySCIMPatchEmptyPathValueObject(t *testing.T) {
	// Okta style: {"op":"replace","value":{"active":false}}.
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "replace", Value: mustJSON(t, map[string]any{"active": false})},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.Active {
		t.Fatal("active should be false")
	}
}

func TestApplySCIMPatchRemoveNameSubAttr(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "remove", Path: "name.familyName"},
	}}
	if err := ApplySCIMPatch(u, p); err != nil {
		t.Fatal(err)
	}
	if u.Name == nil || u.Name.FamilyName != "" {
		t.Fatalf("familyName should be cleared: %+v", u.Name)
	}
}

func TestApplySCIMPatchUnsupportedOpRefused(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "insert", Path: "active", Value: mustJSON(t, true)},
	}}
	if err := ApplySCIMPatch(u, p); err == nil {
		t.Fatal("expected unsupported op to error")
	}
}

func TestApplySCIMPatchUnsupportedPathRefused(t *testing.T) {
	u := baseDraft()
	p := SCIMPatch{Operations: []SCIMPatchOp{
		{Op: "replace", Path: "password", Value: mustJSON(t, "x")},
	}}
	if err := ApplySCIMPatch(u, p); err == nil {
		t.Fatal("expected unsupported path to error")
	}
}

func TestApplySCIMPatchNilUser(t *testing.T) {
	if err := ApplySCIMPatch(nil, SCIMPatch{}); err == nil {
		t.Fatal("expected nil user to error")
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
