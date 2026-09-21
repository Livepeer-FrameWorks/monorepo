package grpc

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestServiceEventForDispatchKeepsOneIDPerRow(t *testing.T) {
	withID, err := protojson.Marshal(&ipcpb.ServiceEvent{EventId: "payload-id", EventType: "tenant_updated"})
	if err != nil {
		t.Fatal(err)
	}
	withoutID, err := protojson.Marshal(&ipcpb.ServiceEvent{EventType: "tenant_updated"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		row  qmOutboxRow
		want string
	}{
		{"stored event id wins", qmOutboxRow{id: "row-id", eventID: "stored-id", payload: withID}, "stored-id"},
		{"payload id without a stored one", qmOutboxRow{id: "row-id", payload: withID}, "payload-id"},
		{"row id for a row written before event_id", qmOutboxRow{id: "row-id", payload: withoutID}, "row-id"},
	} {
		for attempt := 0; attempt < 2; attempt++ {
			event, err := serviceEventForDispatch(tc.row)
			if err != nil {
				t.Fatal(err)
			}
			if event.GetEventId() != tc.want {
				t.Fatalf("%s: attempt %d sent %q, want %q", tc.name, attempt, event.GetEventId(), tc.want)
			}
		}
	}
}

// A forwarded service event is written without a domain event, keeps the
// caller's ID, and is refused when that ID is not a UUID.
func TestEnqueueServiceEventRPCIsLegacyOnly(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	const (
		tenantID = "11111111-1111-4111-8111-111111111111"
		eventID  = "01900000-0000-7000-8000-000000000009"
	)
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(eventID, "tenant_updated", tenantID, "tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-1"))
	raw := func(id string) []byte {
		t.Helper()
		out, err := proto.Marshal(&ipcpb.ServiceEvent{EventId: id, EventType: "tenant_updated", TenantId: tenantID, Source: "deckhand"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := server.EnqueueServiceEvent(serviceCtx(), &quartermasterpb.EnqueueServiceEventRequest{Event: raw(eventID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.EnqueueServiceEvent(serviceCtx(), &quartermasterpb.EnqueueServiceEventRequest{Event: raw("not-a-uuid")}); err == nil {
		t.Fatal("a forwarded event with a non-UUID event_id was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
