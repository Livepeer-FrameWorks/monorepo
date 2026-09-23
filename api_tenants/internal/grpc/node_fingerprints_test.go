package grpc

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	testFingerprintID  = "0b9f7a3e-6f0f-4b8e-9c55-3f7e0f2d2a11"
	testFingerprintID2 = "1c0e8b4f-7010-4c9f-8d66-4081103e3b22"
)

func fingerprintOperatorCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator-user")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "operator-tenant")
	return context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
}

func fingerprintTenantOwnerCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "owner-user")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	return context.WithValue(ctx, ctxkeys.KeyRole, "owner")
}

// A service token is trusted elsewhere but names no operator; with a
// platform-operator flag forged into it the call is still refused.
func fingerprintServiceCtx() context.Context {
	return context.WithValue(serviceCtx(), ctxkeys.KeyPlatformOperator, true)
}

var fingerprintListColumns = []string{
	"id", "node_id", "tenant_id", "cluster_id", "machine_sha256", "macs_sha256",
	"has_identity_key", "first_seen", "last_seen", "sort_time", "machine_count", "macs_count",
}

func TestNodeFingerprintRPCsRequirePlatformOperatorJWT(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"tenant owner JWT", fingerprintTenantOwnerCtx()},
		{"service token", fingerprintServiceCtx()},
		{"no identity", context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server := &QuartermasterServer{db: db, logger: logrus.New()}

			_, err = server.ListNodeFingerprints(tc.ctx, &quartermasterpb.ListNodeFingerprintsRequest{})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("list code = %s, want PermissionDenied", status.Code(err))
			}
			_, err = server.UnbindNodeFingerprint(tc.ctx, &quartermasterpb.UnbindNodeFingerprintRequest{
				NodeId: "node-1", FingerprintId: testFingerprintID, Reason: "stale",
			})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unbind code = %s, want PermissionDenied", status.Code(err))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("denied call touched the database: %v", err)
			}
		})
	}
}

