package mistdiag

import (
	"strings"
)

// ParseOutput extracts structured results from analyzer stdout/stderr.
func ParseOutput(stdout, stderr string, exitCode int) *AnalyzerResult {
	result := &AnalyzerResult{
		OK:       exitCode == 0,
		Output:   stdout,
		ExitCode: exitCode,
	}

	combined := stdout + "\n" + stderr
	for _, line := range strings.Split(combined, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		upper := strings.ToUpper(line)
		if containsAny(upper, "ERROR", "FAIL", "FATAL") {
			result.Errors = append(result.Errors, line)
		} else if containsAny(upper, "WARNING", "WARN") {
			result.Warnings = append(result.Warnings, line)
		}
	}

	return result
}

// FirstError returns the first error string, or empty if none.
func (r *AnalyzerResult) FirstError() string {
	if len(r.Errors) == 0 {
		return ""
	}
	return r.Errors[0]
}

// Summary returns a short human-readable summary of the result.
func (r *AnalyzerResult) Summary() string {
	if r.OK && len(r.Warnings) == 0 {
		return "OK"
	}
	if r.OK && len(r.Warnings) > 0 {
		return "OK (with warnings)"
	}
	if len(r.Errors) > 0 {
		return r.Errors[0]
	}
	return "FAIL"
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
