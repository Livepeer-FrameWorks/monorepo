package diagnostics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// TriageAction is the deterministic outcome of the triage step.
type TriageAction string

const (
	TriageOK          TriageAction = "ok"
	TriageFlag        TriageAction = "flag"
	TriageInvestigate TriageAction = "investigate"
)

// ThresholdViolation records a metric that exceeded a hard threshold.
type ThresholdViolation struct {
	Metric  string
	Value   float64
	Limit   float64
	Message string
}

// TriageResult captures the full diagnostic context for a triage decision.
type TriageResult struct {
	Action       TriageAction
	Reason       string
	Trigger      string // "threshold", "baseline", "correlation"
	Violations   []ThresholdViolation
	Deviations   []Deviation
	Correlations []MetricCorrelation
}

// FormatReport produces a human-readable summary for the flag path.
func (r TriageResult) FormatReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Action: %s\nTrigger: %s\nReason: %s\n", r.Action, r.Trigger, r.Reason)
	if len(r.Violations) > 0 {
		b.WriteString("\nThreshold Violations:\n")
		for _, v := range r.Violations {
			fmt.Fprintf(&b, "- %s\n", v.Message)
		}
	}
	if len(r.Deviations) > 0 {
		b.WriteString("\nBaseline Deviations:\n")
		for _, d := range r.Deviations {
			fmt.Fprintf(&b, "- %s\n", d.String())
		}
	}
	if len(r.Correlations) > 0 {
		b.WriteString("\nCorrelations:\n")
		for _, c := range r.Correlations {
			fmt.Fprintf(&b, "- %s (confidence %.2f)\n", c.Hypothesis, c.Confidence)
		}
	}
	return b.String()
}

// Triage produces a deterministic action based on threshold violations,
// baseline deviations, and cross-metric correlations. Zero LLM calls.
func Triage(violations []ThresholdViolation, deviations []Deviation, correlations []MetricCorrelation) TriageResult {
	result := TriageResult{
		Violations:   violations,
		Deviations:   deviations,
		Correlations: correlations,
	}

	// Hard threshold violations → always investigate.
	if len(violations) > 0 {
		msgs := make([]string, len(violations))
		for i, v := range violations {
			msgs[i] = v.Message
		}
		result.Action = TriageInvestigate
		result.Trigger = "threshold"
		result.Reason = "threshold warning: " + strings.Join(msgs, ", ")
		return result
	}

	// High-confidence correlation → investigate.
	for _, c := range correlations {
		if c.Confidence >= 0.5 {
			result.Action = TriageInvestigate
			result.Trigger = "correlation"
			result.Reason = c.Hypothesis
			return result
		}
	}

	// Multiple deviations but no high-confidence pattern → flag for review.
	if len(deviations) >= 2 {
		result.Action = TriageFlag
		result.Trigger = "baseline"
		result.Reason = fmt.Sprintf("%d metrics deviated from baseline", len(deviations))
		return result
	}

	// Single deviation → flag.
	if len(deviations) == 1 {
		result.Action = TriageFlag
		result.Trigger = "baseline"
		result.Reason = deviations[0].String()
		return result
	}

	result.Action = TriageOK
	result.Trigger = ""
	result.Reason = "all metrics within baseline"
	return result
}

// TriageCooldown suppresses repeated flag notifications within a cooldown window.
// Investigations always pass through.
type TriageCooldown struct {
	mu       sync.Mutex
	lastFlag map[string]time.Time
	duration time.Duration
}

const DefaultFlagCooldown = 2 * time.Hour

// NewTriageCooldown creates a cooldown tracker.
func NewTriageCooldown(duration time.Duration) *TriageCooldown {
	if duration <= 0 {
		duration = DefaultFlagCooldown
	}
	return &TriageCooldown{
		lastFlag: make(map[string]time.Time),
		duration: duration,
	}
}

// ShouldFlag returns true if the tenant hasn't been flagged within the cooldown window.
func (c *TriageCooldown) ShouldFlag(tenantID string) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if last, ok := c.lastFlag[tenantID]; ok {
		if now.Sub(last) < c.duration {
			return false
		}
	}
	c.lastFlag[tenantID] = now
	return true
}
