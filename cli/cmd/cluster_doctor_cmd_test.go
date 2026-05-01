package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestClusterDoctorCmd_HelpAdvertisesDeepFlag(t *testing.T) {
	t.Parallel()
	cmd := newClusterDoctorCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help execution: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--deep") {
		t.Errorf("--help output missing --deep flag:\n%s", out)
	}
	if !strings.Contains(out, "SOPS") {
		t.Errorf("--help should explain SOPS implication of --deep:\n%s", out)
	}
}

func TestClusterDoctorCmd_HelpLongDescribesDefaultVsDeep(t *testing.T) {
	t.Parallel()
	cmd := newClusterDoctorCmd()
	long := cmd.Long
	for _, want := range []string{"Default mode", "--deep mode", "not verified", "_migrations"} {
		if !strings.Contains(long, want) {
			t.Errorf("Long description missing %q:\n%s", want, long)
		}
	}
	if strings.Contains(cmd.Short, "Comprehensive") {
		t.Errorf("Short description still aspirational: %q", cmd.Short)
	}
}
