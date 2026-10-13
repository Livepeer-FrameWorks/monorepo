package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
)

func goLivepeerPin(tag string) *gitops.ExternalDependency {
	return &gitops.ExternalDependency{Name: "go-livepeer", ReleaseTag: tag, Binaries: []gitops.ExternalBinary{
		{Name: "livepeer-linux-amd64.tar.gz", URL: "https://example.invalid/" + tag + "/livepeer-linux-amd64.tar.gz", Checksum: "sha256:" + tag + "amd64"},
		{Name: "livepeer-linux-arm64.tar.gz", URL: "https://example.invalid/" + tag + "/livepeer-linux-arm64.tar.gz", Checksum: "sha256:" + tag + "arm64"},
	}}
}

func installedState(version string, pin *gitops.ExternalDependency) *detect.ServiceState {
	state := &detect.ServiceState{Exists: true, Running: true, Version: version, Metadata: map[string]string{}}
	if pin != nil {
		bin := pin.Binaries[0]
		state.Metadata[detect.InstallArtifactSentinelsKey] = detect.InstallIdentity(bin.Checksum, bin.URL)
	}
	return state
}

// A Livepeer gateway still running the previous release's go-livepeer reports
// the upgrade the release pins, the way `cluster diff` reports its binary
// drift, instead of "unknown" and "up to date".
func TestPinnedDependencyReplicaStatusFollowsTheInstalledArtifact(t *testing.T) {
	pin := goLivepeerPin("v0.3.5")
	for _, tc := range []struct {
		name         string
		state        *detect.ServiceState
		wantDeployed string
		wantStatus   string
	}{
		{"previous release installed before install records", installedState("", goLivepeerPin("v0.3.4")), "", "upgrade available"},
		{"previous release with its record", installedState("v0.3.4", goLivepeerPin("v0.3.4")), "v0.3.4", "upgrade available"},
		{"pinned artifact installed before install records", installedState("", pin), "v0.3.5", "up to date"},
		{"pinned artifact with its record", installedState("v0.3.5", pin), "v0.3.5", "up to date"},
		{"record without sentinel, older label", installedState("v0.3.4", nil), "v0.3.4", "upgrade available"},
		{"nothing identifies the artifact", installedState("", nil), "", "unverified"},
	} {
		got := pinnedDependencyReplicaStatus(serviceReplicaStatus{Host: "gw-1", Deployed: tc.state.Version}, tc.state, pin)
		if got.Deployed != tc.wantDeployed || got.Status != tc.wantStatus {
			t.Errorf("%s: deployed %q status %q, want %q %q", tc.name, got.Deployed, got.Status, tc.wantDeployed, tc.wantStatus)
		}
	}
}

func TestSummarizePinnedDependencyReplicas(t *testing.T) {
	pin := goLivepeerPin("v0.3.5")
	old := pinnedDependencyReplicaStatus(serviceReplicaStatus{Host: "gw-1"}, installedState("", goLivepeerPin("v0.3.4")), pin)
	current := pinnedDependencyReplicaStatus(serviceReplicaStatus{Host: "gw-2"}, installedState("", pin), pin)
	summary := summarizeServiceReplicas("livepeer-gateway-eu", "native", pin.ReleaseTag, []serviceReplicaStatus{old, current})
	if summary.RunningReplicas != 2 || summary.Status != "upgrade available" || summary.Available != "v0.3.5" {
		t.Fatalf("summary = %+v, want 2 running replicas with an upgrade to v0.3.5 available", summary)
	}
	unverified := pinnedDependencyReplicaStatus(serviceReplicaStatus{Host: "gw-3"}, installedState("", nil), pin)
	summary = summarizeServiceReplicas("livepeer-gateway-eu", "native", pin.ReleaseTag, []serviceReplicaStatus{current, unverified})
	if summary.RunningReplicas != 2 || !strings.HasPrefix(summary.Status, "unverified") {
		t.Fatalf("summary = %+v, want running replicas reported unverified", summary)
	}
}
