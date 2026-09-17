//go:build schema_verify

package ownership

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/lookouttest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
)

const (
	ownerA = "10000000-0000-0000-0000-00000000000a"
	ownerB = "10000000-0000-0000-0000-00000000000b"
)

var unrestricted = incidents.Access{Unrestricted: true}

// ownerBook models Quartermaster: the registered clusters with their owner and
// updated_at, plus a number of lookups that fail as if Quartermaster were
// unavailable. An unregistered cluster is NotFound.
type ownerBook struct {
	mu          sync.Mutex
	owners      map[string]incidents.ClusterOwner
	clock       time.Time
	failLookups int
	lookupCalls int
}

func (b *ownerBook) setOwner(clusterID string, scope incidents.Scope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clock = b.clock.Add(time.Minute)
	b.owners[clusterID] = incidents.ClusterOwner{Scope: scope, UpdatedAt: b.clock}
}

func (b *ownerBook) failNext(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failLookups = n
}

func (b *ownerBook) lookups() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lookupCalls
}

func (b *ownerBook) LookupOwner(_ context.Context, clusterID string) (incidents.ClusterOwner, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lookupCalls++
	if b.failLookups > 0 {
		b.failLookups--
		return incidents.ClusterOwner{}, errors.New("quartermaster unavailable")
	}
	if owner, ok := b.owners[clusterID]; ok {
		return owner, nil
	}
	return incidents.ClusterOwner{Scope: incidents.Scope{Kind: incidents.ScopePlatform}}, nil
}

type recordingRealtime struct {
	mu     sync.Mutex
	events []*ipcpb.ServiceEvent
}

func (r *recordingRealtime) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingRealtime) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// recipients lists tenant events as "<tenant>:<change>" and operator events,
// which must be tenantless, as "operator/<incident owner>:<change>".
func (r *recordingRealtime) recipients() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, event := range r.events {
		audience := event.GetTenantId()
		if event.GetEventType() == serviceevents.PlatformIncidentUpdated {
			audience = "operator/" + event.GetIncidentEvent().GetTenantId()
			if event.GetTenantId() != "" {
				audience = "operator-with-tenant/" + event.GetTenantId()
			}
		}
		out = append(out, audience+":"+event.GetIncidentEvent().GetChange())
	}
	return out
}

func newOwnershipFixture(t *testing.T, db *sql.DB) (*incidents.Service, *ownerBook, *recordingRealtime, *Consumer) {
	t.Helper()
	book := &ownerBook{
		owners: map[string]incidents.ClusterOwner{},
		clock:  time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC),
	}
	realtime := &recordingRealtime{}
	svc := &incidents.Service{DB: db, Owners: book, Realtime: realtime}
	consumer := &Consumer{
		Reconciler: svc,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	return svc, book, realtime, consumer
}

func openIncident(t *testing.T, svc *incidents.Service, groupKey, clusterID, fingerprint string) string {
	t.Helper()
	result, err := svc.IngestAlertmanager(context.Background(), incidents.AlertmanagerWebhook{
		Version:     "4",
		GroupKey:    groupKey,
		Status:      "firing",
		Receiver:    "lookout",
		GroupLabels: map[string]string{"alertname": "EdgeDown", "cluster": clusterID, "region": "eu-west"},
		Alerts: []incidents.AlertmanagerAlert{{
			Status:      "firing",
			Labels:      map[string]string{"alertname": "EdgeDown", "severity": "warning"},
			StartsAt:    time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC),
			Fingerprint: fingerprint,
		}},
	})
	if err != nil {
		t.Fatalf("ingest %s: %v", groupKey, err)
	}
	if result.Outcome != incidents.IngestCreated {
		t.Fatalf("ingest %s outcome = %q, want created", groupKey, result.Outcome)
	}
	return result.IncidentID
}

