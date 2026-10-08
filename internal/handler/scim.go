package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/sso"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── SCIM 2.0 /Users provisioning (Phase 8, slice 2) ───
//
// The SCIM 2.0 user provisioning endpoints a directory (Okta, Entra ID,
// …) drives to create, update and deprovision platform users. They sit
// behind middleware.SCIMTokenAuth (bearer `htc_scim_…`).
//
//	GET    /scim/v2/Users           list (filter + pagination)
//	POST   /scim/v2/Users           create (provision)
//	GET    /scim/v2/Users/:id       read one
//	PUT    /scim/v2/Users/:id       replace
//	PATCH  /scim/v2/Users/:id       partial update
//	DELETE /scim/v2/Users/:id       deactivate (SCIM "delete" = deprovision)
//
// # MAPPING (see scim_identities migration)
//
// A SCIM User maps to a platform users row + one scim_identities row:
//
//	userName   → users.email   (the SCIM handle; UNIQUE)
//	name.*     → users.name    (SplitName/JoinName round-trip)
//	active     → scim_identities.active
//	externalId → scim_identities.external_id
//	id         → scim_identities.id (the SCIM resource id, NOT user_id)
//
// Provisioning finds-or-creates the platform user by email (linking an
// existing account rather than duplicating it) and adds a scim_identity.
// DELETE deactivates the identity — it does NOT drop the user row (its
// licences, orders and audit trail are preserved) because model.User has
// no disabled flag; the SSO login path refuses a deactivated identity, so
// deprovisioning blocks sign-in without destroying data.
//
// # ERROR SHAPE — SCIM, NOT pkg/response
//
// Every error here is the SCIM error schema (RFC 7644), never the
// platform's pkg/response envelope: a SCIM client parses errors by that
// schema and would choke on `{"success":false,…}`. See the note in
// internal/sso/scim_user.go. Route wiring is the Lead's.

// scimStore is the slice of store.Store this handler needs. The real
// constructor takes *store.Store; tests substitute a fake.
type scimStore interface {
	FindSCIMUserBySCIMID(ctx context.Context, scimID string) (*store.SCIMUserRow, error)
	FindSCIMUserByUserName(ctx context.Context, email string) (*store.SCIMUserRow, error)
	ListSCIMUsers(ctx context.Context, p store.Page) ([]*store.SCIMUserRow, int, error)
	FindSCIMIdentityByID(ctx context.Context, id string) (*store.SCIMIdentity, error)
	CreateSCIMIdentity(ctx context.Context, row *store.SCIMIdentity) error
	UpdateSCIMIdentity(ctx context.Context, row *store.SCIMIdentity) error
	SetSCIMIdentityActive(ctx context.Context, id string, active bool) error
	FindUserByEmail(ctx context.Context, email string) (*model.User, error)
	UpsertUser(ctx context.Context, u *model.User) error
	UpdateUserProfile(ctx context.Context, userID, name string) error
	UpdateSCIMUserEmail(ctx context.Context, userID, email string) error
}

var _ scimStore = (*store.Store)(nil)

// SCIMHandler wires the SCIM /Users endpoints to the store.
type SCIMHandler struct {
	store   scimStore
	baseURL string
}

// NewSCIMHandler builds the handler against the real store. baseURL is the
// install BaseURL (for meta.location).
func NewSCIMHandler(s *store.Store, baseURL string) *SCIMHandler {
	return &SCIMHandler{store: s, baseURL: strings.TrimRight(baseURL, "/")}
}

