-- When subscriptions.cancel_at_period_end last came from Stripe. Before
-- this release nothing wrote that column, so a subscription a customer
-- had already set to cancel still reads "renews". NULL means "not known
-- yet": the reminders send neither "renews tomorrow" nor "expiring" for
-- it until the Stripe sync (StripeHandler.SyncCancelStates) or a webhook
-- has recorded the real state.
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS cancel_state_synced_at TIMESTAMPTZ;
