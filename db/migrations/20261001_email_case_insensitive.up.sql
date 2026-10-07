-- Email addresses are matched case-insensitively (issue #36). A licence
-- or seat keeps the address as it was typed, so lookups compare
-- lower(email); these indexes keep those lookups off a sequential scan.
CREATE INDEX IF NOT EXISTS idx_licenses_email_lower ON licenses (lower(email));
CREATE INDEX IF NOT EXISTS idx_seats_email_lower ON seats (lower(email));
-- Statistics for the new expressions, so the planner picks the indexes
-- right away instead of after the next autovacuum.
ANALYZE licenses;
ANALYZE seats;

-- A user row is a sign-in identity, and sign-in always uses the
-- lower-cased address, so a row stored with capitals (created by the
-- checkout path) could never be signed in to. Lower-case those rows,
-- except where another row already holds the lower-cased address:
-- merging two accounts could hand one person the other's access, so
-- those are left for an operator to resolve by hand.
UPDATE users u
SET email = lower(u.email), updated_at = now()
WHERE u.email <> lower(u.email)
  AND NOT EXISTS (
    SELECT 1 FROM users o WHERE o.id <> u.id AND lower(o.email) = lower(u.email)
  );
