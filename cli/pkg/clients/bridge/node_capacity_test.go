package bridge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeCapacityReadWriteProtocol(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer user-session" {
			t.Error("user credential not attached")
		}
		var body struct {
			Query     string                     `json:"query"`
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls == 1 {
			var id string
			if err := json.Unmarshal(body.Variables["id"], &id); err != nil {
				t.Error(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(id)
			if err != nil || string(decoded) != "InfrastructureNode:edge" {
				t.Errorf("wrong node global ID: %s %v", decoded, err)
			}
			_, _ = w.Write([]byte(`{"data":{"node":{"capacity":{"egressBudgetBytesPerSec":125000000,"ingressBudgetBytesPerSec":250000000,"egressSource":"DEFAULT","ingressSource":"OPERATOR","revision":7,"appliedRevision":6}}}}`))
		} else {
			var input map[string]any
			if err := json.Unmarshal(body.Variables["input"], &input); err != nil {
				t.Error(err)
			}
			if input["expectedRevision"] != float64(7) || input["egressBudgetBytesPerSec"] != nil || input["ingressBudgetBytesPerSec"] != float64(250000000) {
				t.Errorf("write lost CAS/reset: %v", input)
			}
			_, _ = w.Write([]byte(`{"data":{"setNodeCapacity":{"__typename":"InfrastructureNode","capacity":{"egressBudgetBytesPerSec":125000000,"ingressBudgetBytesPerSec":250000000,"egressSource":"DEFAULT","ingressSource":"OPERATOR","revision":8,"appliedRevision":7}}}}`))
		}
	}))
	defer server.Close()
	client := NewWithRefresh(server.URL, nil)
	current, err := client.GetNodeCapacity(t.Context(), "user-session", "edge")
	if err != nil || current.AppliedRevision == nil || *current.AppliedRevision != 6 || current.EgressSource != "DEFAULT" {
		t.Fatalf("read=%v %v", current, err)
	}
	budget := uint64(250000000)
	updated, err := client.SetNodeCapacity(t.Context(), "user-session", "edge", nil, &budget, current.Revision)
	if err != nil || updated.Revision != 8 {
		t.Fatalf("write=%v %v", updated, err)
	}
}
