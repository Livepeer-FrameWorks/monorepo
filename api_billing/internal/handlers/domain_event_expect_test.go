package handlers

import (
	"database/sql/driver"

	"github.com/DATA-DOG/go-sqlmock"
)

// Domain events require UUID tenants, so the handler tests that reach an
// event use these.
const (
	checkoutTenantID = "a0000000-0000-4000-8000-00000000000a"
	webhookTenantID  = "a0000000-0000-4000-8000-000000000001"
)

// domainEventID captures the event_id of an expected domain outbox insert so a
// later expectation can require the legacy billing event row to reuse it.
type domainEventID struct{ value string }

// Match records the event ID of the domain outbox insert.
func (c *domainEventID) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	c.value = s
	return true
}

// sameEventID matches a legacy outbox row ID equal to a captured domain
// event ID.
type sameEventID struct{ domain *domainEventID }

// Match reports whether the legacy row ID is the captured domain event ID.
func (m sameEventID) Match(v driver.Value) bool {
	s, ok := v.(string)
	return ok && m.domain.value != "" && s == m.domain.value
}

// expectDomainEvent expects one insert of eventType for tenantID into
// purser.domain_event_outbox and returns its captured event ID.
func expectDomainEvent(mock sqlmock.Sqlmock, eventType, tenantID string) *domainEventID {
	id := &domainEventID{}
	mock.ExpectExec(`INSERT INTO purser\.domain_event_outbox`).
		WithArgs(id, eventType, "purser", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			"tenant", tenantID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return id
}
