package handler

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// AdminSearchHandler serves the admin side of global search (plan
// §86), the data half of the command palette (plan §87):
//
//	GET /admin/search?q=<term>&types=a,b&limit=20
//	    → {results: [{type, id, title, subtitle, url}]}
//
// types is a comma-separated subset of the closed store.SearchTypes
// vocabulary; absent means every type. limit clamps to 50 and
// defaults to 20. An empty q answers an empty list — "everything" is
// not a search.
//
// The `url` values are SPA routes (paths into the admin React app),
// never absolute URLs: the palette navigates in-app with them. They
// never carry a credential — a licence hit links to the licence list
// filtered by the customer email, and the key that found it stays
// server-side.
//
// RBAC (plan §8): result types are filtered to the caller's granted
// permissions, mirroring middleware.RequirePermission exactly — an
// is_admin session or an API-key request passes unfiltered (see the
// backward-compat wildcard on that middleware), everyone else keeps
// only the types their permission set covers. The route itself sits
// behind the admin group; this filter is the finer sieve inside it.
type AdminSearchHandler struct {
	store searchAdminStore
}

// searchAdminStore is the seam this handler needs from the store.
// *store.Store satisfies it (see the compile-time assertion below);
// tests substitute a fake so the handler can be exercised without a
// database.
type searchAdminStore interface {
	Search(ctx context.Context, opts store.SearchOptions) ([]store.SearchHit, error)
	PermissionsForUser(ctx context.Context, userID string) (map[string]bool, error)
}

var _ searchAdminStore = (*store.Store)(nil)

// NewAdminSearchHandler wires the handler to the store. The caller
// (main.go) passes the concrete *store.Store.
func NewAdminSearchHandler(s *store.Store) *AdminSearchHandler {
	return &AdminSearchHandler{store: s}
}

// The handler's own clamps. The store clamps again — these exist so
// the request's own limit is what the merge truncates to, and so the
// echo in a test can pin the contract without a database.
const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
)

// searchTypePermission maps each result type to the permission its
// admin routes are gated by (see cmd/server/main.go). One vocabulary,
// one meaning: if the caller could not open the page a hit links to,
// the hit must not be shown. Types with no read permission of their
// own ride the one their surface is served under — subscriptions and
// devices live under the licence screens, invoices under orders.
var searchTypePermission = map[string]string{
	store.SearchTypeProduct:      model.PermProductsRead,
	store.SearchTypeCustomer:     model.PermCustomersRead,
	store.SearchTypeOrder:        model.PermOrdersRead,
	store.SearchTypeInvoice:      model.PermOrdersRead,
	store.SearchTypeLicense:      model.PermLicensesRead,
	store.SearchTypeSubscription: model.PermLicensesRead,
	store.SearchTypeDevice:       model.PermLicensesRead,
	store.SearchTypeReseller:     model.PermCustomersRead,
	store.SearchTypeAffiliate:    model.PermCustomersRead,
}

// searchResult is one rendered hit — exactly the JSON contract.
type searchResult struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	URL      string `json:"url"`
}

// searchParseTypes reads ?types= into validated members, in the
// caller's own order. The second result is false when a member is
// outside the closed vocabulary — refused rather than dropped, so a
// typo in a saved palette shortcut cannot quietly widen or narrow a
// search into silence. An empty string means every type.
func searchParseTypes(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		typ := strings.TrimSpace(part)
		if typ == "" {
			continue
		}
		if !store.ValidSearchType(typ) {
			return nil, false
		}
		if !seen[typ] {
			seen[typ] = true
			out = append(out, typ)
		}
	}
	return out, true
}

// searchFilterTypes applies the RBAC sieve to the requested types.
// The rules mirror middleware.RequirePermission to the letter: no
// session identity is 401, api_key auth and is_admin sessions pass
// unfiltered, everyone else keeps only the types their permission set
// covers (or everything, for the reserved "*.*" wildcard).
func searchFilterTypes(c *gin.Context, perms searchAdminStore, userID string, types []string) ([]string, bool) {
	if t, _ := c.Get("auth_type"); t == "api_key" {
		return types, true
	}
	if admin, _ := c.Get("is_admin"); admin == true {
		return types, true
	}
	set, err := perms.PermissionsForUser(c.Request.Context(), userID)
	if err != nil {
		response.Internal(c, err)
		return nil, false
	}
	if set[model.WildcardPermission] {
		return types, true
	}
	requested := types
	if len(requested) == 0 {
		requested = store.SearchTypes
	}
	out := make([]string, 0, len(requested))
	for _, typ := range requested {
		if set[searchTypePermission[typ]] {
			out = append(out, typ)
		}
	}
	// Non-nil even when empty: "no types left" is a legitimate answer
	// (an empty result set), and the caller must be able to tell it
	// apart from "no filter".
	return out, true
}

