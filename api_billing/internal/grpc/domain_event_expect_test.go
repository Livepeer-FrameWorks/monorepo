package grpc

import (
	"database/sql/driver"

	"github.com/DATA-DOG/go-sqlmock"
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
	if m.domain.value == "" {
		return false
	}
	switch id := v.(type) {
	case string:
		return id == m.domain.value
	case []byte:
		return string(id) == m.domain.value
	default:
		if s, ok := v.(interface{ String() string }); ok {
			return s.String() == m.domain.value
		}
		return false
	}
}

// expectBillingDetailsBeforeUpdate expects UpdateBillingDetails' read of the
// stored details, returning email and empty other fields.
func expectBillingDetailsBeforeUpdate(mock sqlmock.Sqlmock, tenantID, email string) {
	mock.ExpectQuery(`-- name: GetTenantBillingDetailsForUpdate`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address"}).
			AddRow(email, "", "", "", []byte(`{}`)))
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