func requireTenant(t *testing.T, svc *incidents.Service, id, wantScope, wantTenant string) {
	t.Helper()
	detail, err := svc.Get(context.Background(), unrestricted, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	inc := detail.Incident
	if inc.Scope != wantScope || inc.TenantID.String != wantTenant || inc.TenantID.Valid != (wantTenant != "") {
		t.Fatalf("incident %s scope=%s tenant=%v, want %s/%q", id, inc.Scope, inc.TenantID, wantScope, wantTenant)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func clusterUpdated(t *testing.T, clusterID string) kafka.Message {
	t.Helper()
	return clusterEvent(t, "cluster_updated", clusterID)
}

func clusterEvent(t *testing.T, eventType, clusterID string) kafka.Message {
	t.Helper()
	return serviceEventMessage(t, kafka.ServiceEvent{
		EventID:      eventType + "-" + clusterID,
		EventType:    eventType,
		Source:       "quartermaster",
		ResourceType: "cluster",
		ResourceID:   clusterID,
		Data:         map[string]any{"cluster_id": clusterID},
	})
}

func TestLookoutClusterOwnershipEvent_RealPG(t *testing.T) {
	runClusterOwnershipEvent(t, lookouttest.StartPostgres(t))
}

func TestLookoutClusterOwnershipEvent_RealYugabyte(t *testing.T) {
	runClusterOwnershipEvent(t, lookouttest.StartYugabyte(t))
}

// runClusterOwnershipEvent moves a cluster from tenant A to tenant B and proves
// a cluster_updated event moves its open incident, leaves its resolved incident
// and other clusters alone, applies nothing on an unverified owner, and is
// idempotent on redelivery.
func runClusterOwnershipEvent(t *testing.T, db *sql.DB) {
	svc, book, realtime, consumer := newOwnershipFixture(t, db)
	ctx := context.Background()
	book.setOwner("moved-cluster", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerA})
	book.setOwner("other-cluster", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerA})

	openID := openIncident(t, svc, "moved-open", "moved-cluster", "f-open")
	resolvedID := openIncident(t, svc, "moved-resolved", "moved-cluster", "f-resolved")
	if _, err := svc.Resolve(ctx, unrestricted, resolvedID); err != nil {
		t.Fatal(err)
	}
	otherID := openIncident(t, svc, "other-open", "other-cluster", "f-other")

	realtime.reset()
	book.setOwner("moved-cluster", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerB})
	book.failNext(1)
	if err := consumer.Handle(ctx, clusterUpdated(t, "moved-cluster")); !errors.Is(err, incidents.ErrScopeUnverified) {
		t.Fatalf("handle with unverified owner = %v, want ErrScopeUnverified", err)
	}
	requireTenant(t, svc, openID, incidents.ScopeTenant, ownerA)

	if err := consumer.HandleUntilApplied(ctx, clusterUpdated(t, "moved-cluster")); err != nil {
		t.Fatalf("handle cluster_updated: %v", err)
	}
	requireTenant(t, svc, openID, incidents.ScopeTenant, ownerB)
	requireTenant(t, svc, resolvedID, incidents.ScopeTenant, ownerA)
	requireTenant(t, svc, otherID, incidents.ScopeTenant, ownerA)
	if _, err := svc.Get(ctx, incidents.Access{TenantID: ownerA}, openID); !errors.Is(err, incidents.ErrNotFound) {
		t.Fatalf("former owner get = %v, want ErrNotFound", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM lookout.notification_outbox WHERE incident_id = $1 AND channel = 'kafka' AND tenant_id = $2 AND delivered_at IS NULL AND failed_at IS NULL`, openID, ownerB); n != 1 {
		t.Fatalf("armed kafka publications for the new owner = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM lookout.notification_outbox WHERE incident_id = $1 AND tenant_id = $2`, openID, ownerA); n != 0 {
		t.Fatalf("outbox rows left for the former owner = %d, want 0", n)
	}
	var body string
	if err := db.QueryRow(`SELECT body::text FROM lookout.incident_events WHERE incident_id = $1 AND kind = 'scope_changed'`, openID).Scan(&body); err != nil {
		t.Fatalf("scope_changed timeline event: %v", err)
	}
	if strings.Contains(body, ownerA) || strings.Contains(body, ownerB) {
		t.Fatalf("scope_changed body %s names a tenant", body)
	}
	wantRealtime := []string{ownerA + ":scope_changed", ownerB + ":scope_changed", "operator/" + ownerB + ":scope_changed"}
	if got := realtime.recipients(); !reflect.DeepEqual(got, wantRealtime) {
		t.Fatalf("realtime recipients = %v, want %v", got, wantRealtime)
	}

	if err := consumer.HandleUntilApplied(ctx, clusterUpdated(t, "moved-cluster")); err != nil {
		t.Fatalf("redelivered cluster_updated: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM lookout.incident_events WHERE incident_id = $1 AND kind = 'scope_changed'`, openID); n != 1 {
		t.Fatalf("scope_changed events after redelivery = %d, want 1", n)
	}
	if got := realtime.recipients(); len(got) != len(wantRealtime) {
		t.Fatalf("realtime events after redelivery = %v, want no new events", got)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM lookout.incident_events WHERE incident_id IN ($1, $2) AND kind = 'scope_changed'`, resolvedID, otherID); n != 0 {
		t.Fatalf("scope_changed events on the resolved or other-cluster incident = %d, want 0", n)
	}
}

func TestLookoutStartupOwnershipReconcile_RealPG(t *testing.T) {
	runStartupOwnershipReconcile(t, lookouttest.StartPostgres(t))
}

func TestLookoutStartupOwnershipReconcile_RealYugabyte(t *testing.T) {
	runStartupOwnershipReconcile(t, lookouttest.StartYugabyte(t))
}

// runStartupOwnershipReconcile changes ownership without an event, as while no
// Lookout was consuming, and proves the startup reconcile retries past an
// unavailable Quartermaster and moves every stale open incident.
func runStartupOwnershipReconcile(t *testing.T, db *sql.DB) {
	svc, book, _, consumer := newOwnershipFixture(t, db)
	ctx := context.Background()
	book.setOwner("stale-to-platform", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerA})
	book.setOwner("stale-to-tenant", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerA})
	toPlatform := openIncident(t, svc, "stale-platform", "stale-to-platform", "s1")
	toTenant := openIncident(t, svc, "stale-tenant", "stale-to-tenant", "s2")
	resolved := openIncident(t, svc, "stale-resolved", "stale-to-tenant", "s3")
	if _, err := svc.Resolve(ctx, unrestricted, resolved); err != nil {
		t.Fatal(err)
	}

	book.setOwner("stale-to-platform", incidents.Scope{Kind: incidents.ScopePlatform})
	book.setOwner("stale-to-tenant", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerB})
	book.failNext(1)
	consumer.ReconcileAtStartup(ctx)

	requireTenant(t, svc, toPlatform, incidents.ScopePlatform, "")
	requireTenant(t, svc, toTenant, incidents.ScopeTenant, ownerB)
	requireTenant(t, svc, resolved, incidents.ScopeTenant, ownerA)
	// Two clusters opened with a lookup each; the startup pass failed once and
	// then verified both.
	if got := book.lookups(); got < 5 {
		t.Fatalf("lookups = %d, want a retry after the unavailable lookup", got)
	}

	moved, err := svc.ReconcileOpenIncidentScopes(ctx)
	if err != nil || moved != 0 {
		t.Fatalf("second reconcile = %d, %v; want idempotent no-op", moved, err)
	}
}

func TestLookoutClusterCreatedOwnership_RealPG(t *testing.T) {
	runClusterCreatedOwnership(t, lookouttest.StartPostgres(t))
}

func TestLookoutClusterCreatedOwnership_RealYugabyte(t *testing.T) {
	runClusterCreatedOwnership(t, lookouttest.StartYugabyte(t))
}

// runClusterCreatedOwnership opens an incident for a cluster Quartermaster does
// not know yet, which is verified platform scope, then registers the cluster
// for a tenant and proves its cluster_created event moves the open incident.
func runClusterCreatedOwnership(t *testing.T, db *sql.DB) {
	svc, book, _, consumer := newOwnershipFixture(t, db)
	ctx := context.Background()

	id := openIncident(t, svc, "early-alert", "new-cluster", "e1")
	requireTenant(t, svc, id, incidents.ScopePlatform, "")

	book.setOwner("new-cluster", incidents.Scope{Kind: incidents.ScopeTenant, TenantID: ownerA})
	if err := consumer.HandleUntilApplied(ctx, clusterEvent(t, "cluster_created", "new-cluster")); err != nil {
		t.Fatalf("handle cluster_created: %v", err)
	}
	requireTenant(t, svc, id, incidents.ScopeTenant, ownerA)
	if _, err := svc.Get(ctx, incidents.Access{TenantID: ownerA}, id); err != nil {
		t.Fatalf("owner get after cluster_created = %v", err)
	}
}
