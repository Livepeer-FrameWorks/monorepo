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
