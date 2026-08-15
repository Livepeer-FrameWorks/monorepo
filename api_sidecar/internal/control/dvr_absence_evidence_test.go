package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A scannable recording root with no matching layout is positive absence evidence. An unreadable
// root remains inconclusive so storage failures cannot authorize teardown.
func TestDVRFingerprintByHash_GenuineAbsenceConverges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HELMSMAN_STORAGE_LOCAL_PATH", root)
	// The dvr root exists and is scannable, but carries no layout for this hash → genuinely absent.
	if err := os.MkdirAll(filepath.Join(root, "dvr", "node-x"), 0o755); err != nil {
		t.Fatalf("seed dvr root: %v", err)
	}

	const hash = "dvr-gone"
	if dir, scanOK := resolveDVRSegmentsDirByHashChecked(hash); dir != "" || !scanOK {
		t.Fatalf("scanned-but-absent recording: dir=%q scanOK=%v, want empty dir + scanOK=true", dir, scanOK)
	}
	if fp, readOK := dvrFingerprintByHash(hash); !readOK || fp != "absent" {
		t.Fatalf("genuinely-absent recording = (%q, %v), want (\"absent\", true)", fp, readOK)
	}

	// The "absent" fingerprint must actually converge across spaced observations.
	dm := &DVRManager{}
	now := time.Unix(1_000_000, 0)
	dm.nowFn = func() time.Time { return now }
	converged := false
	for i := 0; i < dvrAbsenceThreshold+1; i++ {
		fp, readOK := dvrFingerprintByHash(hash)
		converged = dm.observeAbsenceConverged(hash, fp, readOK)
		now = now.Add(dvrAbsenceGrace)
	}
	if !converged {
		t.Fatal("a genuinely-absent recording must converge; otherwise the delete/stop obligation strands forever")
	}
}

// The bounded-absence gate: convergence requires enough spaced observations
// AND a real elapsed grace AND an unchanged fingerprint. ANY fingerprint change (a rolling
// window advancing its newest segment, a growing current segment, new bytes) resets it.
func TestObserveAbsenceConverged(t *testing.T) {
	dm := &DVRManager{}
	now := time.Unix(1_000_000, 0)
	dm.nowFn = func() time.Time { return now }
	const h = "dvr-abc"

	// A read failure (readOK=false) is inconclusive — never counts, never converges.
	if dm.observeAbsenceConverged(h, "", false) {
		t.Fatal("a read failure must never converge (inconclusive, not idle)")
	}

	// Spaced observations with an UNCHANGED fingerprint accumulate; convergence needs the
	// threshold count AND the elapsed grace.
	converged := false
	for i := 0; i < dvrAbsenceThreshold+1; i++ {
		converged = dm.observeAbsenceConverged(h, "c=5;b=100;n=seg5.ts;m=42", true)
		now = now.Add(dvrAbsenceGrace)
	}
	if !converged {
		t.Fatal("absence must converge after enough spaced unchanged observations past the grace")
	}

	// Rapid (sub-interval) observations do NOT converge on their own.
	dm.clearAbsenceEvidence(h)
	rapid := time.Unix(2_000_000, 0)
	dm.nowFn = func() time.Time { return rapid }
	for i := 0; i < 10; i++ {
		if dm.observeAbsenceConverged(h, "c=5;b=100;n=seg5.ts;m=42", true) {
			t.Fatal("a burst of rapid (sub-interval) observations must not converge")
		}
		rapid = rapid.Add(time.Second)
	}

	// A ROLLING writer: segment count and bytes stay constant, but the newest segment advances
	// (name/mtime change) → the fingerprint changes → evidence resets → never converges.
	const roll = "dvr-roll"
	rn := time.Unix(3_000_000, 0)
	dm.nowFn = func() time.Time { return rn }
	dm.observeAbsenceConverged(roll, "c=5;b=100;n=seg10.ts;m=100", true)
	rn = rn.Add(dvrAbsenceGrace)
	dm.observeAbsenceConverged(roll, "c=5;b=100;n=seg10.ts;m=100", true)
	rn = rn.Add(dvrAbsenceGrace)
	// New segment rolled in: count/bytes identical, but newest name+mtime advanced.
	if dm.observeAbsenceConverged(roll, "c=5;b=100;n=seg11.ts;m=200", true) {
		t.Fatal("a rolling writer (advancing newest segment) must reset the evidence, not converge")
	}

	// The elapsed grace alone (below the observation threshold) must not converge.
	const slow = "dvr-slow"
	sn := time.Unix(4_000_000, 0)
	dm.nowFn = func() time.Time { return sn }
	if dm.observeAbsenceConverged(slow, "empty", true) {
		t.Fatal("first observation must not converge")
	}
	sn = sn.Add(dvrAbsenceGrace + time.Hour)
	if dvrAbsenceThreshold > 2 && dm.observeAbsenceConverged(slow, "empty", true) {
		t.Fatal("the elapsed grace alone must not converge below the observation threshold")
	}
}
