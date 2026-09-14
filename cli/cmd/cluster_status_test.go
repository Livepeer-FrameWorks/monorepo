package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSummarizeServiceReplicas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		replicas     []serviceReplicaStatus
		wantDeployed string
		wantRunning  int
		wantStatus   string
	}{
		{
			name: "converged",
			replicas: []serviceReplicaStatus{
				{Host: "eu-1", Deployed: "v0.3.0", Mode: "docker", Status: "up to date"},
				{Host: "eu-2", Deployed: "v0.3.0", Mode: "docker", Status: "up to date"},
			},
			wantDeployed: "v0.3.0",
			wantRunning:  2,
			wantStatus:   "up to date",
		},
		{
			name: "mixed versions",
			replicas: []serviceReplicaStatus{
				{Host: "eu-1", Deployed: "v0.3.0", Mode: "docker", Status: "up to date"},
				{Host: "eu-2", Deployed: "v0.2.96", Mode: "docker", Status: "upgrade available"},
			},
			wantDeployed: "mixed (v0.2.96, v0.3.0)",
			wantRunning:  2,
			wantStatus:   "mixed versions",
		},
		{
			name: "partial installation",
			replicas: []serviceReplicaStatus{
				{Host: "eu-1", Deployed: "v0.3.0", Mode: "docker", Status: "up to date"},
				{Host: "eu-2", Mode: "docker", Status: "not installed"},
			},
			wantDeployed: "v0.3.0",
			wantRunning:  1,
			wantStatus:   "partially installed (1/2)",
		},
		{
			name: "partial runtime",
			replicas: []serviceReplicaStatus{
				{Host: "eu-1", Deployed: "v0.3.0", Mode: "docker", Status: "up to date"},
				{Host: "eu-2", Deployed: "v0.3.0", Mode: "docker", Status: "not running"},
			},
			wantDeployed: "v0.3.0",
			wantRunning:  1,
			wantStatus:   "partially running (1/2)",
		},
		{
			name: "manifest error",
			replicas: []serviceReplicaStatus{
				{Host: "missing", Mode: "native", Status: "configuration error", Error: "host is not declared"},
			},
			wantRunning: 0,
			wantStatus:  "configuration error (1/1)",
		},
		{
			name: "same version artifact drift",
			replicas: []serviceReplicaStatus{
				{Host: "eu-1", Deployed: "v0.3.0", Mode: "docker", Status: "up to date", ArtifactDigest: "sha256:new"},
				{Host: "eu-2", Deployed: "v0.3.0", Mode: "docker", Status: "artifact drift", ArtifactDigest: "sha256:old"},
			},
			wantDeployed: "v0.3.0",
			wantRunning:  2,
			wantStatus:   "artifact drift (1/2)",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := summarizeServiceReplicas("logbook", "docker", "v0.3.0", tt.replicas)
			if got.Deployed != tt.wantDeployed || got.RunningReplicas != tt.wantRunning || got.Status != tt.wantStatus {
				t.Fatalf("summary = %+v, want deployed=%q running=%d status=%q", got, tt.wantDeployed, tt.wantRunning, tt.wantStatus)
			}
			if got.TotalReplicas != len(tt.replicas) || len(got.Replicas) != len(tt.replicas) {
				t.Fatalf("summary lost replica detail: %+v", got)
			}
		})
	}
}

func TestPrintReplicaDiscrepanciesNamesHost(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	printReplicaDiscrepancies(cmd, []serviceStatus{{
		Name:          "logbook",
		Status:        "mixed versions",
		TotalReplicas: 2,
		Replicas: []serviceReplicaStatus{
			{Host: "regional-eu-1", Deployed: "v0.3.0", Status: "up to date"},
			{Host: "regional-eu-2", Deployed: "v0.2.96", Status: "upgrade available"},
		},
	}})

	got := out.String()
	if !strings.Contains(got, "logbook@regional-eu-2: v0.2.96 (upgrade available)") {
		t.Fatalf("replica discrepancy omitted actionable host detail: %q", got)
	}
	if strings.Contains(got, "regional-eu-1") {
		t.Fatalf("converged replica should not be reported as a discrepancy: %q", got)
	}
}