// scimJSON writes a SCIM-shaped body with the SCIM content type.
func scimJSON(c *gin.Context, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.Data(http.StatusInternalServerError, "application/scim+json",
			[]byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"detail":"internal error","status":"500"}`))
		return
	}
	c.Data(status, "application/scim+json", b)
}

// scimErr writes a SCIM error body.
func scimErr(c *gin.Context, status int, detail string) {
	scimJSON(c, status, sso.NewSCIMError(status, detail))
}

// location is the canonical resource URL for a SCIM user.
func (h *SCIMHandler) location(id string) string {
	return h.baseURL + "/scim/v2/Users/" + id
}

// toResource projects a joined store row onto the SCIM User wire shape.
func (h *SCIMHandler) toResource(row *store.SCIMUserRow) *sso.SCIMUser {
	given, family := sso.SplitName(row.Name)
	ext := ""
	if row.ExternalID != nil {
		ext = *row.ExternalID
	}
	return sso.SCIMUserResource(row.SCIMID, ext, row.Email, given, family,
		row.Active, row.CreatedAt, row.UpdatedAt, h.location(row.SCIMID))
}

// ─── GET /scim/v2/Users ───

// ListUsers answers a list, honouring the `userName eq "…"` filter and
// startIndex/count pagination. A userName filter that matches nothing is an
// EMPTY LIST (SCIM convention), not a 404.
func (h *SCIMHandler) ListUsers(c *gin.Context) {
	userName, err := sso.ParseSCIMUserNameFilter(c.Query("filter"))
	if err != nil {
		scimErr(c, http.StatusBadRequest, err.Error())
		return
	}
	page := sso.ParseSCIMPage(c.Query("startIndex"), c.Query("count"))

	if userName != "" {
		row, err := h.store.FindSCIMUserByUserName(c, userName)
		if err != nil {
			scimJSON(c, http.StatusOK, sso.NewSCIMList(nil, 0, page.StartIndex, page.Count))
			return
		}
		scimJSON(c, http.StatusOK,
			sso.NewSCIMList([]*sso.SCIMUser{h.toResource(row)}, 1, page.StartIndex, page.Count))
		return
	}

	rows, total, err := h.store.ListSCIMUsers(c, store.Page{Limit: page.Count, Offset: page.Offset})
	if err != nil {
		scimErr(c, http.StatusInternalServerError, "could not list users")
		return
	}
	resources := make([]*sso.SCIMUser, 0, len(rows))
	for _, r := range rows {
		resources = append(resources, h.toResource(r))
	}
	scimJSON(c, http.StatusOK, sso.NewSCIMList(resources, total, page.StartIndex, page.Count))
}

// ─── GET /scim/v2/Users/:id ───

func (h *SCIMHandler) GetUser(c *gin.Context) {
	row, err := h.store.FindSCIMUserBySCIMID(c, c.Param("id"))
	if err != nil {
		scimErr(c, http.StatusNotFound, "user not found")
		return
	}
	scimJSON(c, http.StatusOK, h.toResource(row))
}

// ─── POST /scim/v2/Users ───

// CreateUser provisions a user. It finds-or-creates the platform user by
// email (linking an existing account, never duplicating) and adds a
// scim_identity. A user that is already provisioned is 409 (uniqueness).
func (h *SCIMHandler) CreateUser(c *gin.Context) {
	raw, err := c.GetRawData()
	if err != nil {
		scimErr(c, http.StatusBadRequest, "could not read request body")
		return
	}
	var u sso.SCIMUser
	if err := json.Unmarshal(raw, &u); err != nil {
		scimErr(c, http.StatusBadRequest, "invalid request body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(u.UserName))
	if email == "" {
		scimErr(c, http.StatusBadRequest, "userName is required")
		return
	}
	name := nameOf(&u)
	active := effectiveActive(raw)
	extID := optStr(strings.TrimSpace(u.ExternalID))

	user, err := h.store.FindUserByEmail(c, email)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			scimErr(c, http.StatusInternalServerError, "could not look up user")
			return
		}
		nu := &model.User{Email: email, Name: name}
		if uerr := h.store.UpsertUser(c, nu); uerr != nil {
			scimErr(c, http.StatusInternalServerError, "could not create user")
			return
		}
		if user, err = h.store.FindUserByEmail(c, email); err != nil {
			scimErr(c, http.StatusInternalServerError, "could not read user")
			return
		}
	} else if name != "" && strings.TrimSpace(user.Name) == "" {
		// Fill a blank display name; never overwrite one the user set.
		_ = h.store.UpdateUserProfile(c, user.ID, name)
	}

	ident := &store.SCIMIdentity{ExternalID: extID, UserID: user.ID, Active: active}
	if err := h.store.CreateSCIMIdentity(c, ident); err != nil {
		if store.IsSCIMIdentityConflict(err) {
			scimErr(c, http.StatusConflict, "userName is already provisioned")
			return
		}
		scimErr(c, http.StatusInternalServerError, "could not create user")
		return
	}

	row, err := h.store.FindSCIMUserBySCIMID(c, ident.ID)
	if err != nil {
		scimErr(c, http.StatusInternalServerError, "could not read user")
		return
	}
	c.Header("Location", h.location(row.SCIMID))
	scimJSON(c, http.StatusCreated, h.toResource(row))
}

// ─── PUT /scim/v2/Users/:id ───

// ReplaceUser replaces the resource. userName is required; an omitted
// optional attribute is cleared/defaulted per SCIM replace semantics.
func (h *SCIMHandler) ReplaceUser(c *gin.Context) {
	row, err := h.store.FindSCIMUserBySCIMID(c, c.Param("id"))
	if err != nil {
		scimErr(c, http.StatusNotFound, "user not found")
		return
	}
	raw, err := c.GetRawData()
	if err != nil {
		scimErr(c, http.StatusBadRequest, "could not read request body")
		return
	}
	var u sso.SCIMUser
	if err := json.Unmarshal(raw, &u); err != nil {
		scimErr(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(u.UserName) == "" {
		scimErr(c, http.StatusBadRequest, "userName is required")
		return
	}
	u.Active = effectiveActive(raw)
	if !h.persistUser(c, row, &u) {
		return
	}
	out, err := h.store.FindSCIMUserBySCIMID(c, row.SCIMID)
	if err != nil {
		scimErr(c, http.StatusInternalServerError, "could not read user")
		return
	}
	scimJSON(c, http.StatusOK, h.toResource(out))
}

// ─── PATCH /scim/v2/Users/:id ───

// PatchUser applies a SCIM PATCH (replace on active/name, add/replace on
// userName, plus the name sub-attributes). See sso.ApplySCIMPatch for the
// supported op set.
func (h *SCIMHandler) PatchUser(c *gin.Context) {
	row, err := h.store.FindSCIMUserBySCIMID(c, c.Param("id"))
	if err != nil {
		scimErr(c, http.StatusNotFound, "user not found")
		return
	}
	raw, err := c.GetRawData()
	if err != nil {
		scimErr(c, http.StatusBadRequest, "could not read request body")
		return
	}
	var patch sso.SCIMPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		scimErr(c, http.StatusBadRequest, "invalid PATCH body")
		return
	}

	draft := h.toResource(row)
	if err := sso.ApplySCIMPatch(draft, patch); err != nil {
		scimErr(c, http.StatusBadRequest, err.Error())
		return
	}
	if !h.persistUser(c, row, draft) {
		return
	}
	out, err := h.store.FindSCIMUserBySCIMID(c, row.SCIMID)
	if err != nil {
		scimErr(c, http.StatusInternalServerError, "could not read user")
		return
	}
	scimJSON(c, http.StatusOK, h.toResource(out))
}

// ─── DELETE /scim/v2/Users/:id ───

// DeleteUser deactivates (SCIM "delete" = deprovision). It flips the
// identity's active flag off; the platform user row is preserved (it has no
// disabled flag — see the mapping note), and the SSO login path refuses a
// deactivated identity so the account can no longer sign in.
func (h *SCIMHandler) DeleteUser(c *gin.Context) {
	row, err := h.store.FindSCIMUserBySCIMID(c, c.Param("id"))
	if err != nil {
		scimErr(c, http.StatusNotFound, "user not found")
		return
	}
	if err := h.store.SetSCIMIdentityActive(c, row.SCIMID, false); err != nil {
		scimErr(c, http.StatusInternalServerError, "could not deactivate user")
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── shared persistence ───

// persistUser writes the draft's userName / name / active / externalId back
// to the store when they differ from the current row, answering SCIM errors
// itself and reporting success. It is shared by PUT and PATCH.
func (h *SCIMHandler) persistUser(c *gin.Context, row *store.SCIMUserRow, draft *sso.SCIMUser) bool {
	// userName → users.email.
	newEmail := strings.ToLower(strings.TrimSpace(draft.UserName))
	if newEmail == "" {
		scimErr(c, http.StatusBadRequest, "userName is required")
		return false
	}
	if newEmail != row.Email {
		if err := h.store.UpdateSCIMUserEmail(c, row.UserID, newEmail); err != nil {
			if store.IsSCIMEmailConflict(err) {
				scimErr(c, http.StatusConflict, "userName is already in use")
			} else {
				scimErr(c, http.StatusInternalServerError, "could not update userName")
			}
			return false
		}
	}

	// name → users.name.
	newName := nameOf(draft)
	if newName != row.Name {
		if err := h.store.UpdateUserProfile(c, row.UserID, newName); err != nil {
			scimErr(c, http.StatusInternalServerError, "could not update name")
			return false
		}
	}

	// active → scim_identities.active.
	if draft.Active != row.Active {
		if err := h.store.SetSCIMIdentityActive(c, row.SCIMID, draft.Active); err != nil {
			scimErr(c, http.StatusInternalServerError, "could not update active")
			return false
		}
	}

	// externalId → scim_identities.external_id.
	if strings.TrimSpace(draft.ExternalID) != derefStr(row.ExternalID) {
		ident, err := h.store.FindSCIMIdentityByID(c, row.SCIMID)
		if err != nil {
			scimErr(c, http.StatusInternalServerError, "could not read identity")
			return false
		}
		ident.ExternalID = optStr(strings.TrimSpace(draft.ExternalID))
		if err := h.store.UpdateSCIMIdentity(c, ident); err != nil {
			scimErr(c, http.StatusInternalServerError, "could not update externalId")
			return false
		}
	}
	return true
}

// nameOf folds a SCIM name sub-attribute into the display name.
func nameOf(u *sso.SCIMUser) string {
	if u == nil || u.Name == nil {
		return ""
	}
	return sso.JoinName(u.Name.GivenName, u.Name.FamilyName)
}

// effectiveActive reads the `active` flag from a create/replace body,
// defaulting to TRUE when the attribute is omitted (a provisioned account
// starts active).
func effectiveActive(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if v, ok := m["active"]; ok {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				return b
			}
		}
	}
	return true
}

// optStr returns a pointer to s, or nil when empty (so a cleared externalId
// stores NULL).
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefStr flattens a nullable column to its value or "".
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
