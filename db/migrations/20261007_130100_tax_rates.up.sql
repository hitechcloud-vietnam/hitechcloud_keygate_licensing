-- Tax rates (one rate per jurisdiction, consumed at checkout)
CREATE TABLE IF NOT EXISTS tax_rates (
    id           TEXT PRIMARY KEY,
    jurisdiction TEXT NOT NULL UNIQUE,
    basis_points BIGINT NOT NULL DEFAULT 0,
    inclusive    BOOLEAN NOT NULL DEFAULT false,
    country      TEXT NOT NULL DEFAULT '',
    region       TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tax_rates_active ON tax_rates(active);
CREATE INDEX IF NOT EXISTS idx_tax_rates_country_region ON tax_rates(country, region);
