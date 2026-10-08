-- Enterprise SSO + SCIM groundwork (Phase 8, slice 1): admin-managed
-- SSO connection configuration (SAML + OIDC) and SCIM provisioning
-- tokens. This slice is CONFIGURATION + TOKENS ONLY — the SAML
-- handshake, the OIDC authorization-code flow and the SCIM 2.0 /Users
-- and /Groups sync endpoints that will READ these tables are future
-- work and deliberately have no routes or tables here.
--
-- Two tables, both self-contained (no foreign keys to the rest of the
-- schema yet): a connection is global configuration, and a SCIM token
-- is a bearer credential — neither references a product, plan, licence
-- or user today.

-- One row per identity-provider connection. The email domain is the
-- handle a future login routes on (alice@acme.com -> the acme.com
-- connection), so it is UNIQUE and stored folded lowercase — the fold
-- (model.NormalizeSSODomain) is what makes that uniqueness mean
-- anything: "Acme.COM" and "acme.com" must not be two connections.
CREATE TABLE IF NOT EXISTS sso_connections (
    id            TEXT PRIMARY KEY,
    -- Display label, unique so an admin can tell rows apart and the
    -- API can answer a clean 409 for a taken name. Bounded to match
    -- model's 100-char name rule.
    name          VARCHAR(100) NOT NULL
                  CONSTRAINT sso_connections_name_key UNIQUE,
    -- Closed vocabulary, enforced here as well as in the handler.
    provider_type TEXT NOT NULL
                  CONSTRAINT sso_connections_provider_type_check
                  CHECK (provider_type IN ('saml', 'oidc')),
    -- The email domain this connection serves. Folded lowercase on
    -- write (see model.NormalizeSSODomain); UNIQUE because one domain
    -- maps to exactly one connection.
    domain        TEXT NOT NULL
                  CONSTRAINT sso_connections_domain_key UNIQUE,
    -- Gates sign-in. A disabled connection keeps its config but refuses
    -- logins. Toggled only via the enable|disable endpoints.
    enabled       BOOLEAN NOT NULL DEFAULT true,

    -- ── SAML settings (provider_type = 'saml') ──
    saml_entity_id   TEXT,
    saml_sso_url     TEXT,
    -- The IdP's X.509 signing certificate as PEM. Public; nullable.
    saml_certificate TEXT,

    -- ── OIDC settings (provider_type = 'oidc') ──
    oidc_issuer TEXT,
    oidc_client_id TEXT,
    -- The OAuth client secret. Stored PLAINTEXT (the token exchange
    -- must present it back verbatim) — same house standard as the
    -- merchant/customer webhook secrets. Encryption at rest is a
    -- documented follow-up (see model/sso.go). Never returned by the
    -- API (json:"-").
    oidc_client_secret TEXT,
    oidc_scopes       TEXT,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Provider-config CHECK: the ACTIVE provider's required settings
    -- must be present. This is the database backstop for the same rule
    -- model.SSOConnection.Validate enforces in Go — a saml connection
    -- is refused without entity id / SSO URL / certificate, and an oidc
    -- connection without issuer / client id. The INACTIVE provider's
    -- fields are unconstrained (left NULL). Note saml_certificate is
    -- checked for NOT NULL here; the "is it actually PEM" shape check is
    -- a Go-side concern (model.ValidPEMCertificate), not a SQL one.
    CONSTRAINT sso_connections_provider_config_check CHECK (
        (provider_type = 'saml'
         AND saml_entity_id IS NOT NULL
         AND saml_sso_url IS NOT NULL
         AND saml_certificate IS NOT NULL)
        OR
        (provider_type = 'oidc'
         AND oidc_issuer IS NOT NULL
         AND oidc_client_id IS NOT NULL)
    )
);
-- The domain lookup the future login route will do on every sign-in is
-- already covered by the UNIQUE constraint's index on domain; likewise
-- name. No extra index is needed for the reads this slice does.

-- SCIM provisioning tokens: the bearer credentials a directory (Okta,
-- Entra ID, …) will present to a future SCIM 2.0 /Users + /Groups sync
-- endpoint. This slice only mints and manages them.
--
-- Token storage is the CustomerAPIKey recipe: the plaintext is returned
-- EXACTLY ONCE at creation and never again; the row keeps only
-- token_hash (SHA-256 hex) and token_prefix (a short display hint). No
-- column holds the plaintext, so nothing can leak it from the database.
CREATE TABLE IF NOT EXISTS scim_tokens (
    id           TEXT PRIMARY KEY,
    -- Display label ("Okta provisioning"). Bounded to the same 100-char
    -- name rule as sso_connections.name.
    name         VARCHAR(100) NOT NULL,
    -- SHA-256 hex of the full token — exactly 64 lowercase hex chars
    -- (model.HashSCIMToken). UNIQUE so the same token is never minted
    -- twice; the look-up the future SCIM middleware does is by this
    -- column (store.FindSCIMTokenByHash).
    token_hash   CHAR(64) NOT NULL
                 CONSTRAINT scim_tokens_hash_key UNIQUE,
    -- First SCIMTokenDisplayPrefixLength ("htc_scim_" + 4) chars of the
    -- token. Displayable, useless for authentication.
    token_prefix TEXT NOT NULL,
    -- Stamped on a successful authentication (future SCIM middleware).
    last_used_at TIMESTAMPTZ,
    -- Soft-revoke stamp. A revoked token is dead but the row keeps its
    -- audit trail. NULL while usable.
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The unique index on token_hash is the auth look-up; the listing walk
-- is "all tokens, newest first" so an index on (created_at, id) would
-- only matter at a scale a handful of tokens never reaches. None added.
