package federation

import (
	"strings"
	"testing"

	"frameworks/api_balancing/internal/control"
)

// Federation events are persisted and read back by operators, so a source-pull
// credential must never travel in one. The stored pull URL keeps its credential
// because the destination needs it to dial; the event carries only the media
// path. This is a regression guard: the sibling ORIGIN_PULL_ARRANGED emit was
// written redacted from the start and this one was not.
func TestOriginPullCompletedEventCarriesNoSourceCredential(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "origin-pull-event-test-secret")
	const base = "dtsc://origin.example:4200/live+demo"
	pull := control.OutboundPull{
		AttemptID: "attempt-1", TenantID: "tenant-a", SourceNodeID: "origin-1",
		DestNodeID: "edge-1", DestClusterID: "cluster-b", DTSCURL: base,
	}
	credentialed, err := control.SourcePullURL(base, "live+demo", pull)
	if err != nil {
		t.Fatalf("issue source pull credential: %v", err)
	}
	if !strings.Contains(credentialed, "token=fwsrc.") {
		t.Fatalf("fixture is not a credentialed URL: %q", credentialed)
	}

	event := originPullCompletedEvent("live+demo", control.Location{
		DestNodeID: "edge-1", PullSourceNodeID: "origin-1", PullDTSCURL: credentialed,
		ReplicatingFrom: "cell-a",
	}, "cluster-a", "tenant-a")

	if got := event.GetDtscUrl(); got != base {
		t.Fatalf("federation event dtsc_url = %q, want the credential-free %q", got, base)
	}
	for _, marker := range []string{"token=", "fwsrc."} {
		if strings.Contains(event.String(), marker) {
			t.Fatalf("federation event payload leaks %q: %s", marker, event.String())
		}
	}
}
