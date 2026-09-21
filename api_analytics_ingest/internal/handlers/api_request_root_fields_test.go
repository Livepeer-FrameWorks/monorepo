package handlers

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

// apiRequestRootFieldsColumn is root_fields' position in insertAPIRequest.
const apiRequestRootFieldsColumn = 22

// serviceBatchData renders a batch the way Decklog puts a ServiceEvent payload
// on service_events (protojson with proto field names).
func serviceBatchData(t *testing.T, batch *ipcpb.APIRequestBatch) map[string]interface{} {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestServiceAPIRequestBatchWritesRootFields(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	batch := &ipcpb.APIRequestBatch{
		Timestamp: time.Now().Unix(), SourceNode: "bridge-1",
		Aggregates: []*ipcpb.APIRequestAggregate{
			{TenantId: tenantID, AuthType: "api_token", OperationType: "query", RequestCount: 3, RootFields: []string{"clips", "streams"}},
			{TenantId: tenantID, AuthType: "api_token", OperationType: "query", RequestCount: 1},
		},
	}
	if err := handler.HandleServiceEvent(kafka.ServiceEvent{
		EventID: uuid.NewString(), EventType: "api_request_batch", Timestamp: time.Now(), Source: "bridge",
		TenantID: tenantID, Data: serviceBatchData(t, batch),
	}); err != nil {
		t.Fatalf("HandleServiceEvent: %v", err)
	}

	rows := conn.batches["api_requests"].rows
	if len(rows) != 2 {
		t.Fatalf("api_requests rows = %d, want 2", len(rows))
	}
	if got := rows[0][apiRequestRootFieldsColumn]; !reflect.DeepEqual(got, []string{"clips", "streams"}) {
		t.Fatalf("first row root_fields = %#v, want [clips streams]", got)
	}
	// An aggregate without root fields (an older Bridge, or MCP) stores an
	// empty array, which the Array column needs instead of NULL.
	if got := rows[1][apiRequestRootFieldsColumn]; !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("second row root_fields = %#v, want []", got)
	}
}

func TestAnalyticsAPIRequestBatchWritesRootFields(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := buildAPIRequestBatchEvent(t, []*ipcpb.APIRequestAggregate{
		{TenantId: uuid.NewString(), AuthType: "jwt", OperationType: "mutation", RequestCount: 1, RootFields: []string{"createStream"}},
	})
	if err := handler.processAPIRequestBatch(context.Background(), event); err != nil {
		t.Fatalf("processAPIRequestBatch: %v", err)
	}
	rows := conn.batches["api_requests"].rows
	if len(rows) != 1 {
		t.Fatalf("api_requests rows = %d, want 1", len(rows))
	}
	if got := rows[0][apiRequestRootFieldsColumn]; !reflect.DeepEqual(got, []string{"createStream"}) {
		t.Fatalf("root_fields = %#v, want [createStream]", got)
	}
}

// A service event's actor lands in api_events' actor columns, the same columns
// domain events fill, so Bridge's audit rows join API usage on token hash.
func TestServiceEventAuditWritesActor(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	if err := handler.HandleServiceEvent(kafka.ServiceEvent{
		EventID: uuid.NewString(), EventType: "api_clip_created", Timestamp: time.Now(), Source: "bridge",
		TenantID: uuid.NewString(), UserID: uuid.NewString(), ResourceType: "clip", ResourceID: "clip-1",
		Data: map[string]interface{}{"status": "requested"}, ActorAuthType: "api_token", ActorTokenHash: 4242,
	}); err != nil {
		t.Fatalf("HandleServiceEvent: %v", err)
	}
	rows := conn.batches["api_events"].rows
	if len(rows) != 1 {
		t.Fatalf("api_events rows = %d, want 1", len(rows))
	}
	if authType, tokenHash := rows[0][14], rows[0][15]; authType != "api_token" || tokenHash != uint64(4242) {
		t.Fatalf("actor columns = (%#v, %#v), want (api_token, 4242)", authType, tokenHash)
	}
}
