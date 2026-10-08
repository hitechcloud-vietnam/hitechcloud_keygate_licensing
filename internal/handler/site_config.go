package handler

import (
	"context"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/branding"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/surface"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// SiteConfigHandler serves GET /api/v1/site-config: the public
// branding every surface's SPA needs at boot, extended with the domain
// map (which host serves which surface) and the payment methods this
// install can charge through right now.
//
// It EXTENDS the existing public /api/v1/config contract (the flat
// settings map web/src/hooks/use-site-config.tsx reads) rather than
// replacing it: the same keys keep their names — site_name, brand_color,
// logo_url, timezone, language, base_url — and the two new shapes
// (surfaces, payment_methods) are added beside them.
//
// Attribution fields are AGPL v3 Section 7(b) obligations (see NOTICE)
// and are ALWAYS emitted — never omitted, never sourced from settings.
type SiteConfigHandler struct {
	// surfGetPublic reads the public branding settings (Store.GetPublicSettings).
	surfGetPublic func(ctx context.Context) (map[string]string, error)
	// surfGetSetting reads one settings row (Store.GetSetting), used for
	// the domain.* keys.
	surfGetSetting func(ctx context.Context, key string) (string, error)
	// surfListProviders lists configured payment provider ids
	// (payment.EnabledProviders); injectable so tests stay hermetic
	// against the package-global provider registry.
	surfListProviders func() []string
	// surfBaseURL is the install's public address (cfg.BaseURL), shown
	// so the dashboard can build copyable feed URLs.
	surfBaseURL string
}

// NewSiteConfigHandler wires the handler to the store. baseURL is the
// install's public BaseURL (cfg.BaseURL).
func NewSiteConfigHandler(s *store.Store, baseURL string) *SiteConfigHandler {
	return &SiteConfigHandler{
		surfGetPublic: func(ctx context.Context) (map[string]string, error) {
			return s.GetPublicSettings(ctx)
		},
		surfGetSetting: func(ctx context.Context, key string) (string, error) {
			return s.GetSetting(ctx, key)
		},
		surfListProviders: payment.EnabledProviders,
		surfBaseURL:       strings.TrimRight(baseURL, "/"),
	}
}

// surfPaymentMethod is one buyer-facing payment method
// ({id, name} — the same display names as the gateway checkout).
type surfPaymentMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Get answers GET /api/v1/site-config.
func (h *SiteConfigHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	// A branding read that fails is not a page failure: answer what is
	// known (attribution and addresses) rather than a 500 the login
	// page cannot render around.
	settings, err := h.surfGetPublic(ctx)
	if err != nil || settings == nil {
		settings = map[string]string{}
	}
	out := make(map[string]any, len(settings)+4)
	for k, v := range settings {
		out[k] = v
	}
	out["base_url"] = h.surfBaseURL

	// Attribution: AGPL v3 Section 7(b) — see NOTICE. Required output
	// on every config response; always branding constants, never a
	// setting that could be blanked.
	out["attribution_text"] = branding.Tagline
	out["attribution_url"] = branding.URL

	cfg := surface.FromSettings(func(key string) string {
		v, gerr := h.surfGetSetting(ctx, key)
		if gerr != nil {
			return ""
		}
		return v
	})
	out["surfaces"] = surface.PublicMap(cfg)

	ids := h.surfListProviders()
	sort.Strings(ids) // registry order is not stable; the answer is
	methods := make([]surfPaymentMethod, 0, len(ids))
	for _, id := range ids {
		methods = append(methods, surfPaymentMethod{ID: id, Name: gwDisplayName(id)})
	}
	out["payment_methods"] = methods

	response.OK(c, out)
}
