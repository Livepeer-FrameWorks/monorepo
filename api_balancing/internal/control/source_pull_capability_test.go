package control

import (
	"net/url"
	"testing"
)

func TestSourcePullCredentialTransportAndRedaction(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "source-pull-test-secret")
	pull := OutboundPull{TenantID: "tenant", AttemptID: "attempt", SourceNodeID: "origin", SourceMediaClusterID: "source",
		SourceGeneration: "generation", SourceRevision: 1, DestClusterID: "destination", DestNodeID: "replica", DTSCURL: "dtsc://origin:4200/live+stream"}
	signed, err := SourcePullURL(pull.DTSCURL, "stream", pull)
	if err != nil {
		t.Fatal(err)
	}
	if SourcePullCredential(signed) == "" || SourcePullBaseURL(signed) != pull.DTSCURL {
		t.Fatal("source credential was lost or source identity changed")
	}
	for _, raw := range []string{signed + "&token=other", "https://origin/live+stream?token=value", "dtsc://user:password@origin/live+stream?token=value", signed + "#fragment", signed + "&bad=%zz"} {
		if SourcePullCredential(raw) != "" {
			t.Fatal("malformed source transport carried a credential")
		}
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("sync", "1")
	u.RawQuery = query.Encode()
	if SourcePullBaseURL(u.String()) != pull.DTSCURL+"?sync=1" {
		t.Fatal("credential redaction discarded significant non-credential parameters")
	}
	if _, err := SourcePullURL("dtsc://another/live+stream", "stream", pull); err == nil {
		t.Fatal("source credential was minted for a different media URL")
	}
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "")
	if _, err := SourcePullURL(pull.DTSCURL, "stream", pull); err == nil {
		t.Fatal("missing signing key returned an uncredentialed fallback")
	}
}
