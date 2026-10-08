-- Reverse of 20261008_137000_affiliates: drop in reverse-dependency
-- order (the FK graph is acyclic — conversions and payouts hang off
-- affiliates, clicks off codes — so this is simply children first).
DROP TABLE IF EXISTS affiliate_payouts;
DROP TABLE IF EXISTS affiliate_conversions;
DROP TABLE IF EXISTS referral_clicks;
DROP TABLE IF EXISTS referral_codes;
DROP TABLE IF EXISTS affiliates;
