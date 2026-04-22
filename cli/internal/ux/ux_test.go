package ux

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

// The ux helpers take an io.Writer. In tests we pass a *bytes.Buffer, which
// is not a *os.File, so DetectMode will return Mode{Color:false, Unicode:false}
// — i.e. the "non-TTY plain" style. Tests assert ASCII fallbacks + no color.

func withRuntime(t *testing.T, o fwcfg.RuntimeOverrides) {
	t.Helper()
	prev := fwcfg.GetRuntimeOverrides()
	fwcfg.SetRuntimeOverrides(o)
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(prev) })
}

func TestHeading_plain(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	Heading(&buf, "Provisioning cluster")
	if got := buf.String(); got != "Provisioning cluster\n" {
		t.Fatalf("unexpected heading output: %q", got)
	}
}

func TestHeading_jsonModeNoOp(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{OutputJSON: true})
	var buf bytes.Buffer
	Heading(&buf, "ignored")
	if buf.Len() != 0 {
		t.Fatalf("expected no output in JSON mode, got %q", buf.String())
	}
}

func TestStatusMarks_nonTTYUsesASCII(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	cases := []struct {
		fn   func(w *bytes.Buffer, msg string)
		want string
	}{
		{func(w *bytes.Buffer, msg string) { Success(w, msg) }, "[OK] done\n"},
		{func(w *bytes.Buffer, msg string) { Warn(w, msg) }, "[WARN] done\n"},
		{func(w *bytes.Buffer, msg string) { Fail(w, msg) }, "[FAIL] done\n"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		tc.fn(&buf, "done")
		if got := buf.String(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestContextNotice_stillPrintsUnderNoHints(t *testing.T) {
	// Notices are load-bearing: they tell the operator a default was pulled
	// from saved state. Suppressing them in CI would make it impossible to
	// debug "why did this command use a different tenant-id in CI vs. local?"
	withRuntime(t, fwcfg.RuntimeOverrides{NoHints: true})
	var buf bytes.Buffer
	ContextNotice(&buf, "tenant", "abc-123")
	if got := buf.String(); got != "Using tenant from context: abc-123\n" {
		t.Fatalf("NoHints must NOT suppress context notices, got %q", got)
	}
}

func TestContextNotice_suppressedByJSON(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{OutputJSON: true})
	var buf bytes.Buffer
	ContextNotice(&buf, "tenant", "abc-123")
	if buf.Len() != 0 {
		t.Fatalf("JSON mode must suppress the notice, got %q", buf.String())
	}
}

func TestContextNotice_prints(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	ContextNotice(&buf, "tenant", "abc-123")
	if got := buf.String(); got != "Using tenant from context: abc-123\n" {
		t.Fatalf("unexpected notice: %q", got)
	}
}

func TestResult_rendersFieldsInOrder(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	Result(&buf, []ResultField{
		{Key: "infra ready", OK: true},
		{Key: "operator account", OK: false, Detail: "missing"},
	})
	got := buf.String()
	if !strings.HasPrefix(got, "Result:\n") {
		t.Fatalf("expected Result heading, got %q", got)
	}
	if !strings.Contains(got, "infra ready") || !strings.Contains(got, "operator account") {
		t.Fatalf("expected both fields in output, got %q", got)
	}
	if idxA, idxB := strings.Index(got, "infra ready"), strings.Index(got, "operator account"); idxA > idxB {
		t.Fatalf("fields out of order: %q", got)
	}
}

func TestNextSteps_suppressedByNoHints(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{NoHints: true})
	var buf bytes.Buffer
	PrintNextSteps(&buf, []NextStep{{Cmd: "frameworks cluster doctor"}})
	if buf.Len() != 0 {
		t.Fatalf("expected no output with NoHints, got %q", buf.String())
	}
}

func TestNextSteps_rendersNumbered(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	PrintNextSteps(&buf, []NextStep{
		{Cmd: "frameworks cluster doctor", Why: "verify control plane"},
		{Cmd: "frameworks admin users create"},
	})
	got := buf.String()
	if !strings.Contains(got, "Next:") {
		t.Fatalf("missing Next: heading: %q", got)
	}
	if !strings.Contains(got, "1. frameworks cluster doctor") {
		t.Fatalf("missing first step: %q", got)
	}
	if !strings.Contains(got, "2. frameworks admin users create") {
		t.Fatalf("missing second step: %q", got)
	}
	if !strings.Contains(got, "verify control plane") {
		t.Fatalf("missing why line: %q", got)
	}
}

func TestNextSteps_whyOnlyRendersAsAdvisoryBullet(t *testing.T) {
	// Readiness remediations sometimes carry only explanatory text (no
	// executable command). The renderer must NOT produce a blank numbered
	// command in that case — blank "1. " with reason text underneath is
	// indistinguishable from a broken build.
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	PrintNextSteps(&buf, []NextStep{
		{Cmd: "frameworks cluster doctor", Why: "executable step"},
		{Why: "advisory only — no command to run"},
	})
	got := buf.String()
	if !strings.Contains(got, "1. frameworks cluster doctor") {
		t.Fatalf("executable step should be numbered: %q", got)
	}
	if strings.Contains(got, "2.") {
		t.Fatalf("why-only entry must NOT get a numbered command prefix: %q", got)
	}
	if !strings.Contains(got, "- advisory only — no command to run") {
		t.Fatalf("why-only entry should render as advisory bullet: %q", got)
	}
}

func TestNextSteps_skipsCompletelyEmptyEntries(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	PrintNextSteps(&buf, []NextStep{{Cmd: "frameworks cluster doctor"}, {}})
	got := buf.String()
	// Empty-empty entries mustn't produce a stray "2." line.
	if strings.Contains(got, "2.") {
		t.Fatalf("empty-empty NextStep must not render: %q", got)
	}
}

func TestFormatError_withHint(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	FormatError(&buf, errors.New("ssh ping failed: i/o timeout"), "check that the host is reachable over SSH")
	got := buf.String()
	if !strings.Contains(got, "ssh ping failed") {
		t.Fatalf("missing error message: %q", got)
	}
	if !strings.Contains(got, "Hint: check that the host is reachable over SSH") {
		t.Fatalf("missing hint: %q", got)
	}
}

func TestFormatError_nilIsNoOp(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	FormatError(&buf, nil, "ignored")
	if buf.Len() != 0 {
		t.Fatalf("expected nil error to produce no output, got %q", buf.String())
	}
}

func TestDetectMode_jsonBeatsEverything(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{OutputJSON: true, NoHints: true})
	m := DetectMode(&bytes.Buffer{})
	if !m.JSON {
		t.Fatal("expected JSON mode")
	}
	if m.Color || m.Unicode || m.Hints {
		t.Fatalf("JSON mode should zero everything else: %+v", m)
	}
}

// sanity: ensure format helpers don't panic when chained
func TestCompositeOutput_chainsCleanly(t *testing.T) {
	withRuntime(t, fwcfg.RuntimeOverrides{})
	var buf bytes.Buffer
	Heading(&buf, "Provisioning cluster")
	ContextNotice(&buf, "manifest", "/tmp/x.yaml")
	Success(&buf, "postgres provisioned")
	Warn(&buf, "redis using default password")
	Result(&buf, []ResultField{{Key: "infra ready", OK: true}})
	PrintNextSteps(&buf, []NextStep{{Cmd: "frameworks cluster doctor"}})
	out := buf.String()
	for _, want := range []string{"Provisioning cluster", "manifest from context", "postgres provisioned", "redis", "infra ready", "cluster doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in composite output:\n%s", want, out)
		}
	}
	// Also ensure no ANSI escape codes leaked in non-TTY mode.
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI escape leaked in non-TTY output:\n%s", out)
	}
	_ = fmt.Sprintf
}
