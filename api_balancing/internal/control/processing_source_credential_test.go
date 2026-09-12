package control

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProcessingSourceCredential_AdmitsOnlyTheMintedRead(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "unit-secret")
	now := time.Unix(1_800_000_000, 0)
	token := ProcessingSourceCredential("tenant-1", "live+stream-1", "edge-node-1", "abc123", now.Add(5*time.Minute))
	if token == "" || !strings.HasPrefix(token, "fwproc.abc123.") {
		t.Fatalf("unexpected credential %q", token)
	}
	requestURL := "http://mistserver:8080/live+stream-1.mkv?audio=all&rate=0&" + SourcePullCredentialParameter + "=" + url.QueryEscape(token)

	hash, ok := AcceptedProcessingSourceRead(requestURL, "tenant-1", "stream-1", "edge-node-1", now)
	if !ok || hash != "abc123" {
		t.Fatalf("expected the minted read to be admitted, got ok=%v hash=%q", ok, hash)
	}
	if _, ok := AcceptedProcessingSourceRead("/live+stream-1.mkv?"+SourcePullCredentialParameter+"="+url.QueryEscape(token), "tenant-1", "live+stream-1", "edge-node-1", now); !ok {
		t.Fatal("a path-only request URL with the credential must be admitted")
	}

	refused := map[string][4]string{
		"other tenant": {requestURL, "tenant-2", "stream-1", "edge-node-1"},
		"other stream": {requestURL, "tenant-1", "stream-2", "edge-node-1"},
		"other node":   {requestURL, "tenant-1", "stream-1", "edge-node-2"},
		"tampered":     {strings.Replace(requestURL, "fwproc.abc123.", "fwproc.abc124.", 1), "tenant-1", "stream-1", "edge-node-1"},
		"duplicated":   {requestURL + "&" + SourcePullCredentialParameter + "=" + url.QueryEscape(token), "tenant-1", "stream-1", "edge-node-1"},
		"no token":     {"http://mistserver:8080/live+stream-1.mkv?audio=all", "tenant-1", "stream-1", "edge-node-1"},
		"pull prefix":  {strings.Replace(requestURL, "fwproc.", "fwsrc.", 1), "tenant-1", "stream-1", "edge-node-1"},
	}
	for name, c := range refused {
		if _, ok := AcceptedProcessingSourceRead(c[0], c[1], c[2], c[3], now); ok {
			t.Errorf("%s: expected refusal", name)
		}
	}
	if _, ok := AcceptedProcessingSourceRead(requestURL, "tenant-1", "stream-1", "edge-node-1", now.Add(5*time.Minute)); ok {
		t.Error("expired credential must be refused")
	}
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "rotated")
	if _, ok := AcceptedProcessingSourceRead(requestURL, "tenant-1", "stream-1", "edge-node-1", now); ok {
		t.Error("credential signed with another secret must be refused")
	}
}

func TestProcessingSourceCredential_RequiresSecretAndIdentity(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "")
	if got := ProcessingSourceCredential("tenant-1", "live+stream-1", "edge-node-1", "abc123", time.Now().Add(time.Minute)); got != "" {
		t.Fatalf("no secret must mint nothing, got %q", got)
	}
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "unit-secret")
	for name, args := range map[string][4]string{
		"tenant":     {"", "live+stream-1", "edge-node-1", "abc123"},
		"stream":     {"tenant-1", "", "edge-node-1", "abc123"},
		"node":       {"tenant-1", "live+stream-1", "", "abc123"},
		"hash":       {"tenant-1", "live+stream-1", "edge-node-1", ""},
		"dotted has": {"tenant-1", "live+stream-1", "edge-node-1", "a.b"},
	} {
		if got := ProcessingSourceCredential(args[0], args[1], args[2], args[3], time.Now().Add(time.Minute)); got != "" {
			t.Errorf("%s: expected no credential, got %q", name, got)
		}
	}
}

func TestRedactSourcePullCredential_StripsProcessingCredential(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "unit-secret")
	token := ProcessingSourceCredential("tenant-1", "live+stream-1", "edge-node-1", "abc123", time.Now().Add(time.Minute))
	raw := "http://mistserver:8080/live+stream-1.mkv?rate=0&" + SourcePullCredentialParameter + "=" + url.QueryEscape(token)
	got := RedactSourcePullCredential(raw)
	if strings.Contains(got, "fwproc.") || !strings.Contains(got, "rate=0") {
		t.Fatalf("processing credential must be stripped and other parameters kept, got %q", got)
	}
	tenantToken := "http://mistserver:8080/live+stream-1/index.m3u8?" + SourcePullCredentialParameter + "=viewer-token"
	if RedactSourcePullCredential(tenantToken) != tenantToken {
		t.Fatal("a tenant playback token must be left byte-identical")
	}
}
