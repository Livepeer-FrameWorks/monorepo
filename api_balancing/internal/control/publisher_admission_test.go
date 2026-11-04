package control

import (
	"context"
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
