-- §35 webhook event expansion: user_notifications_event_check pinned
-- the original 19 event names (see 20261008_145000). The vocabulary
-- grew to the 28 names in model.CustomerWebhookEvents — 9 new events
-- (product.created/updated/deleted, invoice.paid, invoice.voided,
-- order.failed, order.refunded, subscription.renewed,
-- usage.threshold_reached) are now dispatchable and subscribable, so
-- the hub CHECK must accept them before hub notification rows can
-- carry the new names. Same closed-vocabulary doctrine: the check is
-- still exactly CustomerWebhookEvents, not a superset.

ALTER TABLE user_notifications
    DROP CONSTRAINT IF EXISTS user_notifications_event_check;

ALTER TABLE user_notifications
    ADD CONSTRAINT user_notifications_event_check
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
        'release.unyanked',
        'product.created',
        'product.updated',
        'product.deleted',
        'invoice.paid',
        'invoice.voided',
        'order.failed',
        'order.refunded',
        'subscription.renewed',
        'usage.threshold_reached'
    ));
