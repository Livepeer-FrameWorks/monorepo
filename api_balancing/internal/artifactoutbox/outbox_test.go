package artifactoutbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func sp(s string) *string { return &s }

// resetPackageState clears the package-level dependencies between tests so a
// db set by one test can't leak into another (Init mutates process globals).
func resetPackageState(t *testing.T) {
	t.Cleanup(func() {
		db = nil
		logger = nil
		decklogClient = nil
	})
}

// marshalPayload must serialize each of the four state-coupled Foghorn event
// kinds and reject anything else loudly — an unsupported type is a programmer
// error, not a silently-dropped event.
func TestMarshalPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload any
	}{
		{"clip", &ipcpb.ClipLifecycleData{TenantId: sp("t1"), ClipHash: "c1"}},
		{"dvr", &ipcpb.DVRLifecycleData{TenantId: sp("t1"), DvrHash: "d1"}},
		{"vod", &ipcpb.VodLifecycleData{TenantId: sp("t1"), VodHash: "v1"}},
		{"federation", &ipcpb.FederationEventData{TenantId: sp("t1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := marshalPayload(tc.payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !json.Valid(body) || len(body) == 0 {
				t.Fatalf("expected valid non-empty JSON, got %q", body)
			}
		})
	}

	t.Run("unsupported type errors", func(t *testing.T) {
		if _, err := marshalPayload("not a proto"); err == nil {
			t.Fatal("expected an error for an unsupported payload type")
		}
	})
}

func TestIDArray(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{"empty", nil, "{}"},
		{"single", []string{"a"}, "{a}"},
		{"multi", []string{"a", "b", "c"}, "{a,b,c}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idArray(tt.ids); got != tt.want {
				t.Errorf("idArray(%v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}

// Nil payloads are no-ops even before Init wires the DB — background producers
// can call the Enqueue helpers unconditionally.
func TestEnqueueNilIsNoOp(t *testing.T) {
	resetPackageState(t)
	if err := EnqueueClipLifecycle(nil); err != nil {
		t.Errorf("clip: %v", err)
	}
	if err := EnqueueDVRLifecycle(nil); err != nil {
		t.Errorf("dvr: %v", err)
	}
	if err := EnqueueVodLifecycle(nil); err != nil {
		t.Errorf("vod: %v", err)
	}
	if err := EnqueueFederationEvent(nil); err != nil {
		t.Errorf("federation: %v", err)
	}
}

func TestEnqueueWritesOutboxRow(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	t.Run("clip carries kind, ids and payload", func(t *testing.T) {
		mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
			WithArgs(kindClipLifecycle, "tenant-1", "stream-1", "cliphash", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		err := EnqueueClipLifecycle(&ipcpb.ClipLifecycleData{
			TenantId: sp("tenant-1"), StreamId: sp("stream-1"), ClipHash: "cliphash",
		})
		if err != nil {
			t.Fatalf("enqueue clip: %v", err)
		}
	})

	t.Run("vod leaves stream_id blank", func(t *testing.T) {
		// VOD uploads aren't always tied to a live stream — stream_id is "".
		mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
			WithArgs(kindVodLifecycle, "tenant-1", "", "vodhash", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		err := EnqueueVodLifecycle(&ipcpb.VodLifecycleData{TenantId: sp("tenant-1"), VodHash: "vodhash"})
		if err != nil {
			t.Fatalf("enqueue vod: %v", err)
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// claimBatch reads pending rows and stamps them claimed in the same
// transaction. The empty case must skip the UPDATE entirely.
func TestClaimBatch(t *testing.T) {
	t.Run("claims and stamps pending rows", func(t *testing.T) {
		resetPackageState(t)
		mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		Init(mockDB, logging.NewLogger(), nil)

		rows := sqlmock.NewRows([]string{
			"id", "event_kind", "tenant_id", "stream_id", "artifact_id", "payload", "attempts", "created_at",
		}).AddRow("id-1", kindClipLifecycle, "tenant-1", "stream-1", "cliphash", `{"tenantId":"tenant-1"}`, 0, time.Unix(1_700_000_000, 0))

		mock.ExpectBegin()
		mock.ExpectQuery(`FROM foghorn\.artifact_event_outbox`).
			WithArgs("60 seconds", batchSize).
			WillReturnRows(rows)
		mock.ExpectExec(`SET claimed_at = NOW`).
			WithArgs(idArray([]string{"id-1"})).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		out, err := claimBatch(context.Background())
		if err != nil {
			t.Fatalf("claimBatch: %v", err)
		}
		if len(out) != 1 || out[0].id != "id-1" || out[0].eventKind != kindClipLifecycle {
			t.Fatalf("unexpected claim batch: %+v", out)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("no pending rows skips the claim update", func(t *testing.T) {
		resetPackageState(t)
		mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		Init(mockDB, logging.NewLogger(), nil)

		empty := sqlmock.NewRows([]string{
			"id", "event_kind", "tenant_id", "stream_id", "artifact_id", "payload", "attempts", "created_at",
		})
		mock.ExpectBegin()
		mock.ExpectQuery(`FROM foghorn\.artifact_event_outbox`).
			WithArgs("60 seconds", batchSize).
			WillReturnRows(empty)
		mock.ExpectCommit()

		out, err := claimBatch(context.Background())
		if err != nil {
			t.Fatalf("claimBatch: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("expected empty batch, got %+v", out)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

// With a nil Decklog client the worker is a logged no-op: Enqueue still
// persists rows (retention), but RunWorker returns immediately instead of
// draining — the documented retention-vs-delivery split.
func TestRunWorkerWithoutDecklogClientReturns(t *testing.T) {
	resetPackageState(t)
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	done := make(chan struct{})
	go func() {
		RunWorker(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWorker did not return promptly with a nil decklog client")
	}
}

// dispatchRow fails closed when the Decklog client was never wired, rather
// than dropping the event.
func TestDispatchRowWithoutClientErrors(t *testing.T) {
	resetPackageState(t)
	decklogClient = nil
	_, err := dispatchRow(context.Background(), outboxRow{eventKind: kindClipLifecycle})
	if err == nil {
		t.Fatal("expected an error when decklog client is not configured")
	}
}
