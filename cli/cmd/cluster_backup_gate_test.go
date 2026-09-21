package cmd

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

var gateNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

var gateLedger = []backup.LedgerRow{
	{Version: "v0.3.10", Phase: "expand", Seq: 1, Checksum: "aaa"},
	{Version: "v0.3.11", Phase: "expand", Seq: 1, Checksum: "bbb"},
}

// writeGateBackup stores a real backup of postgres/purser at a local directory: three section files and a manifest
// recording gateLedger, started `age` before gateNow.
func writeGateBackup(t *testing.T, age time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	loc, err := backup.ParseLocation(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db := backup.Database{Name: "purser", Engine: backup.EngineYugabyte, Owner: "purser", Ledger: gateLedger, LedgerDigest: backup.LedgerDigest(gateLedger), RowCounts: map[string]int64{"purser.invoices": 2}}
	for _, section := range backup.Sections {
		file, storeErr := backup.StoreFile(ctx, loc, db.Key()+"/"+section+".sql.gz", func(w io.Writer) error {
			_, writeErr := io.WriteString(w, "section "+section)
			return writeErr
		})
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		db.Sections = append(db.Sections, backup.Section{Name: section, File: file})
	}
	m := &backup.Manifest{FormatVersion: backup.FormatVersion, CreatedAt: gateNow.Add(-age), CompletedAt: gateNow.Add(-age + time.Minute), Databases: []backup.Database{db}}
	if err := backup.WriteManifest(ctx, loc, m); err != nil {
		t.Fatal(err)
	}
	return dir
}

type contractGateHarness struct {
	liveLedger    []backup.LedgerRow
	pending       bool
	mutationCount int
}

func (h *contractGateHarness) install(t *testing.T) {
	t.Helper()
	origDigests, origGuard, origNow := contractGateDigestsFn, migrateBelowFloorGuardFn, backupNowFn
	t.Cleanup(func() { contractGateDigestsFn, migrateBelowFloorGuardFn, backupNowFn = origDigests, origGuard, origNow })
	backupNowFn = func() time.Time { return gateNow }
	contractGateDigestsFn = func(context.Context, *resolvedCluster, *ssh.Pool, string) (map[string]string, error) {
		if !h.pending {
			return nil, nil
		}
		return map[string]string{"postgres/purser": backup.LedgerDigest(h.liveLedger)}, nil
	}
	migrateBelowFloorGuardFn = func(context.Context, *resolvedCluster, *ssh.Pool, map[string]struct{}) error {
		h.mutationCount++
		return nil
	}
}

func (h *contractGateHarness) run(t *testing.T, backupPath string, dryRun bool) error {
	t.Helper()
	cmd := newClusterMigrateCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if backupPath != "" {
		if err := cmd.Flags().Set(backupFlag, backupPath); err != nil {
			t.Fatal(err)
		}
	}
	return runMigrate(cmd, &resolvedCluster{Manifest: &inventory.Manifest{}}, dryRun, "contract", true, "v0.3.11", true, false)
}

func TestContractMigrationsRequireAFreshMatchingBackup(t *testing.T) {
	h := &contractGateHarness{liveLedger: gateLedger, pending: true}
	h.install(t)
	fresh := writeGateBackup(t, 20*time.Minute)
	stale := writeGateBackup(t, 61*time.Minute)

	refusals := []struct {
		name, backup, want string
		live               []backup.LedgerRow
		dryRun             bool
	}{
		{name: "no backup", want: "a verified backup is required"},
		{name: "no backup, dry run", dryRun: true, want: "a verified backup is required"},
		{name: "stale backup", backup: stale, want: "the limit is 1h0m0s"},
		{name: "ledger changed after the backup", backup: fresh, want: "postgres/purser: its migration ledger changed after the backup",
			live: append(append([]backup.LedgerRow{}, gateLedger...), backup.LedgerRow{Version: "v0.3.11", Phase: "postdeploy", Seq: 1, Checksum: "ccc"})},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			h.mutationCount = 0
			h.liveLedger = gateLedger
			if tc.live != nil {
				h.liveLedger = tc.live
			}
			err := h.run(t, tc.backup, tc.dryRun)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want a refusal containing %q", err, tc.want)
			}
			if h.mutationCount != 0 {
				t.Fatal("contract migrations went past the backup gate")
			}
		})
	}

	t.Run("fresh matching backup", func(t *testing.T) {
		h.mutationCount, h.liveLedger = 0, gateLedger
		if err := h.run(t, fresh, false); err != nil {
			t.Fatalf("a fresh backup with the live ledger was refused: %v", err)
		}
		if h.mutationCount != 1 {
			t.Fatal("contract migrations did not proceed after the gate passed")
		}
	})

	t.Run("no pending contract migration needs no backup", func(t *testing.T) {
		h.mutationCount, h.pending = 0, false
		defer func() { h.pending = true }()
		if err := h.run(t, "", false); err != nil || h.mutationCount != 1 {
			t.Fatalf("err = %v, proceeded = %v; nothing to contract must not ask for a backup", err, h.mutationCount == 1)
		}
	})

	t.Run("postdeploy never asks for a backup", func(t *testing.T) {
		h.mutationCount = 0
		cmd := newClusterMigrateCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := runMigrate(cmd, &resolvedCluster{Manifest: &inventory.Manifest{}}, false, "postdeploy", true, "v0.3.11", true, false); err != nil || h.mutationCount != 1 {
			t.Fatalf("err = %v, proceeded = %v", err, h.mutationCount == 1)
		}
	})
}

