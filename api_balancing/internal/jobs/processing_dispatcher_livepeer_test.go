package jobs

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestPrepareProcessingDispatchConfigBindsAssignedJob(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "dispatch-secret")
	now := time.Unix(1_800_000_000, 0).UTC()
	authoritative := `[{"process":"Livepeer","workload":"vod","deadline_ms":30000,"min_speed":0.5,"frameworks_gateway_cluster_ids":["gateway-eu"],"target_profiles":[{"name":"360p","height":360,"bitrate":900000}]}]`
	job := &processingJob{
		JobID: "job-7", TenantID: "tenant-1", RetryCount: 3,
		ArtifactHash: sql.NullString{String: "artifact-7", Valid: true},
	}

	delivery, err := prepareProcessingDispatchConfig(authoritative, job, "edge-2", "media-eu", now)
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if unmarshalErr := json.Unmarshal([]byte(delivery), &processes); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	token, _ := processes[0]["job_token"].(string)
	claims, err := control.VerifyTranscodeJobToken("dispatch-secret", token, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ManifestID != "processing+artifact-7" || claims.JobID != "job-7" ||
		claims.AttemptOrGeneration != "3" || claims.Session != "job-7" ||
		claims.NodeID != "edge-2" || claims.ClusterID != "media-eu" || claims.TenantID != "tenant-1" {
		t.Fatalf("unexpected processing claims: %+v", claims)
	}
	if token == "" || mist.HasLivepeerProcesses(authoritative) && authoritative == delivery {
		t.Fatal("Livepeer delivery was not stamped")
	}
	if mist.StripLivepeerJobToken(authoritative) != authoritative {
		t.Fatal("authoritative config unexpectedly contains a token")
	}
}

func TestPrepareProcessingDispatchConfigFailsClosedWithoutBinding(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "dispatch-secret")
	config := `[{"process":"Livepeer","workload":"vod","frameworks_gateway_cluster_ids":["gateway-eu"],"target_profiles":[{"height":360}]}]`
	job := &processingJob{JobID: "job", TenantID: "tenant"}
	if _, err := prepareProcessingDispatchConfig(config, job, "edge", "media", time.Now()); err == nil {
		t.Fatal("accepted Livepeer dispatch without an artifact manifest")
	}
}
