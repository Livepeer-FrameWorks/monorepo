package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBootstrapEdgeRetainsPendingEnrollment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !strings.Contains(request.Query, "... on BootstrapEdgePending { clusterId phase retryAfterSeconds }") {
			t.Error("CLI enrollment must explicitly request the pending identity and retry interval")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"bootstrapEdge":{"__typename":"BootstrapEdgePending","clusterId":"edge-cluster","phase":"dns","retryAfterSeconds":3}}}`))
	}))
	defer server.Close()

	result, err := New(server.URL).BootstrapEdge(t.Context(), EdgeBootstrapInput{Token: "enrollment"})
	var pending *EdgeBootstrapPending
	if result != nil || !errors.As(err, &pending) {
		t.Fatalf("result=%v err=%v, want pending enrollment", result, err)
	}
	if pending.ClusterID != "edge-cluster" || pending.Phase != "dns" || pending.RetryAfter != 3*time.Second {
		t.Fatalf("pending enrollment lost identity or retry interval: %+v", pending)
	}
}
