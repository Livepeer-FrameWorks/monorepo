package grpc

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ingestStopWriteContext() context.Context {
	ctx := context.WithValue(retentionAuthCtx(uuid.NewString()), ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:write"})
}

func TestDisconnectIngestRequiresStreamsWrite(t *testing.T) {
	server, _, done := newRetentionServer(t)
	defer done()
	ctx := context.WithValue(ingestStopWriteContext(), ctxkeys.KeyPermissions, []string{"billing:write"})
	_, err := server.DisconnectIngest(ctx, &pb.DisconnectIngestRequest{StreamId: uuid.NewString()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("disconnect without streams:write = %v", err)
	}
}

func TestDisconnectIngestPullPointsToDisableSource(t *testing.T) {
	server, mock, done := newRetentionServer(t)
	defer done()
	mock.ExpectQuery("GetStreamIngestModeForUser").WillReturnRows(sqlmock.NewRows([]string{"ingest_mode"}).AddRow("pull"))
	_, err := server.DisconnectIngest(ingestStopWriteContext(), &pb.DisconnectIngestRequest{StreamId: uuid.NewString()})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "pullSource.enabled=false") {
		t.Fatalf("pull stop has no actionable source setting: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshStreamKeyEndActiveSessionBindsOldClaim(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	expectPushStream(mock)
	mock.ExpectBegin()
	mock.ExpectQuery("LockIngestStopTarget").WillReturnRows(sqlmock.NewRows([]string{"id", "internal_name", "ingest_mode", "active_ingest_claim_id", "control_cell_id"}).AddRow("s1", "runtime", "push", "old-claim", "cell"))
	mock.ExpectExec("RefreshPrimaryStreamKey").WillReturnResult(sqlmock.NewResult(0, 1))
	expectDualEventInsert(mock, "stream.key_rotated", eventStreamUpdated)
	mock.ExpectExec("EnqueueIngestStopObligation").WithArgs(sqlmock.AnyArg(), testTenantID, "key_rotated", sqlmock.AnyArg(), ingestStopPayloadArgument{claim: "old-claim", refresh: true}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("GetStreamPlaybackID").WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("playback"))
	response, err := server.RefreshStreamKey(ctxAs("u1", testTenantID, "owner"), &pb.RefreshStreamKeyRequest{StreamId: "s1", EndActiveSession: true})
	if err != nil || response.GetStreamKey() == "" {
		t.Fatalf("rotate-and-end did not capture its publisher and enqueue the stop in one transaction: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type ingestStopPayloadArgument struct {
	claim   string
	refresh bool
}

func (argument ingestStopPayloadArgument) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok {
		text, isText := value.(string)
		if !isText {
			return false
		}
		raw = []byte(text)
	}
	var payload ingestStopPayload
	return json.Unmarshal(raw, &payload) == nil && payload.ClaimID == argument.claim && payload.RefreshAuthority == argument.refresh && payload.InternalName != "" && payload.ControlCellID != "" && payload.StreamID != ""
}

func TestDisconnectIngestCapturesClaimInDurablePayload(t *testing.T) {
	server, mock, done := newRetentionServer(t)
	defer done()
	stream := uuid.NewString()
	mock.ExpectQuery("GetStreamIngestModeForUser").WillReturnRows(sqlmock.NewRows([]string{"ingest_mode"}).AddRow("push"))
	mock.ExpectBegin()
	mock.ExpectQuery("LockIngestStopTarget").WillReturnRows(sqlmock.NewRows([]string{"id", "internal_name", "ingest_mode", "active_ingest_claim_id", "control_cell_id"}).AddRow(stream, "runtime", "push", "captured-claim", "cell"))
	mock.ExpectExec("EnqueueIngestStopObligation").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "requested", sqlmock.AnyArg(), ingestStopPayloadArgument{claim: "captured-claim"}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	response, err := server.DisconnectIngest(ingestStopWriteContext(), &pb.DisconnectIngestRequest{StreamId: stream})
	if err != nil || !response.GetStopRequested() || response.GetStreamId() != stream {
		t.Fatalf("disconnect did not durably capture the current claim: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
