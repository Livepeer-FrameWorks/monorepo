-- Crypto wallet status + quote model reshape (Purser).
--
-- The schema file (pkg/database/sql/schema/purser.sql) carries the canonical
-- shape; this migration brings deployed environments that don't re-provision
-- onto the new column set, status enum, and indexes.
--
-- Reshape rather than expand-only: pre-launch, no production data to preserve.
--
-- Safe on every DB in postgres_databases: the body only fires when the
-- `purser.crypto_wallets` table is present, so applying this migration to
-- quartermaster / commodore / other databases is a no-op.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = 'purser'
          AND table_name = 'crypto_wallets'
    ) THEN
        RETURN;
    END IF;

    -- Drop the old (network, confirmed_tx_hash) unique index before renaming
    -- the column underneath it.
    DROP INDEX IF EXISTS purser.idx_purser_crypto_wallets_confirmed_tx;

    -- Drop status/quote constraints up front so column shape changes and
    -- value remaps below don't trip them. Re-added at the end.
    ALTER TABLE purser.crypto_wallets DROP CONSTRAINT IF EXISTS chk_wallet_status;
    ALTER TABLE purser.crypto_wallets DROP CONSTRAINT IF EXISTS chk_wallet_quote_source;
    ALTER TABLE purser.crypto_wallets DROP CONSTRAINT IF EXISTS chk_wallet_credited_currency;

    -- New columns. Idempotent so the migration can be re-applied safely.
    ALTER TABLE purser.crypto_wallets
        ADD COLUMN IF NOT EXISTS confirmations              INTEGER,
        ADD COLUMN IF NOT EXISTS received_amount_base_units NUMERIC(78,0),
        ADD COLUMN IF NOT EXISTS credited_amount_cents      BIGINT,
        ADD COLUMN IF NOT EXISTS credited_amount_currency   VARCHAR(3),
        ADD COLUMN IF NOT EXISTS detected_at                TIMESTAMP WITH TIME ZONE,
        ADD COLUMN IF NOT EXISTS completed_at               TIMESTAMP WITH TIME ZONE,
        ADD COLUMN IF NOT EXISTS expected_amount_base_units NUMERIC(78,0),
        ADD COLUMN IF NOT EXISTS quoted_price_usd           NUMERIC(28,18),
        ADD COLUMN IF NOT EXISTS quoted_usd_to_eur_rate     NUMERIC(12,8),
        ADD COLUMN IF NOT EXISTS quoted_at                  TIMESTAMP WITH TIME ZONE,
        ADD COLUMN IF NOT EXISTS quote_source               VARCHAR(20);

    -- Rename confirmed_tx_hash → tx_hash (or merge if both exist from a
    -- partial earlier run).
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'crypto_wallets'
          AND column_name = 'confirmed_tx_hash'
    ) THEN
        IF NOT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = 'purser'
              AND table_name = 'crypto_wallets'
              AND column_name = 'tx_hash'
        ) THEN
            ALTER TABLE purser.crypto_wallets
                RENAME COLUMN confirmed_tx_hash TO tx_hash;
        ELSE
            UPDATE purser.crypto_wallets
            SET tx_hash = confirmed_tx_hash
            WHERE tx_hash IS NULL
              AND confirmed_tx_hash IS NOT NULL;

            ALTER TABLE purser.crypto_wallets
                DROP COLUMN confirmed_tx_hash;
        END IF;
    END IF;

    -- Cover the case where neither column exists yet (fresh-but-not-recreated
    -- DB run before bootstrap schema is updated).
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'crypto_wallets'
          AND column_name = 'tx_hash'
    ) THEN
        ALTER TABLE purser.crypto_wallets
            ADD COLUMN tx_hash VARCHAR(66);
    END IF;

    -- Backfill received_amount_base_units from the legacy
    -- actual_amount_received DECIMAL(30,18) using per-asset decimals.
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'crypto_wallets'
          AND column_name = 'actual_amount_received'
    ) THEN
        UPDATE purser.crypto_wallets
        SET received_amount_base_units = CASE asset
            WHEN 'USDC' THEN TRUNC(actual_amount_received * 1000000)
            WHEN 'ETH'  THEN TRUNC(actual_amount_received * 1000000000000000000)
            WHEN 'LPT'  THEN TRUNC(actual_amount_received * 1000000000000000000)
            ELSE received_amount_base_units
        END
        WHERE received_amount_base_units IS NULL
          AND actual_amount_received IS NOT NULL;

        ALTER TABLE purser.crypto_wallets
            DROP COLUMN actual_amount_received;
    END IF;

    -- Split confirmed_at → (detected_at, completed_at). Treat the legacy
    -- single timestamp as both.
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'crypto_wallets'
          AND column_name = 'confirmed_at'
    ) THEN
        UPDATE purser.crypto_wallets
        SET completed_at = confirmed_at
        WHERE completed_at IS NULL
          AND confirmed_at IS NOT NULL;

        UPDATE purser.crypto_wallets
        SET detected_at = confirmed_at
        WHERE detected_at IS NULL
          AND confirmed_at IS NOT NULL;

        ALTER TABLE purser.crypto_wallets
            DROP COLUMN confirmed_at;
    END IF;

    -- Status remap to the new canonical set. 'swept' and 'expired' carry
    -- through unchanged; 'active' → 'pending', 'used' → 'completed'.
    UPDATE purser.crypto_wallets
    SET status = CASE status
        WHEN 'active' THEN 'pending'
        WHEN 'used'   THEN 'completed'
        ELSE status
    END
    WHERE status IN ('active', 'used');

    -- Backfill confirmations for legacy rows so the NOT NULL set below
    -- doesn't trip.
    UPDATE purser.crypto_wallets
    SET confirmations = 0
    WHERE confirmations IS NULL;

    -- Defaults / nullability.
    ALTER TABLE purser.crypto_wallets
        ALTER COLUMN status        SET DEFAULT 'pending',
        ALTER COLUMN confirmations SET DEFAULT 0,
        ALTER COLUMN confirmations SET NOT NULL,
        ALTER COLUMN network       DROP DEFAULT;

    -- Re-add status / quote constraints under the new shape.
    ALTER TABLE purser.crypto_wallets
        ADD CONSTRAINT chk_wallet_status
            CHECK (status IN ('pending', 'confirming', 'completed', 'swept', 'expired')),
        ADD CONSTRAINT chk_wallet_quote_source
            CHECK (quote_source IS NULL OR quote_source IN ('chainlink', 'one_to_one')),
        ADD CONSTRAINT chk_wallet_credited_currency
            CHECK (credited_amount_currency IS NULL OR credited_amount_currency ~ '^[A-Z]{3}$');

    -- New unique index on (network, tx_hash) for de-duping confirmed
    -- transactions per chain. Replaces idx_purser_crypto_wallets_confirmed_tx.
    CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_crypto_wallets_tx
        ON purser.crypto_wallets(network, tx_hash)
        WHERE tx_hash IS NOT NULL;
END
$$;
