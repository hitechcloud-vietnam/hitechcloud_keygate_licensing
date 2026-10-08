-- Enterprise SSO + SCIM provisioning (Phase 8, slice 2): the mapping
-- between SCIM 2.0 /Users resources and platform users.
--
-- Slice 1 created sso_connections + scim_tokens (the CONFIG and the
-- bearer CREDENTIAL). This slice is the runtime: the SAML/OIDC login
-- handshake and the SCIM /Users endpoints that CREATE, UPDATE and
-- DEPROVISION platform users on behalf of an identity provider.
--
-- scim_identities is the join between the two worlds. A SCIM-managed
-- user is an ordinary users row (it logs in through the normal portal
-- session exactly like any other account) plus one scim_identities row
-- that remembers "this user is provisioned from a directory" and holds
-- the two facts that only the directory knows:
--
--   - external_id — the IdP's own identifier for the person (Okta's
--     "externalId", Entra's). Nullable: not every provider sends one.
--     NOT unique here — the same value may be minted by two different
--     directories, and this table is not scoped to a connection (see
--     the note on user_id below). Indexed because a client may filter
--     on it.
--
--   - active — the SCIM "active" attribute. SCIM DELETE is a DEACTIVATE,
--     not a row removal: the account is preserved (its licences, orders
--     and audit trail stay intact) and this flag is what the SSO login
--     path consults before letting the person in. A deactivated identity
--     therefore blocks sign-in without destroying anything.
--
-- user_id is UNIQUE and a FK to users(id) ON DELETE CASCADE:
--
--   - UNIQUE because one platform user is provisioned at most once. The
--     SCIM userName IS the user's email, and users.email is already
--     unique — so "one identity per user" is the same cardinality rule,
--     expressed as a database constraint so a retry that races past the
--     handler's pre-check still cannot create a second row.
--
--   - CASCADE so deleting a platform user (admin hard-delete) takes its
--     provisioning bookkeeping with it instead of orphaning a row no
--     endpoint can ever resolve.
--
-- The rows here deliberately do NOT carry the user's name or email: those
-- live on the users row (single source of truth). GivenName/FamilyName in
-- the SCIM payload are derived from users.name on the way out and folded
-- back into users.name on the way in (see handler/scim.go), so a name
-- change via SCIM is a normal user-profile update, not a second copy that
-- can drift.
CREATE TABLE IF NOT EXISTS scim_identities (
    id          TEXT PRIMARY KEY,
    -- The IdP's identifier for the user. Nullable; indexed for the
    -- externalId filter. Not unique — see the header note.
    external_id TEXT,
    -- The provisioned platform user. UNIQUE (one identity per user) and
    -- CASCADE on user deletion.
    user_id     TEXT NOT NULL
                CONSTRAINT scim_identities_user_id_key UNIQUE
                REFERENCES users(id) ON DELETE CASCADE,
    -- SCIM "active". false = deactivated (SCIM DELETE); the SSO login
    -- path refuses a deactivated identity.
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A client may filter /Users by externalId; keep that lookup indexed.
CREATE INDEX IF NOT EXISTS scim_identities_external_id_idx
    ON scim_identities (external_id);
