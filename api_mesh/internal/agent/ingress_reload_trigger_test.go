package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// The reload trigger is what makes ingress pick up new certificate material.
// The version memo is what makes a later pass skip a bundle it believes is
// already current, so advancing the memo before the trigger is touched means one
// failed touch blinds every later pass: the certificates sit on disk, ingress
// keeps serving the previous ones from memory, nothing reports unhealthy, and
// the only recovery is a process restart or the next rotation weeks later.
//
// This drives the two halves directly, because the failure is entirely in their
// ordering.
func TestIngressVersionIsNotRecordedUntilTheReloadTriggerIsTouched(t *testing.T) {
	root := t.TempDir()
	// A directory where the trigger file belongs: opening it for write fails,
	// which is the same shape as the ownership drift seen in the field.
	triggerPath := filepath.Join(root, "trigger")
	if err := os.Mkdir(triggerPath, 0o750); err != nil {
		t.Fatalf("seed unwritable trigger: %v", err)
	}

	a := &Agent{
		logger:          logging.NewLogger(),
		ingressTrigger:  triggerPath,
		ingressVersions: map[string]string{},
	}

	if err := a.touchIngressReloadTrigger(); err == nil {
		t.Fatal("touching an unwritable trigger reported success; the rest of this test proves nothing")
	}
	// The memo must still be empty: nothing was reloaded, so nothing is current.
	if !a.ingressVersionUnchanged("bundle-1", "") && len(a.ingressVersions) != 0 {
		t.Fatalf("version memo was advanced despite the failed touch: %v", a.ingressVersions)
	}
	if a.ingressVersionUnchanged("bundle-1", "marker-1") {
		t.Fatal("a bundle whose reload never happened reads as up to date; the next pass would skip it forever")
	}

	// Once the trigger is writable the touch succeeds and the version is recorded,
	// so subsequent passes can legitimately skip it.
	if err := os.Remove(triggerPath); err != nil {
		t.Fatalf("clear trigger dir: %v", err)
	}
	if err := a.touchIngressReloadTrigger(); err != nil {
		t.Fatalf("touch writable trigger: %v", err)
	}
	a.recordIngressVersion("bundle-1", "marker-1")
	if !a.ingressVersionUnchanged("bundle-1", "marker-1") {
		t.Fatal("a reloaded bundle was not recorded, so every pass would rewrite it")
	}
	if _, err := os.Stat(triggerPath); err != nil {
		t.Fatalf("reload trigger was not written: %v", err)
	}
}

// Same ordering, same reasoning, for the tenant-alias bundles.
func TestTenantAliasVersionIsNotRecordedUntilTheReloadTriggerIsTouched(t *testing.T) {
	root := t.TempDir()
	triggerPath := filepath.Join(root, "trigger")
	if err := os.Mkdir(triggerPath, 0o750); err != nil {
		t.Fatalf("seed unwritable trigger: %v", err)
	}

	a := &Agent{
		logger:         logging.NewLogger(),
		ingressTrigger: triggerPath,
		aliasVersions:  map[string]string{},
	}

	if err := a.touchIngressReloadTrigger(); err == nil {
		t.Fatal("touching an unwritable trigger reported success")
	}
	if a.tenantAliasVersionUnchanged("alias.example.test", "marker-1") {
		t.Fatal("an alias whose reload never happened reads as up to date")
	}

	if err := os.Remove(triggerPath); err != nil {
		t.Fatalf("clear trigger dir: %v", err)
	}
	if err := a.touchIngressReloadTrigger(); err != nil {
		t.Fatalf("touch writable trigger: %v", err)
	}
	a.recordTenantAliasVersion("alias.example.test", "marker-1")
	if !a.tenantAliasVersionUnchanged("alias.example.test", "marker-1") {
		t.Fatal("a reloaded alias was not recorded")
	}
}
