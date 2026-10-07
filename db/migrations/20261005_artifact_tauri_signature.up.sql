-- The signature Tauri's updater verifies: a base64 minisign signature
-- file (signature, trusted comment, global signature). The global
-- signature needs the private key, so it is made when the artifact is
-- signed and stored here rather than derived from ed25519_sig at feed
-- time. Empty = unsigned, or signed before this column existed (the
-- server backfills those at startup from the stored signing key).
ALTER TABLE release_artifacts ADD COLUMN IF NOT EXISTS tauri_signature TEXT NOT NULL DEFAULT '';
