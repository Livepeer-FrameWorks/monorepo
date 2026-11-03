package grafana

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Gauges and counters of the billing and account lifecycle recovery queues.
var lifecycleRecoveryMetric = regexp.MustCompile(`\b[a-z_]+(?:_repeatedly_failed_items|_read_failures_total|_held_items)\b`)

type dashboardPanel struct {
	Title   string           `json:"title"`
	Panels  []dashboardPanel `json:"panels"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
}

func findPanel(panels []dashboardPanel, title string) *dashboardPanel {
	for i := range panels {
		if panels[i].Title == title {
			return &panels[i]
		}
		if found := findPanel(panels[i].Panels, title); found != nil {
			return found
		}
	}
	return nil
}

// Every recovery queue metric a lifecycle alert reads is charted in the ops
// dashboard's lifecycle panel, so an operator sees the input that fired.
func TestLifecycleAlertInputsAreChartedOnTheOpsDashboard(t *testing.T) {
	rules, err := fs.ReadFile(Rules, "rules/frameworks.yml")
	if err != nil {
		t.Fatal(err)
	}
	var metrics []string
	for _, line := range strings.Split(string(rules), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "expr:") {
			continue
		}
		for _, m := range lifecycleRecoveryMetric.FindAllString(line, -1) {
			if !slices.Contains(metrics, m) {
				metrics = append(metrics, m)
			}
		}
	}
	if len(metrics) == 0 {
		t.Fatal("no lifecycle recovery metrics in the alert rules")
	}

	raw, err := fs.ReadFile(Content, "dashboards/frameworks-ops.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []dashboardPanel `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatal(err)
	}
	panel := findPanel(dashboard.Panels, "Billing and account lifecycle recovery")
	if panel == nil {
		t.Fatal("frameworks-ops.json has no Billing and account lifecycle recovery panel")
	}
	var exprs strings.Builder
	for _, target := range panel.Targets {
		exprs.WriteString(target.Expr)
		exprs.WriteString("\n")
	}
	for _, metric := range metrics {
		if !regexp.MustCompile(`\b` + metric + `\b`).MatchString(exprs.String()) {
			t.Errorf("lifecycle alert input %s is not charted in the Billing and account lifecycle recovery panel", metric)
		}
	}
}
