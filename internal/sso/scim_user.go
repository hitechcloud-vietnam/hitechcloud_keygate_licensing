package sso

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ─── SCIM 2.0 /Users protocol (Phase 8, slice 2) ───
//
// The wire representation and pure logic of the SCIM 2.0 /Users resource
// (RFC 7643 schema + RFC 7644 protocol): the User/List/Error JSON shapes,
// the `userName eq "…"` filter subset, startIndex/count pagination, and
// PATCH op application.
//
// This is pure protocol logic — std-lib only, no store/handler/session.
// The HTTP endpoints (internal/handler/scim.go) map these onto platform
// users and scim_identities rows; the bearer-token middleware
// (internal/middleware/scim_auth.go) guards them.
//
// # ERROR SHAPE — SCIM, not pkg/response
//
// This endpoint family speaks the SCIM error schema
// (`urn:ietf:params:scim:api:messages:2.0:Error`), NOT the platform's
// `pkg/response` envelope. That is not an oversight: a SCIM client (Okta,
// Entra ID, …) parses errors by the SCIM schema and status string; handing
// it `{"success":false,"error":{…}}` would be an unparseable reply to the
// provisioning client and break the sync. The two envelopes therefore stay
// separate BY DESIGN — see SCIMError.

// SCIM schema URIs.
const (
	SCIMUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	SCIMListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SCIMErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	SCIMPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// SCIMResourceTypeUser is meta.resourceType for a User.
const SCIMResourceTypeUser = "User"

// SCIMUser is the SCIM 2.0 User resource. userName is the platform user's
// email; name.givenName/familyName are derived from the display name (see
// SplitName/JoinName); active mirrors scim_identities.active.
type SCIMUser struct {
	Schemas    []string  `json:"schemas"`
	ID         string    `json:"id"`
	ExternalID string    `json:"externalId,omitempty"`
	UserName   string    `json:"userName"`
	Name       *SCIMName `json:"name,omitempty"`
	Active     bool      `json:"active"`
	Meta       *SCIMMeta `json:"meta,omitempty"`
}

// SCIMName is the SCIM name sub-attribute.
type SCIMName struct {
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// SCIMMeta is the SCIM meta attribute. Timestamps are RFC 3339.
type SCIMMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
}

// SCIMListResponse is the SCIM ListResponse envelope for /Users.
type SCIMListResponse struct {
	Schemas      []string    `json:"schemas"`
	TotalResults int         `json:"totalResults"`
	StartIndex   int         `json:"startIndex"`
	ItemsPerPage int         `json:"itemsPerPage"`
	Resources    []*SCIMUser `json:"Resources"`
}

// SCIMError is the SCIM error body. Status is a STRING per RFC 7644 (e.g.
// "400"), not a number.
type SCIMError struct {
	Schemas []string `json:"schemas"`
	Detail  string   `json:"detail"`
	Status  string   `json:"status"`
}

// NewSCIMError builds a SCIM error envelope.
func NewSCIMError(status int, detail string) *SCIMError {
	return &SCIMError{
		Schemas: []string{SCIMErrorSchema},
		Detail:  detail,
		Status:  strconv.Itoa(status),
	}
}

// SCIMPatch is a SCIM PATCH request (RFC 7644 §3.5.2).
type SCIMPatch struct {
	Schemas    []string      `json:"schemas"`
	Operations []SCIMPatchOp `json:"Operations"`
}

// SCIMPatchOp is one PATCH operation. Value is kept raw so it can be
// interpreted per the target path (bool for active, string for userName,
// an object for name).
type SCIMPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// SCIMUserResource assembles a User resource from its parts. location is
// the resource URL (meta.location). givenName/familyName are stored as
// provided (the caller derives them via SplitName).
func SCIMUserResource(id, externalID, userName, givenName, familyName string, active bool, created, updated time.Time, location string) *SCIMUser {
	u := &SCIMUser{
		Schemas:    []string{SCIMUserSchema},
		ID:         id,
		ExternalID: externalID,
		UserName:   userName,
		Active:     active,
		Meta: &SCIMMeta{
			ResourceType: SCIMResourceTypeUser,
			Location:     location,
		},
	}
	if givenName != "" || familyName != "" {
		u.Name = &SCIMName{GivenName: givenName, FamilyName: familyName}
	}
	if !created.IsZero() {
		u.Meta.Created = created.UTC().Format(time.RFC3339)
	}
	if !updated.IsZero() {
		u.Meta.LastModified = updated.UTC().Format(time.RFC3339)
	}
	return u
}

// NewSCIMList wraps resources in a ListResponse.
func NewSCIMList(resources []*SCIMUser, total, startIndex, count int) *SCIMListResponse {
	if resources == nil {
		resources = []*SCIMUser{}
	}
	return &SCIMListResponse{
		Schemas:      []string{SCIMListSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	}
}

// ─── name.givenName / name.familyName round-trip ───
//
// The platform stores a single display name (users.name). SCIM exposes the
// structured givenName/familyName pair. The mapping is:
//
//	read  (users.name → SCIM):  split at the LAST space — givenName is
//	                            everything before it, familyName after.
//	write (SCIM → users.name):  join with a single space.
//
// Splitting at the LAST space makes the ordinary two-part name round-trip
// exactly ("Alice Smith" ↔ given "Alice" family "Smith") and keeps the
// leading words of a longer name together ("Alice van der Berg" ↔ given
// "Alice van der" family "Berg"). A single-word name yields givenName =
// the name and familyName = "". Irregular internal whitespace is
// normalised on the write. The split/join pair is an exact round-trip for
// any name, which is the property the mapping actually depends on. This is
// a display-name convenience, not a claim about the linguistic structure of
// a person's name.

// SplitName splits a display name into (givenName, familyName) at the last
// space. A single-word (or empty) name yields familyName "".
func SplitName(display string) (given, family string) {
	s := strings.TrimSpace(display)
	i := strings.LastIndex(s, " ")
	if i < 0 {
		return s, ""
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
}

// JoinName composes a display name from givenName + familyName with single
// spaces.
func JoinName(given, family string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(given)+" "+strings.TrimSpace(family)), " ")
}

// ─── filter subset ───

// scimFilterRe matches the supported filter shape: attr eq "value".
var scimFilterRe = regexp.MustCompile(`(?i)^\s*([A-Za-z][A-Za-z0-9:_-]*)\s+eq\s+"([^"]*)"\s*$`)

// ParseSCIMUserNameFilter parses the supported SCIM filter subset. An empty
// filter means "no filtering". `userName eq "…"` returns the value. Any
// other filter (different attribute, different operator, unquoted value) is
// an error — this endpoint implements only the userName equality filter and
// refuses rather than silently returning the wrong set.
func ParseSCIMUserNameFilter(filter string) (string, error) {
	f := strings.TrimSpace(filter)
	if f == "" {
		return "", nil
	}
	m := scimFilterRe.FindStringSubmatch(f)
	if m == nil {
		return "", fmt.Errorf("scim: unsupported filter %q (only userName eq \"…\")", filter)
	}
	if strings.EqualFold(m[1], "userName") {
		return m[2], nil
	}
	return "", fmt.Errorf("scim: unsupported filter attribute %q", m[1])
}

// ─── pagination ───

// SCIMPage is the parsed startIndex/count window. StartIndex is the 1-based
// index the client asked for; Offset is the 0-based equivalent for the
// store. Count is the page size.
type SCIMPage struct {
	StartIndex int
	Count      int
	Offset     int
}

// DefaultSCIMCount is the page size when the client does not ask.
const DefaultSCIMCount = 100

// MaxSCIMCount caps a client-requested page so one call cannot ask for the
// whole directory.
const MaxSCIMCount = 200

// ParseSCIMPage reads startIndex/count query values. startIndex is 1-based
// (default 1). count defaults to DefaultSCIMCount and is clamped to
// MaxSCIMCount; a count of 0 or less falls back to the default (a request
// for "no rows" is treated as "the default page" rather than a silent empty
// page).
func ParseSCIMPage(startIndexQ, countQ string) SCIMPage {
	startIndex := 1
	if v, err := strconv.Atoi(strings.TrimSpace(startIndexQ)); err == nil && v >= 1 {
		startIndex = v
	}
	count := DefaultSCIMCount
	if v, err := strconv.Atoi(strings.TrimSpace(countQ)); err == nil && v > 0 {
		count = v
	}
	if count > MaxSCIMCount {
		count = MaxSCIMCount
	}
	return SCIMPage{StartIndex: startIndex, Count: count, Offset: startIndex - 1}
}

// ─── PATCH application ───

// ApplySCIMPatch applies a SCIM PATCH to a User draft, mutating u. It
// supports the operations the directory syncs actually send:
//
//	replace/add  active              (bool)
//	replace/add  userName            (string — the email)
//	replace/add  name                (object {givenName, familyName})
//	replace/add  name.givenName      (string)
//	replace/add  name.familyName     (string)
//
// and, when Path is empty, a bare value object whose keys are any of the
// above (the shape Okta sends: {"op":"replace","value":{"active":false}}).
//
// `remove` and any other path are refused with an error — a client that
// wants to clear a field sends replace with an empty value, and silently
// ignoring an operation we do not understand would leave the client
// believing a change was applied that was not.
func ApplySCIMPatch(u *SCIMUser, p SCIMPatch) error {
	if u == nil {
		return fmt.Errorf("scim: nil user")
	}
	for i, op := range p.Operations {
		switch strings.ToLower(strings.TrimSpace(op.Op)) {
		case "replace", "add":
			if err := applyPatchValue(u, op); err != nil {
				return fmt.Errorf("scim: op %d: %w", i, err)
			}
		case "remove":
			if err := applyPatchRemove(u, op); err != nil {
				return fmt.Errorf("scim: op %d: %w", i, err)
			}
		default:
			return fmt.Errorf("scim: op %d: unsupported op %q", i, op.Op)
		}
	}
	return nil
}

// applyPatchValue handles a replace/add op.
func applyPatchValue(u *SCIMUser, op SCIMPatchOp) error {
	path := normalizePatchPath(op.Path)
	if path == "" {
		// No path: the value is an object of attribute replacements.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &obj); err != nil {
			return fmt.Errorf("value must be an object when path is omitted: %w", err)
		}
		for k, v := range obj {
			if err := setPatchAttr(u, normalizePatchPath(k), v); err != nil {
				return err
			}
		}
		return nil
	}
	return setPatchAttr(u, path, op.Value)
}

