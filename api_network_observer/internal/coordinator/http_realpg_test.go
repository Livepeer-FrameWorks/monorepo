//go:build schema_verify

package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func startObserverPostgres(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-network-observer-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	baseline, err := dbsql.Content.ReadFile("schema/network_observer.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(baseline)); err != nil {
		t.Fatal(err)
	}
	return db
}

type observerTestSender func(context.Context, *pb.GatewayTelemetryEvent) error

func (send observerTestSender) SendGatewayTelemetryContext(ctx context.Context, event *pb.GatewayTelemetryEvent) error {
	return send(ctx, event)
}

func TestObserverReplayRollbackAndRestart_RealPG(t *testing.T) {
	verifyObserverReplayRollbackAndRestart(t, startObserverPostgres(t))
}

func TestObserverReplayRollbackAndRestart_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "network_observer", "network_observer")
	if !ok {
		t.Fatal("run through the shared Yugabyte contract fixture")
	}
	verifyObserverReplayRollbackAndRestart(t, db)
}

func verifyObserverReplayRollbackAndRestart(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	newAPI := func(tenant string) *API {
		return &API{Store: &store.Store{DB: db, TenantID: tenant}, Engine: Engine{Network: "42161", Environment: "staging"}, Secret: "test-observer-secret", ClusterID: "test-cluster", MaxObservers: 8}
	}
	api, otherAPI := newAPI(tenant), newAPI(otherTenant)
	resource := obs.Resource{Provider: "livepeer", Network: "42161", Environment: "staging", ID: "0xabc", URI: "https://orch.example:8935", Active: true, CatalogSeen: time.Now().UTC()}
	for _, current := range []*API{api, otherAPI} {
		if err := current.Store.Update(ctx, func(state *store.State, _ *sql.Tx) error {
			state.Resources[resource.Key()] = resource
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	request := func(current *API, method, path string, body []byte, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+current.Secret)
		w := httptest.NewRecorder()
		current.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status = %d (%s), want %d", method, path, w.Code, w.Body, want)
		}
		return w.Body.Bytes()
	}
	marshal := func(batch obs.Batch) []byte {
		t.Helper()
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	getWork := func(current *API) obs.Work {
		t.Helper()
		var work obs.Work
		if err := json.Unmarshal(request(current, "GET", "/v1/work?observer_id=eu&region=eu-west", nil, 200), &work); err != nil {
			t.Fatal(err)
		}
		return work
	}
	count := func(table, scope string) int {
		t.Helper()
		var total int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM network_observer."+table+" WHERE tenant_id=$1", scope).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return total
	}
	dns := obs.Batch{ID: uuid.NewString(), ObserverID: "eu", DNS: []obs.DNSResult{{Resource: resource, IPs: []string{"192.0.2.1", "192.0.2.2"}, ObservedAt: time.Now().UTC()}}}
	for _, current := range []*API{api, otherAPI} {
		getWork(current)
		request(current, "POST", "/v1/observations", marshal(dns), 200)
	}
	work := getWork(api)
	if len(work.Assignments) != 2 {
		t.Fatalf("DNS union assignments = %d, want 2", len(work.Assignments))
	}
	assignment := work.Assignments[0]
	batch := obs.Batch{ID: uuid.NewString(), ObserverID: "eu", Results: []obs.ProbeResult{{ID: uuid.NewString(), AssignmentID: assignment.ID, Assignment: &assignment, EndpointID: assignment.Endpoint.ID, Check: obs.Check{ObserverID: "eu", ObservedAt: time.Now().UTC(), ActualIP: assignment.Endpoint.IP, Success: true, Qualifying: true, LatencyMS: 25}}}}
	raw := marshal(batch)
	request(api, "POST", "/v1/observations", raw, 200)
	api = newAPI(tenant)
	request(api, "POST", "/v1/observations", raw, 200)
	var inventory struct {
		Observers []obs.Observer `json:"observers"`
	}
	if err := json.Unmarshal(request(api, "GET", "/v1/inventory", nil, 200), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Observers) != 1 || inventory.Observers[0].ID != "eu" || inventory.Observers[0].Kind != "prober" || inventory.Observers[0].Region != "eu-west" || inventory.Observers[0].LastSeen.IsZero() {
		t.Fatalf("observer measurement site lost across restart: %+v", inventory.Observers)
	}
	if count("telemetry_outbox", tenant) != 2 || count("ingest_receipts", tenant) != 2 || count("telemetry_outbox", otherTenant) != 0 {
		t.Fatal("restart replay duplicated telemetry or crossed tenant scope")
	}
	conflict := batch
	conflict.ObserverID = "different"
	request(api, "POST", "/v1/observations", marshal(conflict), 409)

	batch.ID = uuid.NewString()
	batch.Results[0].ID = uuid.NewString()
	batch.DNS = []obs.DNSResult{{Resource: resource, IPs: []string{"192.0.2.3"}, ObservedAt: time.Now().UTC()}}
	constraint := "ALTER TABLE network_observer.telemetry_outbox ADD CONSTRAINT injected_outbox_failure CHECK (id <> '" + batch.Results[0].ID + "'::uuid)"
	if _, err := db.ExecContext(ctx, constraint); err != nil {
		t.Fatal(err)
	}
	request(api, "POST", "/v1/observations", marshal(batch), 503)
	if count("endpoints", tenant) != 2 || count("ingest_receipts", tenant) != 2 || count("telemetry_outbox", tenant) != 2 {
		t.Fatal("failed outbox write committed partial inventory or a replay receipt")
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE network_observer.telemetry_outbox DROP CONSTRAINT injected_outbox_failure"); err != nil {
		t.Fatal(err)
	}
	request(api, "POST", "/v1/observations", marshal(batch), 200)
	if count("endpoints", tenant) != 3 || count("telemetry_outbox", tenant) != 4 || count("endpoints", otherTenant) != 2 {
		t.Fatal("retry failed to commit the complete batch within its tenant")
	}
	outage := errors.New("Decklog unavailable")
	if err := api.Drain(ctx, observerTestSender(func(context.Context, *pb.GatewayTelemetryEvent) error { return outage })); !errors.Is(err, outage) {
		t.Fatalf("drain during outage = %v", err)
	}
	if count("telemetry_outbox", tenant) != 4 {
		t.Fatal("failed delivery removed durable telemetry")
	}
	if _, err := db.ExecContext(ctx, "UPDATE network_observer.telemetry_outbox SET next_attempt_at=now() WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	api = newAPI(tenant)
	seen := map[string]bool{}
	sender := observerTestSender(func(_ context.Context, event *pb.GatewayTelemetryEvent) error {
		if event.ClusterOwnerTenantId != tenant || event.NetworkId != "42161" || event.Environment != "staging" || seen[event.EventId] {
			t.Fatalf("invalid or duplicated delivered observation: %s", event.EventId)
		}
		seen[event.EventId] = true
		return nil
	})
	for range 4 {
		if err := api.Drain(ctx, sender); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.Drain(ctx, sender); !errors.Is(err, sql.ErrNoRows) || len(seen) != 4 {
		t.Fatalf("final drain = %v, delivered IDs = %d", err, len(seen))
	}

	// A reachable database alone is not readiness: the collector must have
	// refreshed its catalog and be consuming its consumer group.
	started := time.Now().UTC()
	api.Readiness = NewReadiness(15*time.Minute, started)
	var report ReadinessReport
	if err := json.Unmarshal(request(api, "GET", "/ready", nil, 503), &report); err != nil || report.Ready || len(report.Reasons) != 2 {
		t.Fatalf("collector without a catalog or consumer group reported %+v (%v)", report, err)
	}
	api.Readiness.CatalogRefreshed(started, nil)
	api.Readiness.KafkaSampled(started, KafkaSample{Joined: true, Partitions: 1})
	if err := json.Unmarshal(request(api, "GET", "/ready", nil, 200), &report); err != nil || !report.Ready || report.Catalog.LastSuccess == nil {
		t.Fatalf("working collector reported %+v (%v)", report, err)
	}
}
