package periscopeingestdb

import (
	"reflect"
	"time"

	"github.com/google/uuid"
)

// identifiedStatements lists every INSERT whose table stores the ingest_id of
// the Kafka record that wrote the row. These are the append-only tables the
// consumer writes, where a redelivered record would otherwise add its rows a
// second time; replacing tables collapse a redelivery by their key and need
// none. A duplicate lookup is bounded by the rows' TenantID when the table's
// tenant_id is a UUID, and by the time column the table partitions or sorts
// by, read from the row field named in timeField.
var identifiedStatements = map[string]*statementIdentity{
	insertRoutingDecision:              byTenantAndTimestamp,
	insertStreamLifecycleEvent:         byTenantAndTimestamp,
	insertStreamBufferEvent:            byTenantAndTimestamp,
	insertStreamEndEvent:               byTenantAndTimestamp,
	insertTrackListStreamEvent:         byTenantAndTimestamp,
	insertPushRewriteEvent:             byTenantAndTimestamp,
	insertStreamLifecycleHealth:        byTenantAndTimestamp,
	insertStreamBufferHealth:           byTenantAndTimestamp,
	insertViewerConnectionEvent:        byTenantAndTimestamp,
	insertTrackListEvent:               byTenantAndTimestamp,
	insertClientQOESample:              byTenantAndTimestamp,
	insertPlayerBootSample:             byTenantAndTimestamp,
	insertNodeMetricsSample:            byTenantAndTimestamp,
	insertStorageSnapshot:              byTenantAndTimestamp,
	insertStorageEvent:                 byTenantAndTimestamp,
	insertFederationEvent:              byTenantAndTimestamp,
	insertProcessingEvent:              byTenantAndTimestamp,
	insertAPIRequest:                   byTenantAndTimestamp,
	insertAPIEvent:                     byTenantAndTimestamp,
	insertTenantAcquisitionEvent:       byTenantAndTimestamp,
	insertClipArtifactEvent:            byTenantAndTimestamp,
	insertDVRArtifactEvent:             byTenantAndTimestamp,
	insertVODArtifactEvent:             byTenantAndTimestamp,
	insertDomainArtifactEvent:          byTenantAndTimestamp,
	insertOrchestratorDiscoverySample:  byTenantAndTimestamp,
	insertOrchestratorTranscodeOutcome: byTenantAndTimestamp,
	insertOrchestratorAIOutcome:        byTenantAndTimestamp,
	// ingest_errors stores tenant_id as text, so only received_at bounds it.
	insertIngestError: {timeColumn: "received_at", timeField: "ReceivedAt", scope: rowScopeOf},
	// Finalized and anomalous facts carry the projection time of the delivery
	// that wrote them, which a redelivery does not repeat; tenant_id leads
	// their sort key.
	insertViewerSessionFinal:       byTenant,
	insertRestreamSessionFinal:     byTenant,
	insertStreamSessionFinal:       byTenant,
	insertProcessingSegmentFinal:   byTenant,
	insertRestreamSessionAnomalous: byTenant,
	insertViewerSessionAnomalous:   byTenant,
	insertStreamSessionAnomalous:   byTenant,
	// projection_divergences has no tenant column and records its observation
	// time; the ingest_id skip index alone bounds the lookup.
	insertProjectionDivergence: {scope: rowScopeOf},
}

var (
	byTenantAndTimestamp = &statementIdentity{tenant: true, timeColumn: "timestamp", timeField: "Timestamp", scope: rowScopeOf}
	byTenant             = &statementIdentity{tenant: true, scope: rowScopeOf}
)

// rowScopeOf reads a typed row's lookup bounds: its TenantID when the
// statement is bounded by tenant, and the field named by timeField.
func rowScopeOf(id *statementIdentity, row any) rowScope {
	v := reflect.ValueOf(row)
	if v.Kind() != reflect.Struct {
		return rowScope{}
	}
	var s rowScope
	if id.tenant {
		if f := v.FieldByName("TenantID"); f.IsValid() {
			if tenant, ok := f.Interface().(uuid.UUID); ok {
				s.tenant = tenant
			}
		}
	}
	if id.timeField != "" {
		if f := v.FieldByName(id.timeField); f.IsValid() {
			if at, ok := f.Interface().(time.Time); ok {
				s.at = at
			}
		}
	}
	return s
}
