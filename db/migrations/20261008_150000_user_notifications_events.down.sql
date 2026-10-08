-- Value-guarded rollback: only shrink the event vocabulary back to
-- the original 19 names if no row uses one of the 9 additions —
-- refusing to roll back beats failing mid-DO block on live data.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM user_notifications
        WHERE event IN (
            'product.created',
            'product.updated',
            'product.deleted',
            'invoice.paid',
            'invoice.voided',
            'order.failed',
            'order.refunded',
            'subscription.renewed',
            'usage.threshold_reached'
        )
    ) THEN
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
                'release.unyanked'
            ));
    END IF;
END $$;
