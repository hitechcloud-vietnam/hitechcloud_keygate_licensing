DROP INDEX IF EXISTS idx_seats_email_lower;
DROP INDEX IF EXISTS idx_licenses_email_lower;
-- The lower-cased user addresses are not restored: the original
-- capitalisation is not kept, and it never identified anyone.
