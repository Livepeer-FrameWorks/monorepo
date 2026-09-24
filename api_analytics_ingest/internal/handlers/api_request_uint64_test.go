package handlers

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

// Positions in insertAPIRequest.
const (
	apiRequestErrorCountColumn        = 9
	apiRequestTotalDurationColumn     = 10
	apiRequestLLMInputTokensColumn    = 12
	apiRequestLLMOutputTokensColumn   = 13
	apiRequestUserHashesColumn        = 16
	apiRequestTokenHashesColumn       = 17
	apiRequestGraphQLErrorCountColumn = 23
)

// Decklog puts ServiceEvent payloads on service_events as protojson, which
// renders uint64 fields (scalar and repeated) as JSON strings. The ingest
// must read them back at full uint64 range.
func TestServiceAPIRequestBatchReadsProtojsonUint64Strings(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	bigHash := uint64(math.MaxUint64 - 7)
	batch := &ipcpb.APIRequestBatch{
		Timestamp: time.Now().Unix(), SourceNode: "bridge-1",
		Aggregates: []*ipcpb.APIRequestAggregate{{
			TenantId: tenantID, AuthType: "api_token", OperationType: "query",
			RequestCount: 4, ErrorCount: 2, GraphqlErrorCount: 5,
			TotalDurationMs: 12345, LlmInputTokens: 700, LlmOutputTokens: 80,
			UserHashes: []uint64{bigHash, 42}, TokenHashes: []uint64{9},
		}},
	}
	event := &ipcpb.ServiceEvent{
		EventId: uuid.NewString(), EventType: "api_request_batch", Source: "bridge", TenantId: tenantID,
		Payload: &ipcpb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
	}
	data := serviceBatchData(t, event.GetApiRequestBatch())
	aggs, ok := data["aggregates"].([]interface{})
	if !ok || len(aggs) != 1 {
		t.Fatalf("aggregates = %#v", data["aggregates"])
	}
	if _, isString := aggs[0].(map[string]interface{})["total_duration_ms"].(string); !isString {
		t.Fatalf("protojson rendered total_duration_ms as %T, want string", aggs[0].(map[string]interface{})["total_duration_ms"])
	}

	if err := handler.HandleServiceEvent(kafka.ServiceEvent{
		EventID: event.GetEventId(), EventType: event.GetEventType(), Timestamp: time.Now(), Source: event.GetSource(),
		TenantID: tenantID, Data: data,
	}); err != nil {
		t.Fatalf("HandleServiceEvent: %v", err)
	}

	rows := conn.batches["api_requests"].rows
	if len(rows) != 1 {
		t.Fatalf("api_requests rows = %d, want 1", len(rows))
	}
	row := rows[0]
	checks := map[string]struct {
		col  int
		want interface{}
	}{
		"error_count":         {apiRequestErrorCountColumn, uint32(2)},
		"graphql_error_count": {apiRequestGraphQLErrorCountColumn, uint32(5)},
		"total_duration_ms":   {apiRequestTotalDurationColumn, uint64(12345)},
		"llm_input_tokens":    {apiRequestLLMInputTokensColumn, uint64(700)},
		"llm_output_tokens":   {apiRequestLLMOutputTokensColumn, uint64(80)},
		"user_hashes":         {apiRequestUserHashesColumn, []uint64{bigHash, 42}},
		"token_hashes":        {apiRequestTokenHashesColumn, []uint64{9}},
	}
	for name, c := range checks {
		if got := row[c.col]; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", name, got, c.want)
		}
	}
}

func TestGetUint64FromMapAcceptsStringAndJSONNumber(t *testing.T) {
	data := map[string]interface{}{
		"str":      "18446744073709551615",
		"num":      json.Number("18446744073709551614"),
		"negative": "-1",
		"garbage":  "abc",
	}
	if got := getUint64FromMap(data, "str"); got != math.MaxUint64 {
		t.Errorf("string = %d, want MaxUint64", got)
	}
	if got := getUint64FromMap(data, "num"); got != math.MaxUint64-1 {
		t.Errorf("json.Number = %d, want MaxUint64-1", got)
	}
	if got := getUint64FromMap(data, "negative"); got != 0 {
		t.Errorf("negative = %d, want 0", got)
	}
	if got := getUint64FromMap(data, "garbage"); got != 0 {
		t.Errorf("garbage = %d, want 0", got)
	}
}
