package dockerpg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRunDockerPreservesDeadlineError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := runDocker(context.Background(), 50*time.Millisecond, "inspect", "fixture")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("docker timeout = %v, want context deadline evidence", err)
	}
}

func TestWithEphemeralPostgresData(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "postgres fixture",
			args: []string{"run", "-d", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
			want: []string{"run", "--tmpfs", "/var/lib/postgresql", "-d", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
		},
		{
			name: "existing split tmpfs",
			args: []string{"run", "--tmpfs", "/var/lib/postgresql", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
			want: []string{"run", "--tmpfs", "/var/lib/postgresql", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
		},
		{
			name: "existing joined tmpfs",
			args: []string{"run", "--tmpfs=/var/lib/postgresql:rw", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
			want: []string{"run", "--tmpfs=/var/lib/postgresql:rw", "-e", "POSTGRES_PASSWORD=harness", "postgres:18"},
		},
		{
			name: "yugabyte fixture",
			args: []string{"run", "yugabytedb/yugabyte:2025"},
			want: []string{"run", "yugabytedb/yugabyte:2025"},
		},
		{
			name: "non run command",
			args: []string{"inspect", "fixture"},
			want: []string{"inspect", "fixture"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withEphemeralPostgresData(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("withEphemeralPostgresData() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

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

func TestContractEngineImageRequiresPinnedPair(t *testing.T) {
	image, digest, err := contractEngineImage(`
contract_engines:
  - name: yugabyte
    image: yugabytedb/yugabyte:2025
    digest: sha256:abc123
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
