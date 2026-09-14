package provisioner

import (
	"strings"
	"testing"
)

func TestYugabytePartialMasterStateFailsClosed(t *testing.T) {
	service := databaseRoleTaskFile(t, "yugabyte", "service.yml")
	for _, forbidden := range []string{
		"Recover from stale partial yb-master bootstrap",
		"Remove stale master metadata directories",
		"yugabyte_pg_data_dir",
	} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("Yugabyte service role must not contain automatic destructive recovery %q", forbidden)
		}
	}
	for _, required := range []string{
		"Refuse automatic recovery of partial yb-master state",
		"Refusing to delete database state or generate",
		"not yugabyte_master_consensus_meta_stat.stat.exists",
	} {
		if !strings.Contains(service, required) {
			t.Fatalf("Yugabyte service role missing fail-closed contract %q", required)
		}
	}
}
