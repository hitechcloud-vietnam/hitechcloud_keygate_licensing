-- In-app notification center (plan §44 NOTIFICATION SYSTEM — its
-- "In-app" channel — and §88 NOTIFICATION CENTER: unread count,
-- read/unread, priority, timestamp, deep link).
--
-- NOT the `notifications` table. That one is the EMAIL-DEDUP LEASE
-- ledger keyed (license_id, tag) that keeps the expiry/dunning
-- reminder loops from mailing the same customer twice
-- (store.ClaimNotification / HasNotification). It is keyed by LICENSE,
-- its rows mean "this mail already went out", and it is written by the
-- scheduler. This table is the customer's INBOX: keyed by USER, its
-- rows mean "something happened, show it in the portal", and it is
-- written by the events hub (internal/events NotificationSink).
-- Different table, different key, different lifecycle — the two never
-- collide and neither reads or writes the other.
--
-- title_key is PINNED to equal event. The row stores no user-facing
-- prose: the client translates the event name via i18n (plan §83), so
-- a wording change is a locale-file edit, never a data migration. The
-- CHECK below makes that contract a database invariant rather than a
-- convention.
--
-- data is small context for the client (license_id, order_number,
-- amount_minor, …) and NEVER a secret: no license keys, no webhook
-- secrets, no tokens. link is a root-relative deep link (starts with
-- "/" — checked here too), never an absolute URL, so a notification
-- can only ever point inside the app.
--
-- Referential action: user_id ON DELETE CASCADE. A deleted account's
-- inbox goes with it (privacy: the rows are personal), and nothing
-- here can block or outlive a user deletion.
CREATE TABLE IF NOT EXISTS user_notifications (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The event name. Closed vocabulary: EXACTLY the 19 names in
    -- model.CustomerWebhookEvents (the same list customer webhooks
    -- subscribe to), so an in-app row and a webhook delivery can never
    -- disagree about what happened.
    event      TEXT NOT NULL
               CONSTRAINT user_notifications_event_check
               CHECK (event IN (
                   'license.created',
                   'license.activated',
                   'license.deactivated',
                   'license.expiry_changed',
                   'license.expired',
                   'license.canceled',
                   'license.suspended',
                   'license.reinstated',
                   'license.revoked',
                   'license.payment_failed',
                   'license.payment_recovered',
                   'quota.warning',
                   'quota.exceeded',
                   'seat.added',
                   'seat.removed',
                   'plan.changed',
                   'release.published',
                   'release.yanked',
                   'release.unyanked'
               )),
    -- PINNED: the i18n key IS the event name (see header note).
    title_key  TEXT NOT NULL
               CONSTRAINT user_notifications_title_key_check
               CHECK (title_key = event),
    -- Small client-side params, JSONB so the client can read them
    -- without a schema change per event. Nullable.
    data       JSONB,
    -- Deep link path, root-relative. Nullable: not every event has a
    -- meaningful destination page.
    link       TEXT
               CONSTRAINT user_notifications_link_check
               CHECK (link IS NULL OR link LIKE '/%'),
    -- Closed vocabulary (model.NotificationPriority*).
    priority   TEXT NOT NULL DEFAULT 'normal'
               CONSTRAINT user_notifications_priority_check
               CHECK (priority IN ('normal', 'high')),
    -- NULL = unread. Stamped once by POST .../read or .../read-all.
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The inbox walk: "this user's rows (optionally only unread), newest
-- first". Leading user_id + read_at serve the unread filter and the
-- unread badge count; created_at DESC carries the listing order.
CREATE INDEX IF NOT EXISTS idx_user_notifications_user_read_created
    ON user_notifications (user_id, read_at, created_at DESC);
