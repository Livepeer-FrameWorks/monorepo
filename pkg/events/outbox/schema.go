// Package outbox is the shared transactional outbox for domain events. A
// producer inserts the event with Enqueue inside the transaction that commits
// the state change, and a Relay delivers committed rows to Decklog's
// PublishDomainEvents.
package outbox

import (
	"fmt"
	"regexp"
	"strings"
)

// TableName is the outbox table every producing service creates in its own
// schema.
const TableName = "domain_event_outbox"

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateSchema(schema string) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("outbox: schema %q is not a lower snake case identifier", schema)
	}
	return nil
}

// TableDDL returns the outbox table and indexes for schema. It is the one
// definition of the table: a service's baseline and its expand migration use
// this DDL verbatim, with the schema substituted.
//
//   - event_id is the UUIDv7 events.New generated; the relay sends it on every
//     attempt, so a redelivery after a lost acknowledgment keeps the ID.
//   - The relay orders an aggregate's rows by (enqueued_at, event_id) and
//     claims a row only when no older row of the same aggregate is incomplete.
//     enqueued_at is clock_timestamp(), so rows written by one transaction keep
//     their insert order.
//   - scope and tenant_id are checked together: tenant rows carry a tenant,
//     platform rows never do.
//   - actor_token_hash is the decimal uint64 from events.Actor, or empty.
func TableDDL(schema string) (string, error) {
	if err := validateSchema(schema); err != nil {
		return "", err
	}
	return strings.ReplaceAll(tableDDLTemplate, "{schema}", schema), nil
}

const tableDDLTemplate = `CREATE TABLE IF NOT EXISTS {schema}.domain_event_outbox (
    event_id          UUID PRIMARY KEY,
    event_type        TEXT NOT NULL,
    source            TEXT NOT NULL,
    aggregate_type    TEXT NOT NULL,
    aggregate_id      TEXT NOT NULL,
    aggregate_version BIGINT NOT NULL DEFAULT 0,
    scope             TEXT NOT NULL,
    tenant_id         UUID,
    actor_auth_type   TEXT NOT NULL DEFAULT '',
    actor_user_id     TEXT NOT NULL DEFAULT '',
    actor_token_hash  TEXT NOT NULL DEFAULT '',
    occurred_at       TIMESTAMPTZ NOT NULL,
    payload           BYTEA NOT NULL,
    enqueued_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    claimed_at        TIMESTAMPTZ,
    lease_token       UUID,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    completed_at      TIMESTAMPTZ,
    CONSTRAINT chk_{schema}_domain_event_outbox_scope CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT chk_{schema}_domain_event_outbox_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_{schema}_domain_event_outbox_pending
    ON {schema}.domain_event_outbox (enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_{schema}_domain_event_outbox_aggregate
    ON {schema}.domain_event_outbox (aggregate_type, aggregate_id, enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_{schema}_domain_event_outbox_completed
    ON {schema}.domain_event_outbox (completed_at)
    WHERE completed_at IS NOT NULL;
`
