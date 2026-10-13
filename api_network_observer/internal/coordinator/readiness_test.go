package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	_ "github.com/lib/pq"
	"github.com/twmb/franz-go/pkg/kadm"
)

const testCatalogInterval = 15 * time.Minute

var readinessStart = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func healthyKafka() KafkaSample { return KafkaSample{Joined: true, Partitions: 3, Committed: 100} }

// readyAt returns a Readiness whose catalog and consumer group were healthy
// at at.
func readyAt(at time.Time) *Readiness {
	r := NewReadiness(testCatalogInterval, readinessStart)
	r.CatalogRefreshed(at, nil)
	r.KafkaSampled(at, healthyKafka())
	return r
}

func assertReady(t *testing.T, r *Readiness, now time.Time) {
	t.Helper()
	if report := r.Check(now); !report.Ready || len(report.Reasons) != 0 {
		t.Fatalf("not ready at %s: %v", now.Sub(readinessStart), report.Reasons)
	}
}

func assertNotReady(t *testing.T, r *Readiness, now time.Time, reason string) {
	t.Helper()
	report := r.Check(now)
	if report.Ready {
		t.Fatalf("ready at %s, want a %q reason", now.Sub(readinessStart), reason)
	}
	for _, got := range report.Reasons {
		if strings.Contains(got, reason) {
			return
		}
	}
	t.Fatalf("reasons %v at %s lack %q", report.Reasons, now.Sub(readinessStart), reason)
}

func TestReadinessHealthyCollectorIsReady(t *testing.T) {
	r := readyAt(readinessStart.Add(time.Minute))
	assertReady(t, r, readinessStart.Add(2*time.Minute))
}

func TestReadinessCatalogNeverRefreshed(t *testing.T) {
	r := NewReadiness(testCatalogInterval, readinessStart)
	r.KafkaSampled(readinessStart.Add(10*time.Second), healthyKafka())
	assertNotReady(t, r, readinessStart.Add(time.Second), "catalog: waiting for the first contract catalog refresh")
	r.CatalogRefreshed(readinessStart.Add(time.Minute), errors.New("livepeer RPC network mismatch"))
	assertNotReady(t, r, readinessStart.Add(2*time.Minute), "livepeer RPC network mismatch")
	// Past one interval and one refresh timeout the first refresh and its retry have both failed.
	assertNotReady(t, r, readinessStart.Add(testCatalogInterval+CatalogRefreshTimeout+time.Second), "catalog: no successful contract catalog refresh since start: livepeer RPC network mismatch")
}

func TestReadinessCatalogStaleAfterTwoFailedRefreshes(t *testing.T) {
	success := readinessStart.Add(time.Minute)
	r := readyAt(success)
	// One failed refresh between two successful ones does not flip readiness.
	r.CatalogRefreshed(success.Add(testCatalogInterval), errors.New("dial tcp: i/o timeout"))
	keepKafka := func(at time.Time) { r.KafkaSampled(at, KafkaSample{Joined: true, Partitions: 3, Committed: 100}) }
	keepKafka(success.Add(testCatalogInterval))
	assertReady(t, r, success.Add(testCatalogInterval+time.Minute))
	r.CatalogRefreshed(success.Add(2*testCatalogInterval), errors.New("dial tcp: i/o timeout"))
	keepKafka(success.Add(2*testCatalogInterval + CatalogRefreshTimeout))
	assertReady(t, r, success.Add(2*testCatalogInterval+CatalogRefreshTimeout))
	assertNotReady(t, r, success.Add(2*testCatalogInterval+CatalogRefreshTimeout+time.Second), "catalog: no successful refresh since")
	r.CatalogRefreshed(success.Add(3*testCatalogInterval), nil)
	assertReady(t, r, success.Add(3*testCatalogInterval))
}

func TestReadinessKafkaConsumerStopped(t *testing.T) {
	r := readyAt(readinessStart.Add(time.Minute))
	r.KafkaStopped(errors.New("flush poll batch: outbox full"))
	assertNotReady(t, r, readinessStart.Add(2*time.Minute), "kafka: consumer stopped: flush poll batch: outbox full")
}

func TestReadinessKafkaNeverSampled(t *testing.T) {
	r := NewReadiness(testCatalogInterval, readinessStart)
	r.CatalogRefreshed(readinessStart.Add(time.Second), nil)
	r.KafkaSampled(readinessStart.Add(5*time.Second), KafkaSample{Err: errors.New("unable to dial: connection refused")})
	assertNotReady(t, r, readinessStart.Add(10*time.Second), "kafka: waiting for the first consumer group sample: unable to dial")
	assertNotReady(t, r, readinessStart.Add(testCatalogInterval+CatalogRefreshTimeout+time.Second), "kafka: consumer group never sampled since start: unable to dial")
}

func TestReadinessKafkaSamplesFailingBeyondWindow(t *testing.T) {
	sampled := readinessStart.Add(time.Minute)
	r := readyAt(sampled)
	r.KafkaSampled(sampled.Add(time.Minute), KafkaSample{Err: errors.New("broker unavailable")})
	assertReady(t, r, sampled.Add(2*testCatalogInterval))
	assertNotReady(t, r, sampled.Add(2*testCatalogInterval+time.Second), "kafka: consumer group not sampled since")
}

