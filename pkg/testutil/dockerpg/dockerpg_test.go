package dockerpg

import "testing"

func TestInfrastructureImageRequiresPinnedPair(t *testing.T) {
	image, digest, err := infrastructureImage(`
infrastructure:
  - name: postgresql
    image: pgvector/pgvector:pg18
    digest: sha256:abc123
  - name: clickhouse
    image: clickhouse/clickhouse-server:26
    digest: sha256:def456
`, "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if image != "pgvector/pgvector:pg18" || digest != "sha256:abc123" {
		t.Fatalf("resolved %q@%q", image, digest)
	}
	if _, _, err := infrastructureImage("- name: postgresql\n  image: postgres:18\n", "postgresql"); err == nil {
		t.Fatal("accepted infrastructure image without digest")
	}
}

func TestParseInspectedHostPort(t *testing.T) {
	ports := `{"5432/tcp":[{"HostIp":"0.0.0.0","HostPort":"49153"},{"HostIp":"::","HostPort":"49153"}],"8080/tcp":null}`
	if got := parseInspectedHostPort(ports, "5432/tcp"); got != "49153" {
		t.Fatalf("host port = %q, want 49153", got)
	}
	if got := parseInspectedHostPort(ports, "8080/tcp"); got != "" {
		t.Fatalf("unpublished port = %q, want empty", got)
	}
	if got := parseInspectedHostPort("not-json", "5432/tcp"); got != "" {
		t.Fatalf("invalid JSON port = %q, want empty", got)
	}
}