type dataMigrationGateHarness struct {
	entry      dataMigrationListEntry
	found      bool
	liveLedger []backup.LedgerRow
	ran        int
	listed     int
}

func (h *dataMigrationGateHarness) install(t *testing.T) {
	t.Helper()
	origEntry, origDigests, origRemote, origNow := remoteDataMigrationEntryFn, serviceDatabaseDigestsFn, runDataMigrateRemoteFn, backupNowFn
	t.Cleanup(func() {
		remoteDataMigrationEntryFn, serviceDatabaseDigestsFn, runDataMigrateRemoteFn, backupNowFn = origEntry, origDigests, origRemote, origNow
	})
	backupNowFn = func() time.Time { return gateNow }
	remoteDataMigrationEntryFn = func(context.Context, *resolvedCluster, *ssh.Pool, string, string) (dataMigrationListEntry, bool, error) {
		h.listed++
		return h.entry, h.found, nil
	}
	serviceDatabaseDigestsFn = func(context.Context, *resolvedCluster, *ssh.Pool, string) (map[string]string, error) {
		return map[string]string{"postgres/purser": backup.LedgerDigest(h.liveLedger)}, nil
	}
	runDataMigrateRemoteFn = func(*cobra.Command, *resolvedCluster, string, []string) error {
		h.ran++
		return nil
	}
}

func (h *dataMigrationGateHarness) run(t *testing.T, service, id, backupPath string, dryRun bool) error {
	t.Helper()
	cmd := newDMRunCmd()
	cmd.SetOut(io.Discard)
	if backupPath != "" {
		if err := cmd.Flags().Set(backupFlag, backupPath); err != nil {
			t.Fatal(err)
		}
	}
	manifest := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{
		"purser":    {Enabled: true, Host: "db-1"},
		"commodore": {Enabled: true, Host: "db-1"},
	}}
	return runDataMigrateRun(cmd, &resolvedCluster{Manifest: manifest}, service, id, dryRun, []string{"data-migrations", "run", id})
}

func boolPtr(v bool) *bool { return &v }

