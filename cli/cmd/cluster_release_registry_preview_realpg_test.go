//go:build schema_verify

package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
)

func TestRegistryRetirementPreviewBeforeAndAfterOwnershipExpandRealPG(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker required")
	}
	name := fmt.Sprintf("fw-registry-preview-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start Postgres: %v %s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE quartermaster"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/quartermaster?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE SCHEMA quartermaster;
 CREATE TABLE quartermaster.services(service_id text PRIMARY KEY,name text NOT NULL);
 CREATE TABLE quartermaster.service_instances(instance_id text PRIMARY KEY,service_id text,cluster_id text,node_id text,protocol text,port integer,status text);
 INSERT INTO quartermaster.services VALUES('clickhouse','clickhouse');
 INSERT INTO quartermaster.service_instances VALUES('legacy-reviewed','clickhouse','core','old','tcp',9000,'running'),('unreviewed','clickhouse','core','other','tcp',9000,'running');`); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\nexec " + ssh.ShellQuote(docker) + " exec -i " + ssh.ShellQuote(name) + " psql \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := &cobra.Command{}
	rc := &resolvedCluster{Manifest: postExpandReportManifest()}
	scope := &bootstrap.ServiceRegistryScope{Owner: "production", Clusters: []string{"core"}, Complete: true,
		Adopt: []bootstrap.ServiceRegistryAdoption{{InstanceID: "legacy-reviewed", ServiceName: "clickhouse", ClusterID: "core", NodeID: "old", Protocol: "tcp", Port: 9000}},
	}
	for _, expanded := range []bool{false, true} {
		if expanded {
			if _, err := db.ExecContext(t.Context(), `ALTER TABLE quartermaster.service_instances ADD COLUMN registration_source text DEFAULT 'legacy', ADD COLUMN registry_owner text;
    INSERT INTO quartermaster.service_instances VALUES('managed-removed','clickhouse','core','old-2','tcp',9000,'running','manifest','production');`); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := readReleaseRegistryInstances(t.Context(), cmd, rc, nil, scope)
		if err != nil {
			t.Fatalf("expanded=%v read: %v", expanded, err)
		}
		stopped, err := registryRetirementPreview(bootstrap.QuartermasterSection{RegistryScope: scope}, rows)
		want := 1
		if expanded {
			want = 2
		}
		if err != nil || len(stopped) != want {
			t.Fatalf("expanded=%v preview=%+v err=%v", expanded, stopped, err)
		}
	}
	var changed int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM quartermaster.service_instances WHERE status<>'running'").Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("preview mutated inventory: %d %v", changed, err)
	}
}
