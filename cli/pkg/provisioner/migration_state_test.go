package provisioner

import (
	"testing"
)

func TestParseLedgerPipeOutput_TwoRows(t *testing.T) {
	out := "v0.1.0|expand|1|abc123\nv0.1.0|postdeploy|2|def456\n"
	got, err := parseLedgerPipeOutput(out)
	if err != nil {
		t.Fatalf("parseLedgerPipeOutput: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Version != "v0.1.0" || got[0].Phase != "expand" || got[0].Seq != 1 || got[0].Checksum != "abc123" {
		t.Errorf("row 0 wrong: %+v", got[0])
	}
	if got[1].Version != "v0.1.0" || got[1].Phase != "postdeploy" || got[1].Seq != 2 || got[1].Checksum != "def456" {
		t.Errorf("row 1 wrong: %+v", got[1])
	}
}

func TestParseLedgerPipeOutput_BlankLinesIgnored(t *testing.T) {
	out := "\nv0.1.0|expand|1|abc\n\n\n"
	got, err := parseLedgerPipeOutput(out)
	if err != nil {
		t.Fatalf("parseLedgerPipeOutput: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}

func TestParseLedgerPipeOutput_TrailingCR(t *testing.T) {
	out := "v0.1.0|expand|1|abc\r\nv0.1.0|expand|2|def\r\n"
	got, err := parseLedgerPipeOutput(out)
	if err != nil {
		t.Fatalf("parseLedgerPipeOutput: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Checksum != "abc" || got[1].Checksum != "def" {
		t.Errorf("checksum strip CR failed: %+v", got)
	}
}

func TestParseLedgerPipeOutput_WrongFieldCount(t *testing.T) {
	out := "v0.1.0|expand|1\n" // 3 fields, not 4
	_, err := parseLedgerPipeOutput(out)
	if err == nil {
		t.Fatal("want error for wrong field count, got nil")
	}
}

func TestParseLedgerPipeOutput_BadSeq(t *testing.T) {
	out := "v0.1.0|expand|notanint|abc\n"
	_, err := parseLedgerPipeOutput(out)
	if err == nil {
		t.Fatal("want error for bad seq, got nil")
	}
}

func TestParseLedgerPipeOutput_Empty(t *testing.T) {
	got, err := parseLedgerPipeOutput("")
	if err != nil {
		t.Fatalf("parseLedgerPipeOutput empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 entries from empty input, got %d", len(got))
	}
}

func TestIsUndefinedTableOutput(t *testing.T) {
	cases := map[string]bool{
		``: false,
		`ERROR:  relation "_migrations" does not exist`:                                 true,
		`psql:<stdin>:1: ERROR:  relation "_migrations" does not exist at character 38`: true,
		`some other error`:                false,
		`relation "other" does not exist`: false,
	}
	for in, want := range cases {
		if got := isUndefinedTableOutput(in); got != want {
			t.Errorf("isUndefinedTableOutput(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBuildLedgerPsqlCommand(t *testing.T) {
	cmd, err := buildLedgerPsqlCommand("purser")
	if err != nil {
		t.Fatalf("buildLedgerPsqlCommand returned error: %v", err)
	}
	want := `sudo -u postgres psql -tAF '|' -d purser -c "SELECT version, phase, seq, checksum FROM _migrations ORDER BY version, phase, seq"`
	if cmd != want {
		t.Errorf("got:\n%s\nwant:\n%s", cmd, want)
	}
}

func TestBuildLedgerPsqlCommandRejectsUnsafeDatabaseName(t *testing.T) {
	if _, err := buildLedgerPsqlCommand(`purser; touch /tmp/nope`); err == nil {
		t.Fatal("want unsafe database name rejected")
	}
}

func TestBuildMigrationItemsRequiresTarget(t *testing.T) {
	_, err := BuildMigrationItems([]string{"purser"}, "expand", "")
	if err == nil {
		t.Fatal("want error for empty targetVersion, got nil")
	}
}

func TestBuildMigrationItemsRejectsBadPhase(t *testing.T) {
	_, err := BuildMigrationItems([]string{"purser"}, "bogus", "v0.1.0")
	if err == nil {
		t.Fatal("want error for invalid phase, got nil")
	}
}

func TestDiffExpectedAgainstLedger(t *testing.T) {
	expected := []map[string]any{
		{"db": "purser", "version": "v0.3.0", "phase": "expand", "sequence": 1, "checksum": "aaa", "filename": "001.sql"},
		{"db": "purser", "version": "v0.3.0", "phase": "postdeploy", "sequence": 1, "checksum": "bbb", "filename": "001.sql"},
		{"db": "qm", "version": "v0.3.0", "phase": "expand", "sequence": 1, "checksum": "ddd", "filename": "001.sql"},
	}
	ledger := map[string][]LedgerEntry{
		"purser": {
			{Version: "v0.3.0", Phase: "expand", Seq: 1, Checksum: "aaa"},
		},
		"qm": {
			// checksum mismatch
			{Version: "v0.3.0", Phase: "expand", Seq: 1, Checksum: "DRIFTED"},
		},
	}

	missing := diffExpectedAgainstLedger(expected, ledger)
	if len(missing) != 2 {
		t.Fatalf("got %d missing, want 2; %+v", len(missing), missing)
	}

	var sawPostdeploy, sawMismatch bool
	for _, m := range missing {
		if m.Database == "purser" && m.Phase == "postdeploy" && m.MismatchedChecksum == "" {
			sawPostdeploy = true
		}
		if m.Database == "qm" && m.MismatchedChecksum == "DRIFTED" {
			sawMismatch = true
		}
	}
	if !sawPostdeploy {
		t.Error("expected purser postdeploy/1 reported as missing")
	}
	if !sawMismatch {
		t.Error("expected qm expand/1 reported as checksum mismatch")
	}
}

func TestDiffExpectedAgainstLedger_EmptyLedger(t *testing.T) {
	expected := []map[string]any{
		{"db": "purser", "version": "v0.3.0", "phase": "expand", "sequence": 1, "checksum": "aaa", "filename": "001.sql"},
	}
	missing := diffExpectedAgainstLedger(expected, nil)
	if len(missing) != 1 {
		t.Fatalf("empty ledger must yield all expected as missing; got %d", len(missing))
	}
}

func TestMigrationKey_StringWithMismatch(t *testing.T) {
	k := MigrationKey{Database: "purser", Version: "v0.3.0", Phase: "expand", Filename: "001.sql", MismatchedChecksum: "abc"}
	if got := k.String(); got == "" || !contains(got, "mismatch") {
		t.Errorf("expected mismatch in string output, got %q", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) && (containsAt(s, substr))))
}

func containsAt(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
