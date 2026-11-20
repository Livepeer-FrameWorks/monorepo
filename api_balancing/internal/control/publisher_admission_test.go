package control

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestPublisherPrepareBoundsIncumbentChurn(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	oldGuard := ingestTakeoverNodeGuard
	guards, released := 0, 0
	ingestTakeoverNodeGuard = func(context.Context, string) (func(), error) {
		guards++
		return func() { released++ }, nil
	}
	t.Cleanup(func() { ingestTakeoverNodeGuard = oldGuard })
	for range 3 {
		response := `{"outcome":5,"incumbent_generation":"` + uuid.NewString() + `","incumbent_node":"` + uuid.NewString() + `"}`
		mock.ExpectQuery("SELECT foghorn.prepare_publisher_admission_v1").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow([]byte(response)))
	}
	req := IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: "publisher", InternalName: "stream", ConnectorPID: 1, TriggerUUID: "execution", StartedAtMillis: time.Now().UnixMilli()}
	got, err := NewPublisherAdmissionRepository(conn).Prepare(t.Context(), req, logging.NewLogger())
	if err != nil || got.Outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("incumbent churn must stop after three prepare attempts: %+v %v", got, err)
	}
	if guards != released {
		t.Fatalf("takeover guards leaked: acquired=%d released=%d", guards, released)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The settlement event is consumed after replicas restart; its payload must
// identify the retired generation rather than whichever publisher is current.
func TestPublisherSettlementIdleCarriesGeneration(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	generation := uuid.NewString()
	matcher := publisherIdleGenerationMatcher{generation: generation}
	mock.ExpectQuery("SELECT foghorn.settle_publisher_admission_v1").WithArgs(matcher).WillReturnRows(sqlmock.NewRows([]string{"settled"}).AddRow(false))
	_, err = NewPublisherAdmissionRepository(conn).Settle(t.Context(), IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: "node", InternalName: "stream", TriggerUUID: "execution"}, generation, IngestEndedAdmissionAbandoned)
	if err != nil {
		t.Fatalf("settlement must carry its exact session ID: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type publisherIdleGenerationMatcher struct{ generation string }

func (m publisherIdleGenerationMatcher) Match(value driver.Value) bool {
	data, ok := value.([]byte)
	if !ok {
		return false
	}
	var payload struct {
		Idle events.Event `json:"idle_event"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return false
	}
	var idle publicv1.StreamIdle
	return proto.Unmarshal(payload.Idle.Data, &idle) == nil && idle.GetSessionId() == m.generation
}

func TestPublisherPrepareSuppliesSessionFieldPrefixes(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	generation := uuid.NewString()
	mock.ExpectQuery("SELECT foghorn.prepare_publisher_admission_v1").WithArgs(publisherSessionPrefixMatcher{}).WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow([]byte(`{"outcome":1,"generation":"` + generation + `","source_revision":1}`)))
	_, err = NewPublisherAdmissionRepository(conn).Prepare(t.Context(), IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: "node", InternalName: "stream", ConnectorPID: 1, TriggerUUID: "execution", StartedAtMillis: time.Now().UnixMilli()}, logging.NewLogger())
	if err != nil {
		t.Fatalf("atomic publisher events have no session identity encoder: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type publisherSessionPrefixMatcher struct{}

func (publisherSessionPrefixMatcher) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok {
		return false
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return false
	}
	generation := uuid.NewString()
	for _, kind := range []string{"connected", "idle"} {
		var prefix []byte
		if json.Unmarshal(data[kind+"_session_prefix"], &prefix) != nil {
			return false
		}
		// UUID text has a fixed 36-byte protobuf string encoding.
		var event events.Event
		if json.Unmarshal(data[kind+"_event"], &event) != nil {
			return false
		}
		payload := append(append(event.Data, prefix...), []byte(generation)...)
		if kind == "connected" {
			var msg publicv1.StreamConnected
			if proto.Unmarshal(payload, &msg) != nil || msg.GetSessionId() != generation {
				return false
			}
		} else {
			var msg publicv1.StreamIdle
			if proto.Unmarshal(payload, &msg) != nil || msg.GetSessionId() != generation {
				return false
			}
		}
	}
	return true
}

func TestPublisherStopFenceKeepsCapacityUntilNodeConfirmation(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mock.ExpectBegin()
	mock.ExpectExec("LockIngestStream").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("FenceIngestStopExecution").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	req := IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: "node", InternalName: "stream", TriggerUUID: "claim", ConnectorPID: 123}
	if err := NewPublisherAdmissionRepository(conn).FenceExecution(t.Context(), req, uuid.NewString()); err != nil {
		t.Fatalf("stop fence retired the publisher before its node confirmed termination: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
