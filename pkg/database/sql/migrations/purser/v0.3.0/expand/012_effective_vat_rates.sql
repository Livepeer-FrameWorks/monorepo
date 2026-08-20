-- Effective-dated VAT evidence. Historic invoice rows retain the selected
-- source/effective date, while operators can add a future period without
-- rewriting issued records.

CREATE TABLE IF NOT EXISTS purser.vat_rate_periods (
    country_code CHAR(2) NOT NULL,
    rate_bps INTEGER NOT NULL CHECK (rate_bps >= 0 AND rate_bps <= 10000),
    effective_from DATE NOT NULL,
    effective_until DATE,
    source VARCHAR(255) NOT NULL,
    source_reference TEXT NOT NULL,
    source_checked_on DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(country_code, effective_from),
    CHECK (effective_until IS NULL OR effective_until > effective_from)
);

INSERT INTO purser.vat_rate_periods
    (country_code, rate_bps, effective_from, source, source_reference, source_checked_on)
VALUES
    ('AT',2000,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('BE',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('BG',2000,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('HR',2500,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('CY',1900,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('CZ',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('DK',2500,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('EE',2400,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('FI',2550,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('FR',2000,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('DE',1900,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('GR',2400,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('HU',2700,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('IE',2300,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('IT',2200,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('LV',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('LT',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('LU',1700,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('MT',1800,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('NL',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('PL',2300,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('PT',2300,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('RO',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('SK',2300,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('SI',2200,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('ES',2100,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13'),
    ('SE',2500,'2026-01-01','EU Your Europe standard VAT rates','https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm','2026-07-13')
ON CONFLICT (country_code, effective_from) DO NOTHING;

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS vat_rate_effective_from DATE,
    ADD COLUMN IF NOT EXISTS fx_rate_source VARCHAR(255),
    ADD COLUMN IF NOT EXISTS fx_rate_observed_at TIMESTAMPTZ;
