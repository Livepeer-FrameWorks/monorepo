package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/gin-gonic/gin"
)

func TestStreamRegistryDebugPreservesPhysicalSourceIdentities(t *testing.T) {
	previous := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "us-cell", time.Minute)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(previous) })
	pull, err := r.RecordInboundPull(context.Background(), "stream", control.InboundPull{
		TenantID: "tenant", SourceClusterID: "eu-cell", SourceMediaClusterID: "eu-media", SourceNodeID: "publisher",
		SourceGeneration: "generation", SourceRevision: 9007199254740993,
		DestClusterID: "us-media", DestNodeID: "edge", DTSCURL: "dtsc://publisher/live+stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/debug/stream-registry?include=replications", nil)
	HandleStreamRegistry(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("debug request failed: %d", recorder.Code)
	}
	var body struct {
		Replications []map[string]any `json:"local_replications"`
	}
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &body); decodeErr != nil || len(body.Replications) != 1 {
		t.Fatalf("decode replication: %+v, %v", body, decodeErr)
	}
	for field, value := range map[string]string{
		"tenant_id": "tenant", "source_cell_id": "eu-cell", "source_media_cluster_id": "eu-media",
		"dest_cluster_id": "us-media", "dest_node_id": "edge", "pull_source_node_id": "publisher",
		"attempt_id": pull.AttemptID, "source_generation": "generation", "source_revision": "9007199254740993",
	} {
		if body.Replications[0][field] != value {
			t.Fatalf("debug field %s conflated or rounded: got %v, want %s", field, body.Replications[0][field], value)
		}
	}
}
