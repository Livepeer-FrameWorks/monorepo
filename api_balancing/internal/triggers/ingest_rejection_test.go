package triggers

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestIngestErrorCodeForStreamKeyRejection(t *testing.T) {
	tests := []struct {
		name   string
		reason commodorepb.StreamKeyRejectionReason
		want   ipcpb.IngestErrorCode
	}{
		{"unspecified", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_UNSPECIFIED, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"invalid key", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY, ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY},
		{"inactive user", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE, ipcpb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED},
		{"pull mode", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE, ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY},
		{"tenant suspended", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED, ipcpb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED},
		{"negative balance", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE, ipcpb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED},
		{"cluster not entitled", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED, ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY},
		{"cluster class mismatch", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_CLASS_MISMATCH, ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY},
		{"protocol not supported", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PROTOCOL_NOT_SUPPORTED, ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY},
		{"cluster unhealthy", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"duplicate ingest", commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST, ipcpb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ingestErrorCodeForStreamKeyRejection(tt.reason); got != tt.want {
				t.Fatalf("code = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestHandlePushRewrite_ClaimRaceReportsDuplicateIngest(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	previousRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "demo-media", 0))
	t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })
	mock := installControlDBForTest(t)
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)

	identity := &commodorepb.ValidateStreamKeyResponse{
		Valid: true, TenantId: "tenant-1", UserId: "user-1", StreamId: "stream-1", InternalName: "internal-1",
	}
	duplicate := &commodorepb.ValidateStreamKeyResponse{
		Valid: false, Error: "stream already has an active publisher",
		RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST,
	}
	commodoreClient, cleanup, stub := setupCommodoreClientWithStub(t, identity, nil)
	t.Cleanup(cleanup)
	stub.SetValidateResponseQueue(identity, duplicate)
	p := newTestProcessor(t)
	p.commodoreClient = commodoreClient

	_, abort, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{
			Pid: 42, TriggerUuid: "duplicate-race", TriggerUnixMillis: 1,
			StreamName: "sk-demo", Hostname: "127.0.0.1",
		}},
	})
	if err == nil || !abort {
		t.Fatalf("duplicate claim race must abort: abort=%v err=%v", abort, err)
	}
	var ingestErr *ingesterrors.IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != ipcpb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST {
		t.Fatalf("error = %v, want duplicate-ingest code", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
