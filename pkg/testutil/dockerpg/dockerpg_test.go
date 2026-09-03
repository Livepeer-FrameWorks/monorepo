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

func TestInfrastructureContractImageRequiresPinnedPair(t *testing.T) {
	image, digest, err := infrastructureContractImage(`
infrastructure:
  - name: yugabyte
    contract_image: yugabytedb/yugabyte:2025
    contract_digest: sha256:abc123
`, "yugabyte")
	if err != nil {
		t.Fatal(err)
	}
	if image != "yugabytedb/yugabyte:2025" || digest != "sha256:abc123" {
		t.Fatalf("resolved %q@%q", image, digest)
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

func TestSharedYugabyteDatabaseNameIsSafeAndBounded(t *testing.T) {
	name := sharedYugabyteDatabaseName("Navigator Query/Catalog with a deliberately overlong suffix that cannot fit", 42, 7)
	if len(name) > 63 {
		t.Fatalf("database name has %d bytes, want at most 63: %q", len(name), name)
	}
	if name != "navigator_query_catalog_with_a_deliberately_overlong_suffi_42_7" {
		t.Fatalf("database name = %q", name)
	}
	if got := sharedYugabyteDatabaseName("---", 1, 2); got != "contract_1_2" {
		t.Fatalf("empty normalized prefix = %q", got)
	}
}