// setPatchAttr sets one attribute from raw JSON.
func setPatchAttr(u *SCIMUser, path string, raw json.RawMessage) error {
	switch path {
	case "active":
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("active must be a boolean: %w", err)
		}
		u.Active = b
		return nil
	case "username":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("userName must be a string: %w", err)
		}
		u.UserName = strings.TrimSpace(s)
		return nil
	case "name":
		var n SCIMName
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("name must be an object: %w", err)
		}
		if u.Name == nil {
			u.Name = &SCIMName{}
		}
		u.Name.GivenName = n.GivenName
		u.Name.FamilyName = n.FamilyName
		return nil
	case "name.givenname":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("name.givenName must be a string: %w", err)
		}
		if u.Name == nil {
			u.Name = &SCIMName{}
		}
		u.Name.GivenName = strings.TrimSpace(s)
		return nil
	case "name.familyname":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("name.familyName must be a string: %w", err)
		}
		if u.Name == nil {
			u.Name = &SCIMName{}
		}
		u.Name.FamilyName = strings.TrimSpace(s)
		return nil
	default:
		return fmt.Errorf("unsupported path %q", path)
	}
}

// applyPatchRemove clears an attribute. Only name sub-attributes can be
// removed (set to empty); removing userName or active is refused.
func applyPatchRemove(u *SCIMUser, op SCIMPatchOp) error {
	switch normalizePatchPath(op.Path) {
	case "name.givenname":
		if u.Name != nil {
			u.Name.GivenName = ""
		}
		return nil
	case "name.familyname":
		if u.Name != nil {
			u.Name.FamilyName = ""
		}
		return nil
	case "name":
		u.Name = nil
		return nil
	default:
		return fmt.Errorf("unsupported remove path %q", op.Path)
	}
}

// normalizePatchPath folds a SCIM path to a canonical lowercase key and
// strips a leading attribute root. "userName" → "username", "name.familyName"
// → "name.familyname", "urn:…:User:userName" → "username".
func normalizePatchPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Drop an optional schema URN prefix ("urn:…:User:userName").
	if i := strings.LastIndex(p, ":"); i >= 0 && strings.Contains(p[:i], ":") {
		p = p[i+1:]
	}
	p = strings.TrimPrefix(p, ".")
	return strings.ToLower(p)
}