func TestReadinessKafkaWithoutAssignment(t *testing.T) {
	r := NewReadiness(testCatalogInterval, readinessStart)
	r.CatalogRefreshed(readinessStart.Add(time.Second), nil)
	r.KafkaSampled(readinessStart.Add(5*time.Second), KafkaSample{Joined: true})
	assertNotReady(t, r, readinessStart.Add(10*time.Second), "kafka: waiting to join the consumer group")
	r.KafkaSampled(readinessStart.Add(testCatalogInterval+CatalogRefreshTimeout), KafkaSample{Partitions: 3})
	assertNotReady(t, r, readinessStart.Add(testCatalogInterval+CatalogRefreshTimeout+time.Second), "kafka: never joined the consumer group")

	assigned := readinessStart.Add(time.Minute)
	r = readyAt(assigned)
	// A rebalance that briefly leaves no partition does not flip readiness.
	r.KafkaSampled(assigned.Add(time.Minute), KafkaSample{Joined: true})
	assertReady(t, r, assigned.Add(2*time.Minute))
	r.KafkaSampled(assigned.Add(2*testCatalogInterval+time.Second), KafkaSample{Joined: true})
	r.CatalogRefreshed(assigned.Add(2*testCatalogInterval), nil)
	assertNotReady(t, r, assigned.Add(2*testCatalogInterval+time.Second), "kafka: not a member of the consumer group with assigned partitions since")
}

func TestReadinessKafkaLagWithoutProgress(t *testing.T) {
	at := readinessStart.Add(time.Minute)
	r := readyAt(at)
	stuck := KafkaSample{Joined: true, Partitions: 3, Committed: 100, Lag: 40}
	r.KafkaSampled(at.Add(time.Minute), stuck)
	r.KafkaSampled(at.Add(2*testCatalogInterval), stuck)
	r.CatalogRefreshed(at.Add(2*testCatalogInterval), nil)
	assertReady(t, r, at.Add(2*testCatalogInterval))
	assertNotReady(t, r, at.Add(2*testCatalogInterval+time.Second), "kafka: lag of 40 records without committed progress since")
	// Advancing commits restore readiness at the next sample.
	r.KafkaSampled(at.Add(2*testCatalogInterval+time.Minute), KafkaSample{Joined: true, Partitions: 3, Committed: 120, Lag: 20})
	assertReady(t, r, at.Add(2*testCatalogInterval+time.Minute))
}

type fakeLagReader struct {
	lags kadm.DescribedGroupLags
	err  error
}

func (f fakeLagReader) Lag(context.Context, ...string) (kadm.DescribedGroupLags, error) {
	return f.lags, f.err
}

func TestSampleKafkaGroup(t *testing.T) {
	member := kadm.DescribedGroupMember{MemberID: "observer-1"}
	lag := func(partition int32, committed, behind int64, owner *kadm.DescribedGroupMember) kadm.GroupMemberLag {
		return kadm.GroupMemberLag{Member: owner, Topic: "analytics_events", Partition: partition, Commit: kadm.Offset{At: committed}, Lag: behind}
	}
	reader := fakeLagReader{lags: kadm.DescribedGroupLags{"group": {
		Group:   "group",
		Members: []kadm.DescribedGroupMember{member},
		Lag: kadm.GroupLag{"analytics_events": {
			0: lag(0, 10, 2, &member),
			1: lag(1, 20, -1, &member),
			2: lag(2, 30, 5, nil),
		}},
	}}}
	sample := SampleKafkaGroup(context.Background(), reader, "group", "observer-1")
	if sample.Err != nil || !sample.Joined || sample.Partitions != 2 || sample.Lag != 2 || sample.Committed != 10 {
		t.Fatalf("sample = %+v; want joined, 2 assigned partitions, the known lag 2 and its commit 10", sample)
	}
	if sample = SampleKafkaGroup(context.Background(), reader, "group", ""); sample.Joined {
		t.Fatal("a process without a member ID was reported as joined")
	}
	if sample = SampleKafkaGroup(context.Background(), reader, "other", "observer-1"); sample.Err == nil {
		t.Fatal("an undescribed group was sampled")
	}
	failed := fakeLagReader{lags: kadm.DescribedGroupLags{"group": {Group: "group", DescribeErr: errors.New("coordinator not available")}}}
	if sample = SampleKafkaGroup(context.Background(), failed, "group", "observer-1"); sample.Err == nil {
		t.Fatal("a describe error was sampled as healthy")
	}
}

func TestReadyEndpointReportsEveryReason(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := &API{Store: &store.Store{DB: db}, Readiness: NewReadiness(testCatalogInterval, time.Now().UTC())}
	recorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ready", nil))
	var report ReadinessReport
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("ready body %q: %v", recorder.Body.String(), err)
	}
	if recorder.Code != http.StatusServiceUnavailable || report.Ready || len(report.Reasons) != 3 || report.Reasons[0] != "database: unavailable" {
		t.Fatalf("ready = %d %+v; want 503 naming the database, the catalog and Kafka", recorder.Code, report)
	}
}
