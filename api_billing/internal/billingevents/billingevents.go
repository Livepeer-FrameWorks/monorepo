// Package billingevents builds Purser's domain events and writes them to
// purser.domain_event_outbox inside the transaction of the billing change they
// describe. A legacy billing_event_outbox row written for the same fact uses
// the domain event's ID as its row ID, so both streams carry one event ID.
package billingevents

import (
	"context"
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// Source is the CloudEvents source of every Purser domain event, and Schema
// the Postgres schema holding its outbox.
const (
	Source = "purser"
	Schema = "purser"
)

// New builds the domain event msg for tenantID's aggregateID. The actor is
// empty for changes Purser makes on its own schedule or on provider webhooks.
func New(tenantID, aggregateID string, msg proto.Message, actor events.Actor) (*events.Event, error) {
	ev, err := events.New(Source, tenantID, aggregateID, msg, events.WithActor(actor))
	if err != nil {
		return nil, fmt.Errorf("build domain event: %w", err)
	}
	return &ev, nil
}

// Enqueue writes ev to the domain outbox through exec, which must be the
// transaction that commits the change ev describes. A nil ev writes nothing.
func Enqueue(ctx context.Context, exec outbox.Execer, ev *events.Event) error {
	if ev == nil {
		return nil
	}
	if err := outbox.Enqueue(ctx, exec, Schema, *ev); err != nil {
		return fmt.Errorf("enqueue %s: %w", ev.Type, err)
	}
	return nil
}

// NewAndEnqueue builds msg and enqueues it through exec in one step, for facts
// that have no legacy billing event.
func NewAndEnqueue(ctx context.Context, exec outbox.Execer, tenantID, aggregateID string, msg proto.Message, actor events.Actor) error {
	ev, err := New(tenantID, aggregateID, msg, actor)
	if err != nil {
		return err
	}
	return Enqueue(ctx, exec, ev)
}

// LegacyRowID is the billing_event_outbox row ID for a legacy event: the
// domain event's ID when the same fact has one, otherwise a fresh UUIDv7.
func LegacyRowID(ev *events.Event) (uuid.UUID, error) {
	if ev == nil {
		return uuid.NewV7()
	}
	return uuid.Parse(ev.ID)
}

// EUR returns a Money value of cents in the ledger currency.
func EUR(cents int64) *publicv1.Money {
	return &publicv1.Money{AmountMinor: cents, Currency: billing.LedgerCurrency}
}

// EURFromDecimal converts a ledger amount stored as a two-decimal NUMERIC
// string into Money.
func EURFromDecimal(amount string) (*publicv1.Money, error) {
	value, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return nil, fmt.Errorf("parse ledger amount %q: %w", amount, err)
	}
	return EUR(value.Shift(2).Round(0).IntPart()), nil
}
