package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestServiceHealthSummarySnapshot(t *testing.T) {
	summary := newServiceHealthSummary()
	summary.recordResult("bridge", "healthy")
	summary.recordResult("bridge", "unhealthy")
	summary.recordResult("steward", "healthy")
	summary.recordSkipped("skipper")

	byService, healthyServices, unhealthyServices, skippedServices := summary.snapshot()

	if got := byService["bridge"]; got != (serviceHealthCounts{Checked: 2, Healthy: 1, Unhealthy: 1}) {
		t.Fatalf("bridge counts = %+v", got)
	}
	if got := byService["steward"]; got != (serviceHealthCounts{Checked: 1, Healthy: 1}) {
		t.Fatalf("steward counts = %+v", got)
	}
	if got := byService["skipper"]; got != (serviceHealthCounts{Skipped: 1}) {
		t.Fatalf("skipper counts = %+v", got)
	}
	if !reflect.DeepEqual(healthyServices, []string{"bridge", "steward"}) {
		t.Fatalf("healthy services = %v", healthyServices)
	}
	if !reflect.DeepEqual(unhealthyServices, []string{"bridge"}) {
		t.Fatalf("unhealthy services = %v", unhealthyServices)
	}
	if !reflect.DeepEqual(skippedServices, []string{"skipper"}) {
		t.Fatalf("skipped services = %v", skippedServices)
	}
}

func TestApplyServiceDefinitionFallbackUsesCanonicalHealthMetadata(t *testing.T) {
	inst := serviceInstance{serviceID: "vmauth"}

	applyServiceDefinitionFallback(&inst)

	if inst.defaultProto != "http" {
		t.Fatalf("defaultProto = %q, want http", inst.defaultProto)
	}
	if inst.path != "/health" {
		t.Fatalf("path = %q, want /health", inst.path)
	}
	if inst.port != 8427 {
		t.Fatalf("port = %d, want 8427", inst.port)
	}
}

// TestApplyServiceDefinitionFallbackUsesFleetReadinessPath pins the poller's fallback to the fleet-wide readiness path.
// Chandler serves /ready in every supported release, so it is probed there. A Go service whose /ready arrived in a
// release that may not be running everywhere yet (servicedefs.ReadySince) stays on /health: the poller cannot see
// which release an instance runs, and probing /ready on an older binary would mark it unhealthy.
func TestApplyServiceDefinitionFallbackUsesFleetReadinessPath(t *testing.T) {
	chandler := serviceInstance{serviceID: "chandler"}
	applyServiceDefinitionFallback(&chandler)
	if chandler.path != "/ready" {
		t.Fatalf("chandler fallback path = %q, want /ready", chandler.path)
	}

	bridge := serviceInstance{serviceID: "bridge"}
	applyServiceDefinitionFallback(&bridge)
	def, _ := servicedefs.Lookup("bridge")
	if bridge.path != def.ReadinessPath() {
		t.Fatalf("bridge fallback path = %q, want the fleet readiness path %q", bridge.path, def.ReadinessPath())
	}
	if def.ReadySince != "" && bridge.path != "/health" {
		t.Fatalf("bridge fallback path = %q while ReadySince=%s, want /health", bridge.path, def.ReadySince)
	}

	registered := serviceInstance{serviceID: "bridge", path: "/ready"}
	applyServiceDefinitionFallback(&registered)
	if registered.path != "/ready" {
		t.Fatalf("a registered path must win over the fallback; got %q", registered.path)
	}
}

