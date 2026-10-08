-- Reverse: sso_connections + scim_tokens. Both are self-contained
-- configuration/credential tables with no inbound foreign keys, so they
-- drop cleanly. Dropping a table drops its indexes and constraints, so
-- the explicit drops below are only there to stay quiet on a database
-- that never had them.
--
-- A down migration unwrites SCHEMA, not the operational reality it
-- recorded: in particular the SCIM tokens that a directory may still be
-- holding are NOT retroactively revoked here. An operator rolling this
-- back should revoke them through the API first, exactly as they would
-- before dropping any credential table.
DROP INDEX IF EXISTS scim_tokens_hash_key;
DROP INDEX IF EXISTS sso_connections_domain_key;
DROP INDEX IF EXISTS sso_connections_name_key;
DROP TABLE IF EXISTS scim_tokens;
DROP TABLE IF EXISTS sso_connections;
