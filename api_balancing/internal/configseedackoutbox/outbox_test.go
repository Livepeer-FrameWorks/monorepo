package configseedackoutbox

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// TestWorkerQuarantinesInvalidArgumentDelivery proves Navigator's terminal
// InvalidArgument classification quarantines the row instead of retrying a
// permanently malformed delivery forever.
func TestWorkerQuarantinesInvalidArgumentDelivery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worker := NewWorker(db, &ackClient{err: status.Error(codes.InvalidArgument, "cluster identity is missing")}, logging.NewLogger())
	payload, signature := encodeAckRow(t, "node-1", "cluster-1", 9)
	rows := sqlmock.NewRows([]string{"id", "node_id", "cluster_id", "seed_version", "request_payload", "result_signature", "revision", "attempts"}).
		AddRow(int64(11), "node-1", "cluster-1", int64(9), payload, signature, int64(5), int32(7))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn\.config_seed_apply_ack_outbox\s+SET pending = false,[\s\S]*delivered_at = NULL`).
		WithArgs(sqlmock.AnyArg(), int64(11), int64(5), worker.leaseOwner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// encodeAckRow builds a persisted ACK payload plus its matching signature so
// deliver's integrity checks pass and the delivery outcome is what is under
// test.
func encodeAckRow(t *testing.T, nodeID, clusterID string, seedVersion uint64) ([]byte, []byte) {
	t.Helper()
	req := &dnspb.ReportConfigSeedApplyResultRequest{
		NodeId: nodeID, ClusterId: clusterID, SeedVersion: seedVersion, Success: true,
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := resultSignature(req.GetSuccess(), req.GetAppliedBundleIds(), req.GetFailedBundleIds(), req.GetBundleVersions())
	if err != nil {
		t.Fatal(err)
	}
	return payload, signature
}

type ackClient struct {
	err      error
	accepted bool
	requests []*dnspb.ReportConfigSeedApplyResultRequest
}

func TestWriterClassifiesOlderSeedAsStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metrics := &Metrics{Outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_config_seed_ack_outcomes_total"}, []string{"outcome"})}
	w := NewWriter(db, metrics)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.config_seed_apply_ack_outbox")).
		WithArgs("node-1", "cluster-1", int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT seed_version\nFROM foghorn.config_seed_apply_ack_outbox")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"seed_version"}).AddRow(int64(8)))
	if err := w.Enqueue(context.Background(), "node-1", "cluster-1", &ipcpb.ConfigSeedApplyResult{SeedVersion: 7, Success: true}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(metrics.Outcomes.WithLabelValues("stale")); got != 1 {
		t.Fatalf("stale outcome=%v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func (c *ackClient) ReportConfigSeedApplyResult(_ context.Context, req *dnspb.ReportConfigSeedApplyResultRequest) (*dnspb.ReportConfigSeedApplyResultResponse, error) {
	c.requests = append(c.requests, req)
	return &dnspb.ReportConfigSeedApplyResultResponse{Accepted: c.accepted}, c.err
}

func TestWriterPersistsCanonicalRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := NewWriter(db)
	if enqueueErr := w.Enqueue(context.Background(), "", "cluster", &ipcpb.ConfigSeedApplyResult{SeedVersion: 1}); enqueueErr == nil {
		t.Fatal("accepted ACK without node identity")
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.config_seed_apply_ack_outbox")).
		WithArgs("node-1", "cluster-1", int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	err = w.Enqueue(context.Background(), "node-1", "cluster-1", &ipcpb.ConfigSeedApplyResult{
		SeedVersion: 7, AppliedBundleIds: []string{"tenant:t1"}, Success: true,
		BundleVersions: map[string]string{"tenant:t1": "revision-1"},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResultSignatureIgnoresOrderingAndNonProjectionFields(t *testing.T) {
	first, err := resultSignature(true, []string{"tenant:b", "tenant:a"}, []string{"tenant:d", "tenant:c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resultSignature(true, []string{"tenant:a", "tenant:b"}, []string{"tenant:c", "tenant:d"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) {
		t.Fatalf("equivalent bundle sets produced different signatures: %x != %x", first, second)
	}
	different, err := resultSignature(true, []string{"tenant:a"}, []string{"tenant:b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(first, different) {
		t.Fatal("different bundle projections produced the same signature")
	}
	versionOne, err := resultSignature(true, []string{"tenant:a"}, nil, map[string]string{"tenant:a": "revision-1"})
	if err != nil {
		t.Fatal(err)
	}
	versionTwo, err := resultSignature(true, []string{"tenant:a"}, nil, map[string]string{"tenant:a": "revision-2"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(versionOne, versionTwo) {
		t.Fatal("different bundle revisions produced the same durable projection")
	}
	emptySuccess, err := resultSignature(true, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyFailure, err := resultSignature(false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(emptySuccess, emptyFailure) {
		t.Fatal("empty success and empty failure must be distinct durable outcomes")
	}
}

func TestWorkerRetainsACKAcrossNavigatorOutage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload, err := marshalRequest(&dnspb.ReportConfigSeedApplyResultRequest{
		NodeId: "node-1", ClusterId: "cluster-1", SeedVersion: 7,
		BundleVersions: map[string]string{"tenant:t1": "revision-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := resultSignature(false, nil, nil, map[string]string{"tenant:t1": "revision-1"})
	if err != nil {
		t.Fatal(err)
	}
	client := &ackClient{err: errors.New("navigator down")}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "node_id", "cluster_id", "seed_version", "request_payload", "result_signature", "revision", "attempts"}).
		AddRow(int64(9), "node-1", "cluster-1", int64(7), payload, signature, int64(3), int32(0))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn\.config_seed_apply_ack_outbox\s+SET attempts = attempts \+ 1`).
		WithArgs("navigator down", int64(9), int64(3), worker.leaseOwner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.requests) != 1 {
		t.Fatalf("delivery calls=%d, want 1", len(client.requests))
	}
	if got := client.requests[0].GetDeliverySequence(); got != 3 {
		t.Fatalf("delivery sequence=%d, want row revision 3", got)
	}
	if got := client.requests[0].GetBundleVersions()["tenant:t1"]; got != "revision-1" {
		t.Fatalf("bundle revision=%q, want revision-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSettlesOnlyAcceptedRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload, err := marshalRequest(&dnspb.ReportConfigSeedApplyResultRequest{NodeId: "node-1", ClusterId: "cluster-1", SeedVersion: 8})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := resultSignature(false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &ackClient{accepted: true}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "node_id", "cluster_id", "seed_version", "request_payload", "result_signature", "revision", "attempts"}).
		AddRow(int64(10), "node-1", "cluster-1", int64(8), payload, signature, int64(4), int32(1))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn\.config_seed_apply_ack_outbox\s+SET pending = false,[\s\S]*delivered_at = NOW\(\)`).
		WithArgs(int64(10), int64(4), worker.leaseOwner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerQuarantinesInvalidPayloadWithoutRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worker := NewWorker(db, &ackClient{accepted: true}, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "node_id", "cluster_id", "seed_version", "request_payload", "result_signature", "revision", "attempts"}).
		AddRow(int64(11), "node-1", "cluster-1", int64(9), []byte{0xff}, make([]byte, 32), int64(5), int32(7))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn\.config_seed_apply_ack_outbox\s+SET pending = false,[\s\S]*delivered_at = NULL`).
		WithArgs(sqlmock.AnyArg(), int64(11), int64(5), worker.leaseOwner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerQuarantinesPayloadWhoseProjectionDoesNotMatchSignature(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload, err := marshalRequest(&dnspb.ReportConfigSeedApplyResultRequest{
		NodeId: "node-1", ClusterId: "cluster-1", SeedVersion: 9,
		AppliedBundleIds: []string{"tenant:t1"}, BundleVersions: map[string]string{"tenant:t1": "revision-2"}, Success: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSignature, err := resultSignature(true, []string{"tenant:t1"}, nil, map[string]string{"tenant:t1": "revision-1"})
	if err != nil {
		t.Fatal(err)
	}
	client := &ackClient{accepted: true}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "node_id", "cluster_id", "seed_version", "request_payload", "result_signature", "revision", "attempts"}).
		AddRow(int64(12), "node-1", "cluster-1", int64(9), payload, oldSignature, int64(6), int32(0))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(`UPDATE foghorn\.config_seed_apply_ack_outbox\s+SET pending = false,[\s\S]*delivered_at = NULL`).
		WithArgs(sqlmock.AnyArg(), int64(12), int64(6), worker.leaseOwner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.requests) != 0 {
		t.Fatalf("signature-mismatched payload was delivered: %#v", client.requests)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func marshalRequest(req *dnspb.ReportConfigSeedApplyResultRequest) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(req)
}
