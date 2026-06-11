package control

import (
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestClassifyTriggerError pins the ack contract that drives Helmsman's WAL
// retries. The two dangerous failure modes are: marking a terminal rejection
// retryable (infinite retry of a permanently-rejected stream), and marking a
// transient failure non-retryable (a real event silently dropped). Every code
// is asserted on both axes.
func TestClassifyTriggerError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCode      ipcpb.TriggerAckErrorCode
		wantRetryable bool
	}{
		{
			name:          "nil_is_success",
			err:           nil,
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_NONE,
			wantRetryable: false,
		},
		{
			name:          "invalid_stream_key_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY, "bad key"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "account_suspended_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED, "suspended"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "payment_required_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED, "pay"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "duplicate_ingest_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST, "dup"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "free_tier_exhausted_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_FREE_TIER_EXHAUSTED, "exhausted"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "tenant_stream_cap_terminal",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_TENANT_STREAM_CAP, "cap"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_SCHEMA,
			wantRetryable: false,
		},
		{
			name:          "timeout_is_downstream_retryable",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, "timed out"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_DOWNSTREAM_UNAVAILABLE,
			wantRetryable: true,
		},
		{
			name:          "internal_ingest_is_internal_retryable",
			err:           ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL, "?"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_INTERNAL,
			wantRetryable: true,
		},
		{
			name:          "wrapped_ingest_error_still_classified",
			err:           fmt.Errorf("publish failed: %w", ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, "to")),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_DOWNSTREAM_UNAVAILABLE,
			wantRetryable: true,
		},
		{
			name:          "non_ingest_error_assumed_transient",
			err:           errors.New("kafka broker unavailable"),
			wantCode:      ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_KAFKA_PUBLISH,
			wantRetryable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, retryable := classifyTriggerError(tt.err)
			if code != tt.wantCode {
				t.Errorf("code = %v, want %v", code, tt.wantCode)
			}
			if retryable != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v", retryable, tt.wantRetryable)
			}
		})
	}
}
