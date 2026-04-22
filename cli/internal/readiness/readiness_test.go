package readiness

import (
	"context"
	"testing"
)

func TestReport_OKRequiresChecked(t *testing.T) {
	t.Parallel()
	var empty Report
	if empty.OK() {
		t.Error("unchecked Report must not report OK")
	}

	checked := Report{Checked: true}
	if !checked.OK() {
		t.Error("checked Report with no warnings should report OK")
	}

	withWarning := Report{Checked: true, Warnings: []Warning{{Subject: "x", Detail: "y"}}}
	if withWarning.OK() {
		t.Error("checked Report with warnings must not report OK")
	}
}

func TestControlPlaneReadiness_missingInputsIsUnchecked(t *testing.T) {
	t.Parallel()
	r := ControlPlaneReadiness(context.Background(), ControlPlaneInputs{})
	if r.Checked {
		t.Error("missing inputs must produce Checked=false")
	}
	if r.OK() {
		t.Error("missing inputs must not report OK")
	}
}

func TestEdgeReadiness_runsWhenEnvExists(t *testing.T) {
	t.Parallel()
	r := EdgeReadiness(EdgeInputs{HasEnv: true, HTTPSStatus: 200})
	if !r.Checked {
		t.Error("EdgeReadiness must set Checked when it ran")
	}
	if !r.OK() {
		t.Errorf("clean edge state should be OK, got warnings %+v", r.Warnings)
	}
}