// searchMerge interleaves the per-type groups round-robin and
// truncates to limit. Interleaving rather than concatenating means
// one busy type cannot fill the whole first page and starve the rest
// — a palette that only ever shows products has not searched
// everything the admin asked for.
func searchMerge(hits []store.SearchHit, limit int) []store.SearchHit {
	byType := map[string][]store.SearchHit{}
	var order []string
	for _, h := range hits {
		if _, ok := byType[h.Type]; !ok {
			order = append(order, h.Type)
		}
		byType[h.Type] = append(byType[h.Type], h)
	}
	out := make([]store.SearchHit, 0, limit)
	for round := 0; len(out) < limit; round++ {
		progressed := false
		for _, typ := range order {
			group := byType[typ]
			if round < len(group) {
				out = append(out, group[round])
				progressed = true
				if len(out) >= limit {
					break
				}
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

// searchResultURL renders the SPA route a hit links to.
//
// The paths with a detail screen link to it (orders, resellers,
// affiliates); the rest link to the list page with ?search= set to
// the column that page's own search box filters on, so landing there
// shows the row the palette found. No query here can name a
// credential: the licence links by customer email, the device by its
// licence's email.
func searchResultURL(h store.SearchHit) string {
	switch h.Type {
	case store.SearchTypeProduct:
		return "/admin/products?search=" + url.QueryEscape(h.Title)
	case store.SearchTypeCustomer:
		return "/admin/customers?search=" + url.QueryEscape(h.Subtitle)
	case store.SearchTypeOrder:
		return "/admin/orders/" + url.PathEscape(h.ID)
	case store.SearchTypeInvoice:
		return "/admin/orders/" + url.PathEscape(h.Ref)
	case store.SearchTypeLicense:
		return "/admin/licenses?search=" + url.QueryEscape(h.Title)
	case store.SearchTypeSubscription, store.SearchTypeDevice:
		return "/admin/licenses?search=" + url.QueryEscape(h.Subtitle)
	case store.SearchTypeReseller:
		return "/admin/resellers/" + url.PathEscape(h.ID)
	case store.SearchTypeAffiliate:
		return "/admin/affiliates/" + url.PathEscape(h.ID)
	}
	return "/admin"
}

// Search answers GET /admin/search. Query parameters:
//
//	q      the term; empty answers {results: []} without a lookup
//	types  comma-separated subset of the vocabulary; absent = all
//	limit  1..50, default 20 (clamped, not refused)
func (h *AdminSearchHandler) Search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	limit := queryInt(c, "limit", searchDefaultLimit)
	if limit < 1 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	types, ok := searchParseTypes(c.Query("types"))
	if !ok {
		response.BadRequest(c, "types must be a comma-separated list of: "+strings.Join(store.SearchTypes, ", "))
		return
	}

	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	types, ok = searchFilterTypes(c, h.store, userID, types)
	if !ok {
		return // response already written
	}

	// An empty term is answered without a lookup: searching for
	// nothing is not a request for everything. The same goes for a
	// sieve that left no types at all — but nil here means "no
	// filter" (every type), which is not the same as empty.
	if q == "" || (types != nil && len(types) == 0) {
		response.OK(c, gin.H{"results": response.Array([]searchResult{})})
		return
	}

	hits, err := h.store.Search(c.Request.Context(), store.SearchOptions{
		Query: q,
		Types: types,
		Limit: limit,
	})
	if err != nil {
		if errors.Is(err, store.ErrUnknownSearchType) {
			response.BadRequest(c, "unknown search type")
			return
		}
		response.Internal(c, err)
		return
	}

	results := make([]searchResult, 0, limit)
	for _, hit := range searchMerge(hits, limit) {
		results = append(results, searchResult{
			Type:     hit.Type,
			ID:       hit.ID,
			Title:    hit.Title,
			Subtitle: hit.Subtitle,
			URL:      searchResultURL(hit),
		})
	}
	response.OK(c, gin.H{"results": response.Array(results)})
}
