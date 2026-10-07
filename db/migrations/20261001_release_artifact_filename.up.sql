-- The name the artifact had when it was uploaded (issue #37). Storage
-- keys stay generated; this is shown in the dashboard so an admin can
-- tell which build each artifact came from. Empty for older rows.
ALTER TABLE release_artifacts ADD COLUMN IF NOT EXISTS filename TEXT NOT NULL DEFAULT '';
