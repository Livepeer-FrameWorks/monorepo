// Package readiness provides workflow-level health reports for control-plane
// and edge deployments. It is distinct from cli/pkg/health, which contains
// low-level TCP/HTTP/DB probes; readiness reasons about whether a complete
// user-facing workflow is usable (e.g. does the control plane have an
// operator account, does the edge node's HTTPS endpoint return 200).
package readiness

// Remediation is an actionable suggestion for fixing a readiness warning.
// Cmd is the exact command to run (if any); Why explains the fix in one line.
type Remediation struct {
	Cmd string
	Why string
}

// Warning describes one readiness issue.
type Warning struct {
	Subject     string
	Detail      string
	Remediation Remediation
}

// Report aggregates readiness findings for a single workflow check.
// Checked=false means inputs were missing and no checks ran — not the same
// as Checked=true with no warnings.
type Report struct {
	Checked  bool
	Warnings []Warning
}

// OK reports whether the workflow is fully ready (checked and no warnings).
func (r Report) OK() bool { return r.Checked && len(r.Warnings) == 0 }
