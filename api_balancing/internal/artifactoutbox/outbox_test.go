package artifactoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
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

// dispatchRow must reject an unknown event kind loudly rather than silently
// completing the row — a corrupt/forward-incompatible kind is retained, not
// dropped. A zero-value BatchedClient is enough to pass the nil guard because
// the default switch arm returns before touching any client method.
func TestDispatchRowUnknownKindErrors(t *testing.T) {
	resetPackageState(t)
	decklogClient = &decklogclient.BatchedClient{}
	_, err := dispatchRow(context.Background(), outboxRow{eventKind: "totally_unknown"})
	if err == nil {
		t.Fatal("expected an error for an unknown event kind")
	}
}

// The Tx variants share enqueue() with the non-Tx helpers but must route the
// INSERT through the caller-supplied transaction handle, not the package db,
// so the outbox row commits/rolls back atomically with the caller's work.
func TestEnqueueTxRoutesInsertThroughCallerTx(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	// Each Tx call runs inside a real transaction begun on the mock db; the
	// INSERT must be observed on that tx (between Begin and Commit), proving
	// the caller's handle — not the package db — carried the write.
	cases := []struct {
		name     string
		tenant   string
		stream   string
		artifact string
		kind     string
		run      func(ctx context.Context, tx execContext) error
	}{
		{
			name: "clip", tenant: "tenant-1", stream: "stream-1", artifact: "cliphash", kind: kindClipLifecycle,
			run: func(ctx context.Context, tx execContext) error {
				return EnqueueClipLifecycleTx(ctx, tx, &ipcpb.ClipLifecycleData{
					TenantId: sp("tenant-1"), StreamId: sp("stream-1"), ClipHash: "cliphash",
				})
			},
		},
		{
			name: "dvr", tenant: "tenant-1", stream: "stream-1", artifact: "dvrhash", kind: kindDVRLifecycle,
			run: func(ctx context.Context, tx execContext) error {
				return EnqueueDVRLifecycleTx(ctx, tx, &ipcpb.DVRLifecycleData{
					TenantId: sp("tenant-1"), StreamId: sp("stream-1"), DvrHash: "dvrhash",
				})
			},
		},
		{
			name: "vod", tenant: "tenant-1", stream: "", artifact: "vodhash", kind: kindVodLifecycle,
			run: func(ctx context.Context, tx execContext) error {
				return EnqueueVodLifecycleTx(ctx, tx, &ipcpb.VodLifecycleData{
					TenantId: sp("tenant-1"), VodHash: "vodhash",
				})
			},
		},
		{
			name: "federation", tenant: "tenant-1", stream: "stream-1", artifact: "", kind: kindFederationEvent,
			run: func(ctx context.Context, tx execContext) error {
				return EnqueueFederationEventTx(ctx, tx, &ipcpb.FederationEventData{
					TenantId: sp("tenant-1"), StreamId: sp("stream-1"),
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
				WithArgs(tc.kind, tc.tenant, tc.stream, tc.artifact, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			tx, err := mockDB.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if err := tc.run(context.Background(), tx); err != nil {
				t.Fatalf("enqueue tx: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
		})
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Federation events from the system tenant carry an empty tenant id; enqueue
// passes "" through and the SQL coerces it to NULL (NULLIF($2,”)::uuid). We
// assert the literal "" reaches the driver — the NULL coercion is the column's
// job, but the contract is that we never fabricate a fake tenant uuid.
func TestEnqueueFederationEmptyTenantCoercesToNull(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(kindFederationEvent, "", "stream-1", "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// No TenantId set -> GetTenantId() == "".
	if err := EnqueueFederationEvent(&ipcpb.FederationEventData{StreamId: sp("stream-1")}); err != nil {
		t.Fatalf("enqueue federation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// An INSERT failure must propagate to the caller (wrapped), not be swallowed —
// the synchronous Enqueue path is how producers know the row didn't persist.
func TestEnqueuePropagatesInsertError(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	boom := errors.New("insert blew up")
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(kindDVRLifecycle, "tenant-1", "stream-1", "dvrhash", sqlmock.AnyArg()).
		WillReturnError(boom)

	err = EnqueueDVRLifecycle(&ipcpb.DVRLifecycleData{
		TenantId: sp("tenant-1"), StreamId: sp("stream-1"), DvrHash: "dvrhash",
	})
	if err == nil {
		t.Fatal("expected the insert error to propagate")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped insert error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The fire-and-forget Logged variants are used from background goroutines:
// they MUST still issue the INSERT, and a DB error MUST be swallowed (logged,
// not returned) so a transient outage can't crash a producer goroutine.
func TestEnqueueLoggedSwallowsErrorButStillInserts(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	// Each Logged helper issues exactly one INSERT; we make it fail to prove
	// the error is absorbed (the call returns nothing and does not panic).
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(kindClipLifecycle, "tenant-1", "stream-1", "cliphash", sqlmock.AnyArg()).
		WillReturnError(errors.New("clip insert fail"))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(kindDVRLifecycle, "tenant-1", "stream-1", "dvrhash", sqlmock.AnyArg()).
		WillReturnError(errors.New("dvr insert fail"))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(kindVodLifecycle, "tenant-1", "", "vodhash", sqlmock.AnyArg()).
		WillReturnError(errors.New("vod insert fail"))

	EnqueueClipLifecycleLogged(&ipcpb.ClipLifecycleData{
		TenantId: sp("tenant-1"), StreamId: sp("stream-1"), ClipHash: "cliphash",
	})
	EnqueueDVRLifecycleLogged(&ipcpb.DVRLifecycleData{
		TenantId: sp("tenant-1"), StreamId: sp("stream-1"), DvrHash: "dvrhash",
	})
	EnqueueVodLifecycleLogged(&ipcpb.VodLifecycleData{
		TenantId: sp("tenant-1"), VodHash: "vodhash",
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// claimBatch surfaces a SELECT error to the worker (after retry exhaustion)
// instead of returning a partial/empty batch as success — a read failure must
// not look like "nothing pending".
func TestClaimBatchPropagatesSelectError(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM foghorn\.artifact_event_outbox`).
		WithArgs("60 seconds", batchSize).
		WillReturnError(errors.New("select exploded"))
	mock.ExpectRollback()

	if _, err := claimBatch(context.Background()); err == nil {
		t.Fatal("expected claimBatch to surface the select error")
	}
}

// markCompleted stamps completed_at and clears last_error for exactly the
// passed id — the worker's success commit. We assert the UPDATE targets that
// id so a completed event is never re-dispatched.
func TestMarkCompletedUpdatesById(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	mock.ExpectExec(`SET completed_at = NOW\(\), last_error = NULL`).
		WithArgs("id-42").
		WillReturnResult(sqlmock.NewResult(0, 1))

	markCompleted(context.Background(), "id-42")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// recordFailure persists the incremented attempt count, the failure cause, and
// releases the claim (claimed_at = NULL) so another replica can retry. This is
// the retry-reliability invariant: a failed dispatch is re-eligible, not stuck.
func TestRecordFailurePersistsAttemptsAndReleasesClaim(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	// id, attempts, cause string — claimed_at release is in the SQL text.
	mock.ExpectExec(`SET attempts = \$2, last_error = \$3, claimed_at = NULL`).
		WithArgs("id-7", 3, "decklog down").
		WillReturnResult(sqlmock.NewResult(0, 1))

	recordFailure(context.Background(), "id-7", 3, errors.New("decklog down"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// recordFailure with a nil cause stores an empty error string rather than
// panicking on cause.Error() — defensive for callers that fail without a cause.
func TestRecordFailureNilCause(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	mock.ExpectExec(`SET attempts = \$2, last_error = \$3, claimed_at = NULL`).
		WithArgs("id-8", 1, "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	recordFailure(context.Background(), "id-8", 1, nil)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The store adapter is the bridge between the generic outbox.Worker and this
// package's SQL. ClaimBatch must map every scanned row into an outbox.Claim
// preserving id + attempts + payload, so the worker dispatches and retries the
// right rows.
func TestStoreClaimBatchMapsRows(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	rows := sqlmock.NewRows([]string{
		"id", "event_kind", "tenant_id", "stream_id", "artifact_id", "payload", "attempts", "created_at",
	}).
		AddRow("id-a", kindClipLifecycle, "tenant-1", "stream-1", "h1", `{"tenantId":"tenant-1"}`, 2, time.Unix(1_700_000_000, 0)).
		AddRow("id-b", kindDVRLifecycle, "tenant-1", "stream-2", "h2", `{"tenantId":"tenant-1"}`, 5, time.Unix(1_700_000_001, 0))

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM foghorn\.artifact_event_outbox`).
		WithArgs("60 seconds", batchSize).
		WillReturnRows(rows)
	mock.ExpectExec(`SET claimed_at = NOW`).
		WithArgs(idArray([]string{"id-a", "id-b"})).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	claims, err := store{}.ClaimBatch(context.Background(), batchSize, lease)
	if err != nil {
		t.Fatalf("store.ClaimBatch: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	if claims[0].ID != "id-a" || claims[0].Attempts != 2 || claims[0].Payload.eventKind != kindClipLifecycle {
		t.Fatalf("claim[0] mismapped: %+v", claims[0])
	}
	if claims[1].ID != "id-b" || claims[1].Attempts != 5 {
		t.Fatalf("claim[1] mismapped: %+v", claims[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// store.MarkCompleted / store.RecordFailure delegate to the SQL helpers and
// always return nil (the worker treats bookkeeping as best-effort) while still
// issuing the underlying UPDATE.
func TestStoreMarkCompletedAndRecordFailureDelegate(t *testing.T) {
	resetPackageState(t)
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger(), nil)

	mock.ExpectExec(`SET completed_at = NOW`).
		WithArgs("done-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SET attempts = \$2`).
		WithArgs("fail-1", 4, "boom").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := (store{}).MarkCompleted(context.Background(), "done-1"); err != nil {
		t.Fatalf("MarkCompleted returned err: %v", err)
	}
	if err := (store{}).RecordFailure(context.Background(), "fail-1", 4, nil, errors.New("boom"), 0); err != nil {
		t.Fatalf("RecordFailure returned err: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
