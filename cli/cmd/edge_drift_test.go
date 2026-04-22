package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"frameworks/cli/internal/readiness"
)

func TestClassifyEdgeServices_dockerModeOKStoppedMissing(t *testing.T) {
	t.Parallel()
	docker := []readiness.EdgeCheck{
		{Name: "caddy", OK: true, Detail: "Up 2 hours"},
		{Name: "mistserver", OK: false, Detail: "Exited"},
	}
	native := []readiness.EdgeCheck{}
	got := classifyEdgeServices("docker", docker, native)

	statuses := map[string]string{}
	for _, s := range got {
		statuses[s.Name] = s.Status
	}
	if statuses["caddy"] != driftStatusOK {
		t.Errorf("caddy: want ok, got %s", statuses["caddy"])
	}
	if statuses["mistserver"] != driftStatusStopped {
		t.Errorf("mistserver: want stopped, got %s", statuses["mistserver"])
	}
	if statuses["helmsman"] != driftStatusMissing {
		t.Errorf("helmsman: want missing, got %s", statuses["helmsman"])
	}
}

func TestClassifyEdgeServices_wrongModeFoundInOtherStack(t *testing.T) {
	t.Parallel()
	docker := []readiness.EdgeCheck{}
	native := []readiness.EdgeCheck{
		{Name: "mistserver", OK: true, Detail: "active (running)"},
	}
	got := classifyEdgeServices("docker", docker, native)
	statuses := map[string]string{}
	for _, s := range got {
		statuses[s.Name] = s.Status
	}
	if statuses["mistserver"] != driftStatusWrongMode {
		t.Errorf("mistserver: want wrong_mode, got %s", statuses["mistserver"])
	}
}

func TestClassifyEdgeServices_nativeModeSymmetric(t *testing.T) {
	t.Parallel()
	docker := []readiness.EdgeCheck{
		{Name: "caddy", OK: true, Detail: "Up"},
	}
	native := []readiness.EdgeCheck{
		{Name: "mistserver", OK: true, Detail: "active (running)"},
		{Name: "helmsman", OK: false, Detail: "failed"},
	}
	got := classifyEdgeServices("native", docker, native)
	statuses := map[string]string{}
	for _, s := range got {
		statuses[s.Name] = s.Status
	}
	if statuses["caddy"] != driftStatusWrongMode {
		t.Errorf("caddy: want wrong_mode in native mode (found only in docker), got %s", statuses["caddy"])
	}
	if statuses["mistserver"] != driftStatusOK {
		t.Errorf("mistserver: want ok, got %s", statuses["mistserver"])
	}
	if statuses["helmsman"] != driftStatusStopped {
		t.Errorf("helmsman: want stopped, got %s", statuses["helmsman"])
	}
}

func TestClassifyEdgeServices_omittedFromBothStacksIsMissing(t *testing.T) {
	t.Parallel()
	got := classifyEdgeServices("docker", nil, nil)
	if len(got) != len(edgeDriftServices) {
		t.Fatalf("expected %d entries, got %d", len(edgeDriftServices), len(got))
	}
	for _, s := range got {
		if s.Status != driftStatusMissing {
			t.Errorf("%s: want missing, got %s", s.Name, s.Status)
		}
	}
}

func TestClassifyConfigKey_presentEmptyMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		value  string
		info   bool
		want   string
		detail string
	}{
		{"non-empty", "foo", false, driftConfigPresent, ""},
		{"whitespace-only is empty", "   ", false, driftConfigEmpty, ""},
		{"missing required", "", false, driftStatusMissing, ""},
		{"missing informational", "", true, driftStatusMissing, "informational"},
		{"whitespace informational", "   ", true, driftConfigEmpty, "informational"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyConfigKey("X", c.value, c.info)
			if got.Status != c.want {
				t.Errorf("status: want %s, got %s", c.want, got.Status)
			}
			if got.Detail != c.detail {
				t.Errorf("detail: want %q, got %q", c.detail, got.Detail)
			}
		})
	}
}

func TestClassifyEdgeDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		env    string
		flag   string
		want   string
		detail string
	}{
		{"both unset is informational missing", "", "", driftStatusMissing, "informational"},
		{"env set, no flag", "a.example.com", "", driftConfigPresent, ""},
		{"env set, flag matches", "a.example.com", "a.example.com", driftConfigPresent, ""},
		{"env set, flag differs", "a.example.com", "b.example.com", driftConfigDomainFlagMismatch, "env=a.example.com flag=b.example.com"},
		{"env unset, flag set — still informational on env side", "", "b.example.com", driftStatusMissing, "informational"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyEdgeDomain(c.env, c.flag)
			if got.Status != c.want {
				t.Errorf("status: want %s, got %s", c.want, got.Status)
			}
			if got.Detail != c.detail {
				t.Errorf("detail: want %q, got %q", c.detail, got.Detail)
			}
		})
	}
}

func TestCountEdgeDriftDivergences(t *testing.T) {
	t.Parallel()
	rep := edgeDriftReport{
		Services: []edgeDriftServiceStatus{
			{Name: "caddy", Status: driftStatusOK},
			{Name: "mistserver", Status: driftStatusStopped},
			{Name: "helmsman", Status: driftStatusMissing},
		},
		Config: []edgeDriftConfigStatus{
			{Key: "NODE_ID", Status: driftConfigPresent},
			{Key: "EDGE_DOMAIN", Status: driftStatusMissing, Detail: "informational"}, // doesn't count
			{Key: "FOGHORN_CONTROL_ADDR", Status: driftStatusMissing},                 // counts
			{Key: "TELEMETRY_URL", Status: driftConfigEmpty, Detail: "informational"}, // doesn't count
		},
		Health: &edgeDriftHealth{Status: driftHealthMismatch},
	}
	got := countEdgeDriftDivergences(rep)
	// Services: 2 (stopped + missing). Config: 1 (non-informational missing). Health: 1.
	if got != 4 {
		t.Errorf("want 4 divergences, got %d", got)
	}
}

func TestCountEdgeDriftDivergences_noHealthProbe(t *testing.T) {
	t.Parallel()
	rep := edgeDriftReport{
		Services: []edgeDriftServiceStatus{{Status: driftStatusOK}},
		Config:   []edgeDriftConfigStatus{{Status: driftConfigPresent}},
		Health:   nil,
	}
	if got := countEdgeDriftDivergences(rep); got != 0 {
		t.Errorf("want 0, got %d", got)
	}
}

func TestRenderEdgeDriftText_cleanRun(t *testing.T) {
	t.Parallel()
	rep := edgeDriftReport{
		Mode: "docker",
		Node: "edge-1",
		Services: []edgeDriftServiceStatus{
			{Name: "caddy", Status: driftStatusOK},
		},
		Config: []edgeDriftConfigStatus{
			{Key: "NODE_ID", Status: driftConfigPresent},
		},
		Summary: edgeDriftSummary{Total: 2, Divergences: 0},
	}
	var buf bytes.Buffer
	renderEdgeDriftText(&buf, rep)
	text := buf.String()
	if !strings.Contains(text, "Edge drift") {
		t.Errorf("missing header; got:\n%s", text)
	}
	if !strings.Contains(text, "No drift detected") {
		t.Errorf("missing clean-run footer; got:\n%s", text)
	}
}

func TestRenderEdgeDriftText_divergenceFooter(t *testing.T) {
	t.Parallel()
	rep := edgeDriftReport{
		Mode:    "docker",
		Summary: edgeDriftSummary{Total: 3, Divergences: 2},
	}
	var buf bytes.Buffer
	renderEdgeDriftText(&buf, rep)
	text := buf.String()
	if !strings.Contains(text, "2 divergence(s) in 3 checks") {
		t.Errorf("missing divergence footer; got:\n%s", text)
	}
}

func TestReadEnvFileKeyLocalPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/.edge.env"
	content := "NODE_ID=abc\nEDGE_DOMAIN=  example.com  \nEMPTY=\n# comment\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp env: %v", err)
	}
	if got := readEnvFileKey(path, "NODE_ID"); got != "abc" {
		t.Errorf("NODE_ID: want abc, got %q", got)
	}
	if got := readEnvFileKey(path, "EDGE_DOMAIN"); got != "example.com" {
		t.Errorf("EDGE_DOMAIN: want example.com, got %q", got)
	}
	if got := readEnvFileKey(path, "EMPTY"); got != "" {
		t.Errorf("EMPTY: want empty, got %q", got)
	}
	if got := readEnvFileKey(path, "MISSING"); got != "" {
		t.Errorf("MISSING: want empty, got %q", got)
	}
}
