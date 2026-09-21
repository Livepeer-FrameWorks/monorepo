package provisioner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
)

func TestRestoreRecoveryNameSets(t *testing.T) {
	s := DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}
	for mask := range 8 {
		live, previous, shadow := mask&1 != 0, mask&2 != 0, mask&4 != 0
		t.Run(fmt.Sprintf("rollback/%03b", mask), func(t *testing.T) {
			r := &scriptedRunner{outputs: []string{boolCount(previous), boolCount(live), boolCount(shadow)}}
			err := RollbackRestoredDatabase(context.Background(), r, s, "purser")
			wantOK := previous && (!live || !shadow)
			if (err == nil) != wantOK {
				t.Fatalf("rollback = %v, want success %v", err, wantOK)
			}
			commands := strings.Join(r.commands, "\n")
			if strings.Contains(commands, "DROP DATABASE") {
				t.Fatal("rollback must never delete a surviving copy")
			}
			if !wantOK && strings.Contains(commands, "ALTER DATABASE") {
				t.Fatal("refused state must remain untouched")
			}
			if wantOK && !strings.Contains(commands, `ALTER DATABASE "purser__prerestore" RENAME TO "purser"`) {
				t.Fatal("original database was not restored")
			}
		})
		t.Run(fmt.Sprintf("finish/%03b", mask), func(t *testing.T) {
			r := &scriptedRunner{outputs: []string{boolCount(live)}}
			err := FinishRestoredDatabase(context.Background(), r, s, "purser")
			if (err == nil) != live {
				t.Fatalf("finish = %v, live exists %v", err, live)
			}
			if !live && strings.Contains(strings.Join(r.commands, "\n"), "DROP DATABASE") {
				t.Fatal("finish deleted recovery copies without a live database")
			}
		})
	}
}

func boolCount(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func TestClickHouseRestorePreflightPreservesLeftovers(t *testing.T) {
	for _, name := range []string{ClickHousePreviousTable("api_requests"), clickHousePreparingTable("api_requests")} {
		r := &restoreTableRunner{tables: map[string][]string{name: {"202609"}}}
		err := PreflightClickHouseRestore(context.Background(), r, ClickHouseServer{Database: "periscope"}, "api_requests")
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("preflight with %s = %v", name, err)
		}
		if len(r.tables) != 1 {
			t.Fatalf("preflight mutated recovery tables: %v", r.tables)
		}
	}
}
