package triggers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestPlacementErrorCodeSeparatesDenialFromTransientFailure(t *testing.T) {
	_, unsupported := mist.IngestProtocol("dtsc")
	for _, tc := range []struct {
		name string
		err  error
		want ipcpb.IngestErrorCode
	}{
		{"policy_denied", status.Error(codes.PermissionDenied, "selected destination has no current placement permission"), ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		{"unsupported_protocol", unsupported, ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		{"protocol_not_offered", federation.ErrProtocolNotOffered, ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		{"signed_authority_denies", fmt.Errorf("compile: %w", balancer.ErrPlacementAuthorityDenied), ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		// An unobservable destination or an unreachable dependency, such as
		// Commodore without a signed outage owner, is retried on reconnect.
		{"observation_unavailable", status.Error(codes.Unavailable, "selected destination observation is unavailable"), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"rpc_deadline", status.Error(codes.DeadlineExceeded, "slow"), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"incomplete_census", fmt.Errorf("%w: %w", balancer.ErrPlacementUnavailable, balancer.ErrPlacementObservationIncomplete), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"authority_race", status.Error(codes.FailedPrecondition, "placement authority facts changed during revalidation"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"authority_kept_changing", fmt.Errorf("admission: %w", federation.ErrPlacementAuthorityChanged), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"deadline", context.DeadlineExceeded, ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		// An attempt whose receipt conflicted, vanished or expired ends only
		// that attempt; the reconnect prepares a new one.
		{"receipt_attempt_expired", fmt.Errorf("prepare: %w", federation.ErrPlacementReceiptExpired), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"receipt_attempt_conflict", federation.ErrPlacementReceiptConflict, ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		// A new stream whose authority has not reached this cell because the
		// fetch failed (here skipped during the outage backoff) is a slow
		// control plane, not a refusal.
		{"authority_not_yet_fetched", errors.Join(sql.ErrNoRows, localauthority.ErrAuthorityFetchUnavailable, localauthority.ErrAuthorityFetchFailed), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"authority_absent", sql.ErrNoRows, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"generic", errors.New("ingest placement admission is unavailable"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
	} {
		if got := ingestPlacementErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
