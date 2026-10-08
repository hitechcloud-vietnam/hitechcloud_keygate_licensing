package handler

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ConfigAdminHandler is the admin API over the configuration catalog
// (config-in-DB). It is the backend the admin Settings UI reads and
// writes every catalog key through:
//
//	GET    /api/v1/admin/config         — catalog + effective values
//	PUT    /api/v1/admin/config         — {values, secrets} update
//	DELETE /api/v1/admin/config/:key    — reset to env/default
//
// Authorization is the caller's concern: the Lead mounts these behind
// an admin session and middleware.RequirePermission(model.PermSettingsManage, db).
// The handler only records WHO changed what in the audit log.
//
// Secret values never leave the server: GET reports presence only, the
// audit log stores them masked, and PUT takes them in a separate
// `secrets` map so a form can save everything else without echoing a
// credential back.
type ConfigAdminHandler struct {
	svc   *service.ConfigService
	audit configsvcAuditor
}

// configsvcAuditor is the audit seam (store.Store satisfies it).
type configsvcAuditor interface {
	Audit(ctx context.Context, log *model.AuditLog)
}

// NewConfigAdminHandler wires the config admin API over the settings
// store and the config service.
func NewConfigAdminHandler(s *store.Store, svc *service.ConfigService) *ConfigAdminHandler {
	return &ConfigAdminHandler{svc: svc, audit: s}
}

// configsvcKeyView is one key in the GET response.
type configsvcKeyView struct {
	Key             string `json:"key"`
	Category        string `json:"category"`
	Type            string `json:"type"`
	Value           string `json:"value"`
	Set             bool   `json:"set"`
	Secret          bool   `json:"secret"`
	Default         string `json:"default"`
	EnvVar          string `json:"env_var"`
	Description     string `json:"description"`
	RestartRequired bool   `json:"restart_required"`
}

// configsvcCategoryView is one category in the GET response.
type configsvcCategoryView struct {
	ID   string             `json:"id"`
	Name string             `json:"name"`
	Keys []configsvcKeyView `json:"keys"`
}

// configsvcMask is what the audit log stores instead of a secret.
// Its presence still tells the story: "" = not configured, "***" =
// configured.
const configsvcMask = "***"

func configsvcMasked(set bool) string {
	if set {
		return configsvcMask
	}
	return ""
}

// GetConfig answers the full catalog with the effective value of every
// key. Secrets always report value "" and a `set` flag — the value is
// never returned, not even to an admin with full permissions.
func (h *ConfigAdminHandler) GetConfig(c *gin.Context) {
	res, err := h.svc.Resolve(c)
	if err != nil {
		response.Internal(c, err)
		return
	}

	cats := make([]configsvcCategoryView, 0, len(config.CategoryOrder()))
	for _, catID := range config.CategoryOrder() {
		cat := configsvcCategoryView{ID: catID, Name: config.CategoryLabel(catID), Keys: []configsvcKeyView{}}
		for _, e := range config.EntriesInCategory(catID) {
			kv := configsvcKeyView{
				Key:             e.Key,
				Category:        e.Category,
				Type:            e.Type,
				Secret:          config.IsSecretEntry(e),
				Default:         e.Default,
				EnvVar:          e.EnvVar,
				Description:     e.Description,
				RestartRequired: e.RestartRequired,
			}
			if kv.Secret {
				kv.Value = "" // never
			} else {
				kv.Value = res.String(e.Key)
			}
			kv.Set = res.IsSet(e.Key)
			cat.Keys = append(cat.Keys, kv)
		}
		cats = append(cats, cat)
	}
	response.OK(c, gin.H{"categories": cats})
}

// UpdateConfig validates and stores a batch of changes. One audit row
// per changed key (entity "settings", action "config.update", before
// and after recorded — secrets masked), so the trail answers who
// changed what and when without ever carrying a credential.
func (h *ConfigAdminHandler) UpdateConfig(c *gin.Context) {
	var req struct {
		Values  map[string]string `json:"values"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, `body must be {"values": {...}, "secrets": {...}}`)
		return
	}

	before, err := h.svc.Resolve(c)
	if err != nil {
		response.Internal(c, err)
		return
	}

	if err := h.svc.Set(c, req.Values, req.Secrets); err != nil {
		var verr *service.ConfigValidationError
		switch {
		case errors.As(err, &verr):
			response.ErrWithDetails(c, http.StatusBadRequest, "BAD_REQUEST",
				"one or more configuration values are invalid", verr.Problems)
		case errors.Is(err, store.ErrSecretEncryptionUnavailable):
			// Same code and status the legacy settings API uses: an
			// operator config problem, not a server fault.
			response.Err(c, http.StatusServiceUnavailable, "ENCRYPTION_NOT_CONFIGURED",
				"cannot store a secret without an encryption key — set SECRET_ENCRYPTION_KEY and restart")
		default:
			response.Internal(c, err)
		}
		return
	}

	// What actually changed: every non-secret value written, plus the
	// secrets that carried a new value (a blank secret is "unchanged").
	updated := make([]string, 0, len(req.Values)+len(req.Secrets))
	for k := range req.Values {
		updated = append(updated, k)
	}
	for k, v := range req.Secrets {
		if v != "" {
			updated = append(updated, k)
		}
	}
	sort.Strings(updated)

	restarted := []string{}
	for _, key := range updated {
		e, _ := config.FindEntry(key)
		if e.RestartRequired {
			restarted = append(restarted, key)
		}
		changes := map[string]any{}
		if config.IsSecretEntry(e) {
			changes["secret"] = true
			changes["before"] = configsvcMasked(before.IsSet(key))
			changes["after"] = configsvcMask
		} else {
			changes["before"] = before.String(key)
			changes["after"] = req.Values[key]
		}
		h.audit.Audit(c, &model.AuditLog{
			Entity: "settings", EntityID: key, Action: "config.update",
			ActorType: "admin", ActorID: adminID(c),
			Changes: changes,
		})
	}

	response.OK(c, gin.H{"updated": updated, "restarted_required": restarted})
}

// DeleteConfig resets one key: the stored row goes away and the key
// resolves back to its env var and then its catalog default.
func (h *ConfigAdminHandler) DeleteConfig(c *gin.Context) {
	key := c.Param("key")
	e, known := config.FindEntry(key)
	if !known {
		response.NotFound(c, "no such configuration key")
		return
	}

	before, err := h.svc.Resolve(c)
	if err != nil {
		response.Internal(c, err)
		return
	}
	if err := h.svc.Unset(c, key); err != nil {
		if errors.Is(err, service.ErrConfigKeyUnknown) {
			response.NotFound(c, "no such configuration key")
			return
		}
		response.Internal(c, err)
		return
	}

	changes := map[string]any{"after": "(reset to env/default)"}
	if config.IsSecretEntry(e) {
		changes["secret"] = true
		changes["before"] = configsvcMasked(before.IsSet(key))
	} else {
		changes["before"] = before.String(key)
	}
	h.audit.Audit(c, &model.AuditLog{
		Entity: "settings", EntityID: key, Action: "config.reset",
		ActorType: "admin", ActorID: adminID(c),
		Changes: changes,
	})
	response.OK(c, gin.H{"reset": key})
}

// ResetConfigKey is the POST form of the reset contract
// (POST /api/v1/admin/config/:key/reset → {reset: key}); identical to
// DELETE /api/v1/admin/config/:key. Register both routes.
func (h *ConfigAdminHandler) ResetConfigKey(c *gin.Context) {
	h.DeleteConfig(c)
}
