package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/health"
	"io"
	"path/filepath"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
	"github.com/spf13/cobra"
)

func TestDoctorJSONIsACompleteDocumentIncludingCancellation(t *testing.T) {
	previous := fwcfg.GetRuntimeOverrides()
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(previous) })
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{OutputJSON: true})
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "interrupted"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			var out, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetContext(ctx)
			rc := &resolvedCluster{Manifest: &inventory.Manifest{}, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml")}
			err := runDoctor(cmd, rc, false)
			var document struct {
				Completed bool            `json:"completed"`
				Checks    json.RawMessage `json:"checks"`
				Summary   json.RawMessage `json:"summary"`
			}
			if decodeErr := json.Unmarshal(out.Bytes(), &document); decodeErr != nil {
				t.Fatalf("doctor did not emit one JSON document: %v\nstdout=%s\nstderr=%s", decodeErr, out.String(), stderr.String())
			}
			if document.Completed == canceled || len(document.Checks) == 0 || len(document.Summary) == 0 {
				t.Fatalf("missing completion/check/summary information: %s", out.String())
			}
			if canceled {
				var exit *ExitCodeError
				if !errors.As(err, &exit) || exit.Code != 130 {
					t.Fatalf("cancellation exit=%v, want 130", err)
				}
			}
		})
	}
}

func TestDoctorVerboseJSONPreservesMetadataWithoutText(t *testing.T) {
	previous := fwcfg.GetRuntimeOverrides()
	previousVerbose := verbose
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(previous); verbose = previousVerbose })
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{OutputJSON: true})
	verbose = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	result := &health.CheckResult{OK: true, Metadata: map[string]string{"probe": "receipt"}}
	printHealthResult(cmd, "fixture", result)
	rc := &resolvedCluster{Manifest: &inventory.Manifest{}, ManifestPath: "fixture"}
	sections := []doctorSection{{checks: []doctorCheck{{name: "fixture"}}}}
	outcomes := [][]doctorOutcome{{{result: result, passed: true}}}
	if err := writeDoctorJSON(&out, rc, false, sections, outcomes, false, readiness.Report{}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Checks []doctorJSONCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("verbose doctor corrupted JSON with metadata text: %v: %s", err, out.String())
	}
	if len(doc.Checks) != 1 || doc.Checks[0].Result.Metadata["probe"] != "receipt" {
		t.Fatalf("metadata missing: %s", out.String())
	}
}

func TestDoctorCancellationDuringControlPlaneExits130(t *testing.T) {
	previous := fwcfg.GetRuntimeOverrides()
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(previous) })
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{OutputJSON: true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(ctx)
	rc := &resolvedCluster{Manifest: &inventory.Manifest{}, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml")}
	called := false
	check := func(_ *cobra.Command, _ *resolvedCluster, _ string, _ map[string]string, _ bool) (readiness.Report, []ux.NextStep) {
		called = true
		cancel()
		return readiness.Report{Checked: true, Warnings: []readiness.Warning{{Subject: "fixture", Detail: "interrupted"}}}, nil
	}
	err := runDoctorWithControlPlane(cmd, rc, false, check)
	var exit *ExitCodeError
	var doc struct {
		Completed bool `json:"completed"`
		Healthy   bool `json:"healthy"`
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &doc); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !called || !errors.As(err, &exit) || exit.Code != 130 || doc.Completed || doc.Healthy {
		t.Fatalf("control-plane cancellation reported completion: called=%v doc=%+v err=%v", called, doc, err)
	}
}

func TestDoctorCompletedEmptyCheckStaysNotApplicableAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sections := []doctorSection{{checks: []doctorCheck{
		{name: "skipped", run: func(context.Context) doctorOutcome { return doctorOutcome{} }},
		{name: "waiting", run: func(ctx context.Context) doctorOutcome { <-ctx.Done(); return doctorOutcome{} }},
	}}}
	outcomes, interrupted := runDoctorSections(ctx, io.Discard, sections, 1, func(name string, _ doctorOutcome) {
		if name == "skipped" {
			cancel()
		}
	})
	var out bytes.Buffer
	if err := writeDoctorJSON(&out, &resolvedCluster{ManifestPath: "fixture"}, false, sections, outcomes, interrupted, readiness.Report{}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Checks []doctorJSONCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !interrupted || len(doc.Checks) != 2 || doc.Checks[0].State != "not_applicable" || doc.Checks[1].State != "pending" {
		t.Fatalf("completed empty probe became pending: %s", out.String())
	}
}