func TestIrreversibleDataMigrationRequiresAFreshMatchingBackup(t *testing.T) {
	h := &dataMigrationGateHarness{liveLedger: gateLedger}
	h.install(t)
	fresh := writeGateBackup(t, 10*time.Minute)
	stale := writeGateBackup(t, 2*time.Hour)
	const eur = "purser_eur_ledger_conversion_v0_3_11"

	for _, tc := range []struct {
		name, backup, want string
		live               []backup.LedgerRow
	}{
		{name: "no backup", want: "a verified backup is required"},
		{name: "stale backup", backup: stale, want: "the limit is 1h0m0s"},
		{name: "ledger changed", backup: fresh, want: "its migration ledger changed after the backup", live: gateLedger[:1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.ran, h.liveLedger = 0, gateLedger
			if tc.live != nil {
				h.liveLedger = tc.live
			}
			h.entry, h.found = dataMigrationListEntry{ID: eur, Irreversible: boolPtr(true)}, true
			err := h.run(t, "purser", eur, tc.backup, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || h.ran != 0 {
				t.Fatalf("err = %v, ran = %d; want a refusal containing %q before the migration runs", err, h.ran, tc.want)
			}
		})
	}

	t.Run("fresh matching backup runs it", func(t *testing.T) {
		h.ran, h.liveLedger = 0, gateLedger
		if err := h.run(t, "purser", eur, fresh, false); err != nil || h.ran != 1 {
			t.Fatalf("err = %v, ran = %d", err, h.ran)
		}
	})

	t.Run("dry run needs no backup", func(t *testing.T) {
		h.ran = 0
		if err := h.run(t, "purser", eur, "", true); err != nil || h.ran != 1 {
			t.Fatalf("err = %v, ran = %d; a read-only dry run must not ask for a backup", err, h.ran)
		}
	})
}

func TestDataMigrationIrreversibilityDetection(t *testing.T) {
	h := &dataMigrationGateHarness{liveLedger: gateLedger}
	h.install(t)
	manifest := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{"purser": {Enabled: true}, "commodore": {Enabled: true}}}
	rc := &resolvedCluster{Manifest: manifest}

	h.listed = 0
	irreversible, reason, err := dataMigrationIrreversible(context.Background(), rc, nil, "purser", "purser_eur_ledger_conversion_v0_3_11")
	if err != nil || !irreversible || !strings.Contains(reason, "release v0.3.11 disables rollback for purser") || h.listed != 0 {
		t.Fatalf("irreversible=%v reason=%q listed=%d err=%v; the catalog's rollback_disabled must mark it irreversible without asking the binary", irreversible, reason, h.listed, err)
	}

	const fieldEncryption = "commodore_field_encryption_v0_3_0"
	for _, tc := range []struct {
		name  string
		entry dataMigrationListEntry
		found bool
		want  bool
	}{
		{name: "binary marks it irreversible", entry: dataMigrationListEntry{ID: fieldEncryption, Irreversible: boolPtr(true)}, found: true, want: true},
		{name: "binary does not report the field", entry: dataMigrationListEntry{ID: fieldEncryption}, found: true, want: true},
		{name: "binary does not list it", found: false, want: true},
		{name: "binary marks it reversible", entry: dataMigrationListEntry{ID: fieldEncryption, Irreversible: boolPtr(false)}, found: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.entry, h.found = tc.entry, tc.found
			got, _, err := dataMigrationIrreversible(context.Background(), rc, nil, "commodore", fieldEncryption)
			if err != nil || got != tc.want {
				t.Fatalf("irreversible = %v, err = %v; want %v", got, err, tc.want)
			}
		})
	}

	t.Run("a reversible migration runs without a backup", func(t *testing.T) {
		h.ran, h.entry, h.found = 0, dataMigrationListEntry{ID: "x", Irreversible: boolPtr(false)}, true
		if err := h.run(t, "commodore", "x", "", false); err != nil || h.ran != 1 {
			t.Fatalf("err = %v, ran = %d", err, h.ran)
		}
	})
}

func TestFindDataMigrationEntryTellsAbsentFieldFromFalse(t *testing.T) {
	entry, found, err := findDataMigrationEntry(`[{"id":"a","irreversible":false},{"id":"b"}]`, "a")
	if err != nil || !found || entry.Irreversible == nil || *entry.Irreversible {
		t.Fatalf("a: %+v %v %v", entry, found, err)
	}
	entry, found, err = findDataMigrationEntry(`[{"id":"a","irreversible":false},{"id":"b"}]`, "b")
	if err != nil || !found || entry.Irreversible != nil {
		t.Fatalf("b: %+v %v %v; an old binary's missing field must stay nil", entry, found, err)
	}
	if _, _, err := findDataMigrationEntry("not json", "a"); err == nil {
		t.Fatal("undecodable list must fail closed")
	}
	if _, found, _ := findDataMigrationEntry(`[]`, "a"); found {
		t.Fatal("empty list found an entry")
	}
}