func TestListNodeFingerprintsPaginatesAndReportsDuplicates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &QuartermasterServer{db: db, logger: logrus.New()}

	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	second := first.Add(-time.Hour)
	cursor := pagination.EncodeCursor(first.Add(time.Hour), testFingerprintID2)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM bindings b").
		WithArgs("cluster-a", true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("ORDER BY b.sort_time DESC, b.id DESC").
		WithArgs("cluster-a", true, sqlmock.AnyArg(), testFingerprintID2, 3).
		WillReturnRows(sqlmock.NewRows(fingerprintListColumns).
			AddRow(testFingerprintID, "node-a", "tenant-1", "cluster-a", "machine-dup", "macs-a", true, first, first, first, 2, 0).
			AddRow(testFingerprintID2, "node-b", "", "cluster-a", "machine-dup", "", false, nil, nil, second, 2, 0).
			AddRow("2d1f9c50-8121-4da0-9e77-5192214f4c33", "node-c", "tenant-2", "cluster-a", "", "macs-dup", true, second, second, second, 0, 2))

	resp, err := server.ListNodeFingerprints(fingerprintOperatorCtx(), &quartermasterpb.ListNodeFingerprintsRequest{
		ClusterId:      "cluster-a",
		DuplicatesOnly: true,
		Pagination:     &commonpb.CursorPaginationRequest{First: 2, After: &cursor},
	})
	if err != nil {
		t.Fatalf("ListNodeFingerprints: %v", err)
	}
	if got := len(resp.GetFingerprints()); got != 2 {
		t.Fatalf("page size = %d, want 2 (limit trims the lookahead row)", got)
	}
	a := resp.GetFingerprints()[0]
	if a.GetFingerprintId() != testFingerprintID || a.GetNodeId() != "node-a" || a.GetClusterId() != "cluster-a" ||
		a.GetMachineDuplicateCount() != 2 || a.GetMacsDuplicateCount() != 0 || !a.GetHasIdentityKey() ||
		!a.GetFirstSeen().AsTime().Equal(first) {
		t.Fatalf("unexpected first binding: %+v", a)
	}
	if b := resp.GetFingerprints()[1]; b.GetFirstSeen() != nil || b.GetTenantId() != "" || b.GetHasIdentityKey() {
		t.Fatalf("unexpected second binding: %+v", b)
	}
	page := resp.GetPagination()
	if !page.GetHasNextPage() || page.GetTotalCount() != 3 {
		t.Fatalf("pagination = %+v, want has_next with total 3", page)
	}
	end, err := pagination.DecodeCursor(page.GetEndCursor())
	if err != nil || end.ID != testFingerprintID2 || !end.Timestamp.Equal(second) {
		t.Fatalf("end cursor = %+v (%v), want the second row's sort key", end, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListNodeFingerprintsRejectsNonUUIDCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &QuartermasterServer{db: db, logger: logrus.New()}
	cursor := pagination.EncodeCursor(time.Now(), "not-a-uuid")
	_, err = server.ListNodeFingerprints(fingerprintOperatorCtx(), &quartermasterpb.ListNodeFingerprintsRequest{
		Pagination: &commonpb.CursorPaginationRequest{After: &cursor},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnbindNodeFingerprintValidatesInput(t *testing.T) {
	for _, req := range []*quartermasterpb.UnbindNodeFingerprintRequest{
		{NodeId: "node-1", FingerprintId: testFingerprintID, Reason: "   "},
		{NodeId: "", FingerprintId: testFingerprintID, Reason: "stale"},
		{NodeId: "node-1", FingerprintId: "", Reason: "stale"},
		{NodeId: "node-1", FingerprintId: "fp-1", Reason: "stale"},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		server := &QuartermasterServer{db: db, logger: logrus.New()}
		_, err = server.UnbindNodeFingerprint(fingerprintOperatorCtx(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: code = %s, want InvalidArgument", req, status.Code(err))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
}

func TestUnbindNodeFingerprintNotFoundRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &QuartermasterServer{db: db, logger: logrus.New()}
	mock.ExpectBegin()
	mock.ExpectQuery("DELETE FROM quartermaster.node_fingerprints").
		WithArgs(testFingerprintID, "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "cluster_id"}))
	mock.ExpectRollback()
	_, err = server.UnbindNodeFingerprint(fingerprintOperatorCtx(), &quartermasterpb.UnbindNodeFingerprintRequest{
		NodeId: "node-1", FingerprintId: testFingerprintID, Reason: "stale",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %s, want NotFound", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// payloadCapture records the service-event payload handed to the outbox insert.
type payloadCapture struct{ payload string }

func (c *payloadCapture) Match(v driver.Value) bool {
	s, ok := v.(string)
	if ok {
		c.payload = s
	}
	return ok
}

func TestUnbindNodeFingerprintRecordsOperatorEventInTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &QuartermasterServer{db: db, logger: logrus.New()}
	capture := &payloadCapture{}

	mock.ExpectBegin()
	mock.ExpectQuery("DELETE FROM quartermaster.node_fingerprints").
		WithArgs(testFingerprintID, "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "cluster_id"}).AddRow("tenant-1", "cluster-a"))
	mock.ExpectQuery("INSERT INTO quartermaster.service_event_outbox").
		WithArgs(sqlmock.AnyArg(), "node.fingerprint_unbound", "", "platform", "operator-user", "node_fingerprint", testFingerprintID, capture).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-1"))
	mock.ExpectCommit()

	resp, err := server.UnbindNodeFingerprint(fingerprintOperatorCtx(), &quartermasterpb.UnbindNodeFingerprintRequest{
		NodeId: " node-1 ", FingerprintId: testFingerprintID, Reason: " replaced hardware ",
	})
	if err != nil {
		t.Fatalf("UnbindNodeFingerprint: %v", err)
	}
	if resp.GetNodeId() != "node-1" || resp.GetFingerprintId() != testFingerprintID {
		t.Fatalf("response = %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	var event ipcpb.ServiceEvent
	if err := protojson.Unmarshal([]byte(capture.payload), &event); err != nil {
		t.Fatalf("decode payload %q: %v", capture.payload, err)
	}
	cluster := event.GetClusterEvent()
	if event.GetUserId() != "operator-user" || event.GetTenantId() != "" || event.GetSource() != "quartermaster" ||
		cluster.GetReason() != "replaced hardware" || cluster.GetTenantId() != "tenant-1" || cluster.GetClusterId() != "cluster-a" {
		t.Fatalf("unexpected event: %s", capture.payload)
	}
	before := cluster.GetBeforeState().AsMap()
	if before["node_id"] != "node-1" || before["fingerprint_id"] != testFingerprintID || len(before) != 2 {
		t.Fatalf("before_state = %v, want exactly node_id and fingerprint_id", before)
	}
	for _, secret := range []string{"sha256", "machine", "macs", "public_key", "seen_ips"} {
		if strings.Contains(capture.payload, secret) {
			t.Fatalf("event payload carries %q: %s", secret, capture.payload)
		}
	}
}

func TestNodeFingerprintUnboundIsPlatformScoped(t *testing.T) {
	scope, err := serviceEventScope(&ipcpb.ServiceEvent{EventType: "node.fingerprint_unbound"})
	if err != nil || scope != "platform" {
		t.Fatalf("scope = %q, %v; want platform", scope, err)
	}
}