func TestHTTPHealthURL(t *testing.T) {
	tests := []struct {
		name    string
		inst    serviceInstance
		want    string
		wantErr bool
	}{
		{name: "relative registry endpoint", inst: serviceInstance{host: "10.88.0.4", port: 8935, path: "/healthz"}, want: "http://10.88.0.4:8935/healthz"},
		{name: "absolute public endpoint", inst: serviceInstance{host: "10.88.0.4", port: 8935, path: "https://livepeer-gateway.core-eu-1.infra.example.com/healthz"}, want: "https://livepeer-gateway.core-eu-1.infra.example.com/healthz"},
		{name: "reject non-http absolute endpoint", inst: serviceInstance{path: "file:///etc/passwd"}, wantErr: true},
		{name: "reject malformed relative endpoint", inst: serviceInstance{path: "healthz"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := httpHealthURL(tt.inst)
			if (err != nil) != tt.wantErr {
				t.Fatalf("httpHealthURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("httpHealthURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPollOnceRetriesSchemaVersionMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger())

	rows := sqlmock.NewRows([]string{
		"instance_id", "service_id", "cluster_id", "protocol", "advertise_host", "port",
		"path", "last_health_check", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	})
	mock.ExpectQuery("SELECT si.instance_id, si.service_id").
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 121, got 120"})
	mock.ExpectQuery("SELECT si.instance_id, si.service_id").
		WillReturnRows(rows)

	if err := pollOnce(&http.Client{Timeout: time.Millisecond}, make(chan struct{}, 1), 10, 0, newProbeSchedule(), func(string) HealthWatchTLS { return HealthWatchTLS{} }); err != nil {
		t.Fatalf("pollOnce returned error after retry: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPollOnceExcludesFoghornOwnedEdgeServices(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if expectedSQL != "poller excludes edge services" {
			return nil
		}
		if !strings.Contains(actualSQL, "s.type <> 'edge'") {
			return fmt.Errorf("poller query does not exclude aggregate edge service: %s", actualSQL)
		}
		if !strings.Contains(actualSQL, "s.type NOT LIKE 'edge-%'") {
			return fmt.Errorf("poller query does not exclude edge capability services: %s", actualSQL)
		}
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger())

	rows := sqlmock.NewRows([]string{
		"instance_id", "service_id", "cluster_id", "protocol", "advertise_host", "port",
		"path", "last_health_check", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	})
	mock.ExpectQuery("poller excludes edge services").WillReturnRows(rows)

	if err := pollOnce(&http.Client{Timeout: time.Millisecond}, make(chan struct{}, 1), 10, 0, newProbeSchedule(), func(string) HealthWatchTLS { return HealthWatchTLS{} }); err != nil {
		t.Fatalf("pollOnce returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type fixedLeader struct{ leading bool }

func (f *fixedLeader) lead() bool { return f.leading }

type scriptedElection struct {
	answers []bool
	err     error
}

func (s *scriptedElection) Lead(context.Context) (bool, error) {
	next := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}
	return next, s.err
}

// A replica that lost the election ends its gRPC health watches and opens
// none, so only the leader writes health verdicts.
func TestGrpcWatchesRunOnlyOnTheElectedReplica(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger())

	watchCancelled := false
	m := &grpcWatchManager{
		active:  map[string]context.CancelFunc{"inst-1": func() { watchCancelled = true }},
		backoff: map[string]time.Time{},
		tls:     func(string) HealthWatchTLS { return HealthWatchTLS{} },
	}
	if err := m.refreshIfLeader(&fixedLeader{leading: false}, time.Second, time.Second, make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if !watchCancelled || len(m.active) != 0 {
		t.Fatalf("follower kept its watches: cancelled=%v active=%d", watchCancelled, len(m.active))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("follower queried watch candidates: %v", err)
	}

	mock.ExpectQuery("instance_id").WillReturnRows(sqlmock.NewRows([]string{
		"instance_id", "service_id", "protocol", "advertise_host", "port", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	}))
	if err := m.refreshIfLeader(&fixedLeader{leading: true}, time.Second, time.Second, make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("leader did not refresh its watches: %v", err)
	}
}

func TestPollerLeadershipFollowsTheElection(t *testing.T) {
	Init(nil, logging.NewLogger())
	election := &scriptedElection{answers: []bool{false, true, true, false}}
	p := &pollerLeadership{lock: election}
	var got []bool
	for range 4 {
		got = append(got, p.lead())
	}
	if fmt.Sprint(got) != "[false true true false]" {
		t.Fatalf("lead() = %v, want the election's answers", got)
	}
	failing := &pollerLeadership{lock: &scriptedElection{answers: []bool{false}, err: fmt.Errorf("db down")}}
	if failing.lead() {
		t.Fatal("an election that could not be checked must not lead")
	}
}

func TestApplyServiceDefinitionFallbackDoesNotOverrideInstanceProtocol(t *testing.T) {
	inst := serviceInstance{serviceID: "foghorn", proto: "grpc", port: 18029}

	applyServiceDefinitionFallback(&inst)

	if inst.proto != "grpc" {
		t.Fatalf("proto = %q, want grpc", inst.proto)
	}
	if inst.port != 18029 {
		t.Fatalf("port = %d, want 18029", inst.port)
	}
}

func TestGrpcHealthServerNameDefaultsToServiceInternalWithCA(t *testing.T) {
	if got := grpcHealthServerName(serviceInstance{serviceID: "decklog"}, "/etc/frameworks/pki/ca.crt", ""); got != "decklog.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestGrpcHealthServerNameHonorsExplicitValue(t *testing.T) {
	if got := grpcHealthServerName(serviceInstance{serviceID: "decklog"}, "/etc/frameworks/pki/ca.crt", "custom.internal"); got != "custom.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestGrpcHealthTLSConfigUsesInternalNameForFoghornControlPort(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-eu-1",
		assignedBaseURL:   "https://frameworks.network",
		port:              18029,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}

func TestGrpcHealthTLSConfigHonorsFoghornExplicitServerName(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-us-1",
		assignedBaseURL:   "frameworks.network",
		port:              18029,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "foghorn.internal")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}

func TestGrpcHealthTLSConfigUsesInternalNameForFoghornInternalPort(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-eu-1",
		assignedBaseURL:   "https://frameworks.network",
		port:              18019,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}

// A healthy instance that stops answering is logged once at Warn with the URL
// probed and the failure, its recovery once at Info, and polls that do not
// change the status log neither.
func TestHealthProbeLogsEachTransitionOnce(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = mockDB.Close() }()
	log, hook := logrustest.NewNullLogger()
	Init(mockDB, log)

	var serverStatus atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(serverStatus.Load()))
	}))
	defer server.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	inst := serviceInstance{id: "inst-br-1", serviceID: "bridge", proto: "http", host: host, port: port, path: "/health"}
	probeURL := fmt.Sprintf("http://%s:%d/health", host, port)
	probe := healthProbe{client: &http.Client{Timeout: time.Second}}

	poll := func(previous string, code int) {
		t.Helper()
		serverStatus.Store(int32(code))
		mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).
			WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow(previous, "bridge"))
		probe.checkAndPersist(inst, "http")
	}
	transitions := func() []*logrus.Entry {
		var out []*logrus.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Service instance became unhealthy" || entry.Message == "Service instance recovered" {
				out = append(out, entry)
			}
		}
		return out
	}

	poll("healthy", http.StatusServiceUnavailable)
	poll("unhealthy", http.StatusServiceUnavailable)
	poll("unhealthy", http.StatusOK)
	poll("healthy", http.StatusOK)

	logged := transitions()
	if len(logged) != 2 {
		t.Fatalf("transition logs = %d, want 2 (one per transition)", len(logged))
	}
	down, up := logged[0], logged[1]
	if down.Level != logrus.WarnLevel || down.Data["probe"] != probeURL {
		t.Fatalf("unhealthy transition = %s %v, want Warn with probe %s", down.Level, down.Data, probeURL)
	}
	if cause, ok := down.Data[logrus.ErrorKey].(error); !ok || !strings.Contains(cause.Error(), "returned 503") {
		t.Fatalf("unhealthy transition error = %v, want the 503 status", down.Data[logrus.ErrorKey])
	}
	if up.Level != logrus.InfoLevel || up.Message != "Service instance recovered" || up.Data["probe"] != probeURL {
		t.Fatalf("recovery = %s %q %v, want Info recovery with the probe URL", up.Level, up.Message, up.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// A health transition on a pool-assigned/physical instance must wake Navigator
// (passing the instance so served clusters can be resolved); an unchanged status or
// a non-pool service must not.
func TestPersistHealthStatusWakesPoolServiceOnTransition(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())

	var wakes []string
	SetPoolDNSWake(func(instanceID, serviceType string) {
		wakes = append(wakes, instanceID+"/"+serviceType)
	})
	defer SetPoolDNSWake(nil)

	expectQuery := func(oldStatus, serviceType, newStatus, instanceID string) {
		mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).
			WithArgs(instanceID, newStatus, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).
				AddRow(oldStatus, serviceType))
	}

	// Transition on a physical-endpoint service: wake fires with the instance.
	expectQuery("healthy", "livepeer-gateway", "unhealthy", "inst-gw-1")
	if err := persistHealthStatus(context.Background(), "inst-gw-1", "unhealthy"); err != nil {
		t.Fatalf("persistHealthStatus: %v", err)
	}
	if len(wakes) != 1 || wakes[0] != "inst-gw-1/livepeer-gateway" {
		t.Fatalf("expected one wake for inst-gw-1/livepeer-gateway, got %v", wakes)
	}

	// Unchanged status: no additional wake (avoids spamming Navigator every poll).
	expectQuery("healthy", "livepeer-gateway", "healthy", "inst-gw-1")
	if err := persistHealthStatus(context.Background(), "inst-gw-1", "healthy"); err != nil {
		t.Fatalf("persistHealthStatus: %v", err)
	}
	if len(wakes) != 1 {
		t.Fatalf("expected no wake on unchanged status, got %v", wakes)
	}

	// foghorn is pool-assigned too (pooled DNS keyed by served cluster), so its
	// transition also wakes.
	expectQuery("healthy", "foghorn", "unhealthy", "inst-fh-1")
	if err := persistHealthStatus(context.Background(), "inst-fh-1", "unhealthy"); err != nil {
		t.Fatalf("persistHealthStatus: %v", err)
	}
	if len(wakes) != 2 || wakes[1] != "inst-fh-1/foghorn" {
		t.Fatalf("expected a wake for inst-fh-1/foghorn, got %v", wakes)
	}

	// A non-pool, non-physical service (bridge) must not wake.
	expectQuery("healthy", "bridge", "unhealthy", "inst-br-1")
	if err := persistHealthStatus(context.Background(), "inst-br-1", "unhealthy"); err != nil {
		t.Fatalf("persistHealthStatus: %v", err)
	}
	if len(wakes) != 2 {
		t.Fatalf("expected no wake for a non-pool service, got %v", wakes)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// Every status change that wakes Navigator is logged with the instance and both
// statuses, including changes the probe transition log does not cover
// (unknown to healthy, healthy to skipped). Production 2026-10-08: DNS wakes
// arrived in bursts whose cause the logs could not show.
func TestPersistHealthTransitionLogsEveryDNSWake(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = mockDB.Close() }()
	log, hook := logrustest.NewNullLogger()
	Init(mockDB, log)

	wakes := 0
	SetPoolDNSWake(func(instanceID, serviceType string) { wakes++ })
	defer SetPoolDNSWake(nil)

	transitions := [][2]string{{"unknown", "healthy"}, {"healthy", "skipped"}, {"skipped", "unhealthy"}, {"unhealthy", "unhealthy"}}
	for _, tr := range transitions {
		mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).
			WithArgs("inst-fh-1", tr[1], sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow(tr[0], "foghorn"))
		if _, err := persistHealthTransition(context.Background(), "inst-fh-1", tr[1]); err != nil {
			t.Fatalf("persistHealthTransition(%v): %v", tr, err)
		}
	}

	var logged [][2]string
	for _, entry := range hook.AllEntries() {
		if entry.Message != "Service health transition woke Navigator DNS" {
			continue
		}
		if entry.Data["instance_id"] != "inst-fh-1" || entry.Data["service_type"] != "foghorn" {
			t.Fatalf("wake log fields = %v, want the instance and its service type", entry.Data)
		}
		logged = append(logged, [2]string{fmt.Sprint(entry.Data["previous_status"]), fmt.Sprint(entry.Data["status"])})
	}
	if wakes != 3 || !reflect.DeepEqual(logged, transitions[:3]) {
		t.Fatalf("wakes = %d, logged transitions = %v, want one log per wake %v", wakes, logged, transitions[:3])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// An unchanged verdict no longer moves last_health_check, so the schedule
// alone decides which instances are probed. Every instance must still be
// probed once per minAge however small the batch, and none sooner.
func TestProbeScheduleProbesEveryInstanceEachMinAge(t *testing.T) {
	schedule := newProbeSchedule()
	candidates := []serviceInstance{{id: "a"}, {id: "b"}, {id: "c"}}
	ids := func(list []serviceInstance) string {
		out := make([]string, 0, len(list))
		for _, inst := range list {
			out = append(out, inst.id)
		}
		return strings.Join(out, ",")
	}
	start := time.Unix(1_700_000_000, 0)
	const minAge = 30 * time.Second

	if got := ids(schedule.due(candidates, minAge, 2, start)); got != "a,b" {
		t.Fatalf("first round = %s, want a,b", got)
	}
	if got := ids(schedule.due(candidates, minAge, 2, start.Add(time.Second))); got != "c" {
		t.Fatalf("second round = %s, want only the unprobed c", got)
	}
	if got := ids(schedule.due(candidates, minAge, 2, start.Add(2*time.Second))); got != "" {
		t.Fatalf("round inside minAge = %s, want none", got)
	}
	if got := ids(schedule.due(candidates, minAge, 2, start.Add(minAge+time.Second))); got != "a,b" {
		t.Fatalf("round after minAge = %s, want the longest-unprobed a,b", got)
	}
	if got := ids(schedule.due(candidates, minAge, 2, start.Add(minAge+2*time.Second))); got != "c" {
		t.Fatalf("next round = %s, want c", got)
	}

	if got := ids(schedule.due([]serviceInstance{{id: "a"}}, minAge, 2, start.Add(3*minAge))); got != "a" {
		t.Fatalf("round with b and c gone = %s, want a", got)
	}
	if _, kept := schedule.probed["b"]; kept {
		t.Fatal("schedule kept an instance that is no longer a candidate")
	}
}

// Writing an unchanged verdict only once its check is refreshAfter old must
// keep the stored check younger than the DNS stale bound with a full probe
// gap to spare, at the slowest probe cadence the poller allows. A stale bound
// too tight for that writes every verdict, exactly as often as it is probed.
func TestHealthRefreshKeepsDNSFreshWithAProbeGapToSpare(t *testing.T) {
	for _, tc := range []struct {
		staleSeconds, intervalSeconds, minAgeSeconds int
	}{
		{300, 30, 30}, // code defaults
		{90, 30, 30},  // config/env/base.env
		{300, 30, 60},
		{600, 30, 30},
	} {
		interval := time.Duration(tc.intervalSeconds) * time.Second
		minAge := time.Duration(tc.minAgeSeconds) * time.Second
		probeGap := minAge + interval + interval/4
		stale := time.Duration(tc.staleSeconds) * time.Second
		refreshAfter := time.Duration(quartermasterdb.HealthRefreshAfterSeconds(tc.staleSeconds, probeGap)) * time.Second

		var now, written, oldest time.Duration
		for now < time.Hour {
			now += probeGap
			age := now - written
			if age > oldest {
				oldest = age
			}
			if age >= refreshAfter {
				written = now
			}
		}
		switch {
		case refreshAfter == 0 && oldest != probeGap:
			t.Fatalf("stale=%s leaves no room to skip writes, yet the stored check reaches %s instead of one %s probe gap",
				stale, oldest, probeGap)
		case refreshAfter > 0 && oldest+probeGap > stale:
			t.Fatalf("stale=%s interval=%s minAge=%s: stored check reaches %s, leaving less than a %s probe gap before %s",
				stale, interval, minAge, oldest, probeGap, stale)
		}
		if tc.staleSeconds == 300 && tc.minAgeSeconds == 30 && refreshAfter < 2*probeGap {
			t.Fatalf("default config refreshes after %s; want unchanged rows left alone for at least two probe gaps", refreshAfter)
		}
	}
}

// The poller passes the configured refresh bound with every verdict, so the
// statement can leave an unchanged, still-fresh row alone.
func TestPersistHealthTransitionPassesTheRefreshBound(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())
	previous := healthRefreshAfterSeconds.Load()
	defer healthRefreshAfterSeconds.Store(previous)
	healthRefreshAfterSeconds.Store(165)

	mock.ExpectQuery(`(?s)UPDATE quartermaster\.service_instances.*si\.health_status IS DISTINCT FROM`).
		WithArgs("inst-br-1", "healthy", int32(165)).
		WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow("healthy", "bridge"))
	if _, err := persistHealthTransition(context.Background(), "inst-br-1", "healthy"); err != nil {
		t.Fatalf("persistHealthTransition: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
