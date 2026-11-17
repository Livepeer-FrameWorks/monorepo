package cmd

import (
	"encoding/json"
	"io"

	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/health"
)

type doctorJSONCheck struct {
	Section string              `json:"section"`
	Name    string              `json:"name"`
	State   string              `json:"state"`
	Counted bool                `json:"counted"`
	Passed  *bool               `json:"passed,omitempty"`
	Result  *health.CheckResult `json:"result,omitempty"`
	Error   string              `json:"error,omitempty"`
	Note    string              `json:"note,omitempty"`
	Steps   []ux.NextStep       `json:"next_steps,omitempty"`
}

type doctorJSONSummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

// writeDoctorJSON includes checks not reached before cancellation. An absent
// outcome never becomes a successful probe.
func writeDoctorJSON(out io.Writer, rc *resolvedCluster, deep bool, sections []doctorSection, outcomes [][]doctorOutcome, interrupted bool, cp readiness.Report) error {
	report := struct {
		Manifest     string            `json:"manifest"`
		Deep         bool              `json:"deep"`
		Completed    bool              `json:"completed"`
		Healthy      bool              `json:"healthy"`
		Checks       []doctorJSONCheck `json:"checks"`
		Summary      doctorJSONSummary `json:"summary"`
		ControlPlane struct {
			Checked  bool                `json:"checked"`
			Healthy  bool                `json:"healthy"`
			Detail   string              `json:"detail"`
			Warnings []readiness.Warning `json:"warnings,omitempty"`
		} `json:"control_plane"`
	}{Manifest: rc.ManifestPath, Deep: deep, Completed: !interrupted, Checks: []doctorJSONCheck{}}
	for si, section := range sections {
		for ci, check := range section.checks {
			var outcome doctorOutcome
			if si < len(outcomes) && ci < len(outcomes[si]) {
				outcome = outcomes[si][ci]
			}
			item := doctorJSONCheck{Section: section.title, Name: check.name, State: "completed", Counted: outcome.counted(), Result: outcome.result, Error: outcome.miss, Note: outcome.note, Steps: outcome.steps}
			if item.Counted {
				item.Passed = &outcome.passed
				report.Summary.Total++
				if outcome.passed {
					report.Summary.Passed++
				} else {
					report.Summary.Failed++
				}
			} else if outcome.note == "" {
				if interrupted && !outcome.completed {
					item.State = "pending"
					report.Summary.Pending++
				} else {
					item.State = "not_applicable"
				}
			}
			report.Checks = append(report.Checks, item)
		}
	}
	report.ControlPlane.Checked = cp.Checked
	report.ControlPlane.Healthy = cp.OK()
	report.ControlPlane.Detail = doctorControlPlaneDetail(cp, deep)
	report.ControlPlane.Warnings = cp.Warnings
	report.Healthy = !interrupted && report.Summary.Failed == 0 && (!cp.Checked || cp.OK())
	return json.NewEncoder(out).Encode(report)
}
