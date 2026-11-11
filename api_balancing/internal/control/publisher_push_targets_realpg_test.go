//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func testPublisherPushTargetConfirmation(t *testing.T, conn *sql.DB) {
	t.Helper()
	priorDB, priorEncryptor := db, admissionEffectEncryptor
	SetDB(conn)
	t.Cleanup(func() { SetDB(priorDB); admissionEffectEncryptor = priorEncryptor })
	if err := ConfigureAdmissionEffectEncryption("publisher-target-confirmation-fixture"); err != nil {
		t.Fatal(err)
	}
	req := IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: uuid.NewString(), InternalName: uuid.NewString(), ConnectorPID: 100,
		TriggerUUID: uuid.NewString(), StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a"}
	repository := NewPublisherAdmissionRepository(conn)
	prepared, err := repository.Prepare(t.Context(), req, logging.NewLogger())
	if err != nil || prepared.Outcome != IngestSessionActive {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	activation := &ipcpb.ActivatePushTargets{TargetRevision: 7, ActivationAttempt: uuid.NewString(), Targets: []*ipcpb.PushTargetSpec{{TargetId: uuid.NewString(), TargetUri: "rtmp://restream.test/live/fixture"}}}
	raw, err := proto.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewStreamRegistry(nil, "cell-a", time.Minute)
	if applied, _, err := repository.Project(t.Context(), registry, req, prepared, AdmissionEffectIntent{PushTargets: raw}); err != nil || !applied {
		t.Fatalf("push-target confirmation must commit: applied=%v error=%v", applied, err)
	}
	resumed, err := repository.Prepare(t.Context(), req, logging.NewLogger())
	if err != nil || !resumed.Resumed || resumed.Generation != prepared.Generation {
		t.Fatalf("replay lost confirmed generation: %+v %v", resumed, err)
	}
	if applied, _, err := repository.Project(t.Context(), registry, req, resumed, AdmissionEffectIntent{PushTargets: raw}); err != nil || !applied {
		t.Fatalf("replayed confirmation: %v %v", applied, err)
	}
	var effects, revisions int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.ingest_admission_effects WHERE tenant_id=$1::uuid AND source_generation=$2::uuid`, req.TenantID, prepared.Generation).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.admission_push_target_revisions WHERE tenant_id=$1::uuid AND source_generation=$2::uuid AND target_revision=$3 AND activation_attempt=$4::uuid`, req.TenantID, prepared.Generation, activation.TargetRevision, activation.ActivationAttempt).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || revisions != 1 {
		t.Fatalf("confirmation/replay effects=%d target revisions=%d, want 1/1", effects, revisions)
	}
}
