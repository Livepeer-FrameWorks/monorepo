package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// writeReport writes results.json (every statement's metrics and findings, without raw plans), databases.json
// (seeded volumes, tablets and index definitions per database and layout), not-audited.tsv and report.md, a
// ranked table of flagged statements with their distributed and declared-layout numbers side by side.
func writeReport(opts options, results []result, runs []databaseRun, unresolved []string, elapsed time.Duration) error {
	if err := writeJSON(filepath.Join(opts.out, "results.json"), results); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(opts.out, "databases.json"), runs); err != nil {
		return err
	}
	var na strings.Builder
	na.WriteString("id\tlayout\tstatus\tsource\treason\n")
	statusCount := map[string]int{}
	for _, r := range results {
		statusCount[r.Layout+"/"+r.Status]++
		if r.Status != "analyzed" {
			fmt.Fprintf(&na, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Layout, r.Status, r.Source, strings.ReplaceAll(r.Reason, "\t", " "))
		}
	}
	for _, site := range unresolved {
		fmt.Fprintf(&na, "-\t-\tunresolved_call_site\t%s\tSQL text passed through a variable the extractor cannot resolve\n", site)
	}
	if err := os.WriteFile(filepath.Join(opts.out, "not-audited.tsv"), []byte(na.String()), 0o644); err != nil {
		return err
	}

	type pair struct{ distributed, declared *result }
	byID := map[string]*pair{}
	for i := range results {
		r := &results[i]
		p := byID[r.ID]
		if p == nil {
			p = &pair{}
			byID[r.ID] = p
		}
		if r.Layout == "distributed" {
			p.distributed = r
		} else {
			p.declared = r
		}
	}
	var flagged []string
	for id, p := range byID {
		if (p.distributed != nil && len(p.distributed.Flags) > 0) || (p.declared != nil && len(p.declared.Flags) > 0) {
			flagged = append(flagged, id)
		}
	}
	score := func(id string) float64 {
		p := byID[id]
		best := 0.0
		for _, r := range []*result{p.distributed, p.declared} {
			if r == nil {
				continue
			}
			s := float64(len(r.Flags))
			if r.Metrics != nil {
				s += r.Metrics.StorageRowsRead/1000 + r.Metrics.ReadRequests/10 + r.Metrics.ExecutionMillis/10
			}
			for _, f := range r.Findings {
				if f.Kind == "SEQ_SCAN_LARGE" || f.Kind == "TIMEOUT" {
					s += f.Rows / 500
				}
			}
			best = maxf(best, s)
		}
		return best
	}
	sort.Slice(flagged, func(i, j int) bool { return score(flagged[i]) > score(flagged[j]) })

	var md strings.Builder
	fmt.Fprintf(&md, "# Yugabyte explain audit\n\nRun took %s. Hot tables seeded with %d rows, others %d (see databases.json for per-table live rows, tablets and indexes).\n\n",
		elapsed.Round(time.Second), opts.hotRows, opts.defaultRows)
	md.WriteString("| layout/status | statements |\n|---|---|\n")
	for _, k := range sortedKeys(statusCount) {
		fmt.Fprintf(&md, "| %s | %d |\n", k, statusCount[k])
	}
	fmt.Fprintf(&md, "\nUnresolved call sites (SQL in a variable the extractor cannot fold): %d\n\n", len(unresolved))
	// A statement that returned no rows still shows its scans, but not the per-row work a match would cost; one
	// bound only from synthetic values may not reflect a production lookup.
	quality := map[string]int{}
	for _, r := range results {
		if r.Status != "analyzed" {
			continue
		}
		if r.Metrics != nil && r.Metrics.RowsReturned == 0 {
			quality[r.Layout+" returned no rows"]++
		}
		synthetic := 0
		for _, p := range r.Params {
			if strings.HasPrefix(p.Source, "synthetic") {
				synthetic++
			}
		}
		if synthetic > 0 {
			quality[r.Layout+" some parameters synthetic"]++
		}
		if synthetic > 0 && synthetic == len(r.Params) {
			quality[r.Layout+" all parameters synthetic"]++
		}
	}
	md.WriteString("| binding quality (analyzed statements) | statements |\n|---|---|\n")
	for _, k := range sortedKeys(quality) {
		fmt.Fprintf(&md, "| %s | %d |\n", k, quality[k])
	}
	md.WriteString("\n")
	md.WriteString("## Flagged statements, ranked\n\n")
	md.WriteString("| # | statement | source | flags (distributed / declared) | rows scanned / returned (dist) | read RPCs dist / decl | ms dist / decl | first finding |\n|---|---|---|---|---|---|---|---|\n")
	for i, id := range flagged {
		p := byID[id]
		cell := func(r *result, f func(*metrics) string) string {
			if r == nil || r.Metrics == nil {
				if r != nil {
					return r.Status
				}
				return "-"
			}
			return f(r.Metrics)
		}
		flags := func(r *result) string {
			if r == nil {
				return "-"
			}
			if len(r.Flags) == 0 {
				return "ok"
			}
			return strings.Join(r.Flags, ",")
		}
		first := ""
		for _, r := range []*result{p.distributed, p.declared} {
			if r != nil && len(r.Findings) > 0 {
				f := r.Findings[0]
				first = fmt.Sprintf("%s %s %s", f.Kind, f.Relation, f.Detail)
				break
			}
		}
		src := ""
		if p.distributed != nil {
			src = p.distributed.Source
		} else if p.declared != nil {
			src = p.declared.Source
		}
		fmt.Fprintf(&md, "| %d | %s | %s | %s / %s | %s | %s / %s | %s / %s | %s |\n", i+1, id, src, flags(p.distributed), flags(p.declared),
			cell(p.distributed, func(m *metrics) string { return fmt.Sprintf("%.0f / %.0f", m.StorageRowsRead, m.RowsReturned) }),
			cell(p.distributed, func(m *metrics) string { return fmt.Sprintf("%.0f", m.ReadRequests) }),
			cell(p.declared, func(m *metrics) string { return fmt.Sprintf("%.0f", m.ReadRequests) }),
			cell(p.distributed, func(m *metrics) string { return fmt.Sprintf("%.1f", m.ExecutionMillis) }),
			cell(p.declared, func(m *metrics) string { return fmt.Sprintf("%.1f", m.ExecutionMillis) }),
			strings.ReplaceAll(strings.ReplaceAll(first, "|", "\\|"), "\n", " "))
	}
	return os.WriteFile(filepath.Join(opts.out, "report.md"), []byte(md.String()), 0o644)
}

// budgetEntry is one statement's accepted findings in one layout. A run fails when a statement gains a flag its
// entry lacks, or scans or round-trips more than twice what its entry accepts.
type budgetEntry struct {
	Reason       string   `json:"reason,omitempty"`
	Flags        []string `json:"flags"`
	RowsScanned  float64  `json:"rows_scanned"`
	ReadRequests float64  `json:"read_requests"`
}

func budgetKey(r result) string { return r.ID + "@" + r.Layout }

func writeBudget(path string, results []result) error {
	budget := map[string]budgetEntry{}
	for _, r := range results {
		if len(r.Flags) == 0 {
			continue
		}
		e := budgetEntry{Flags: append([]string{}, r.Flags...)}
		sort.Strings(e.Flags)
		if r.Metrics != nil {
			e.RowsScanned, e.ReadRequests = r.Metrics.StorageRowsRead, r.Metrics.ReadRequests
		}
		budget[budgetKey(r)] = e
	}
	return writeJSON(path, budget)
}

func checkBudget(path string, results []result) error {
	if len(results) == 0 {
		return fmt.Errorf("no SQL statements were audited")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read budget %s (create it with -write-budget): %w", path, err)
	}
	var budget map[string]budgetEntry
	if err := json.Unmarshal(data, &budget); err != nil {
		return fmt.Errorf("parse budget %s: %w", path, err)
	}
	var violations []string
	// An unexplainable query has no plan flags. It must not turn a regression
	// green by disappearing from the performance checks. Exceptions describe
	// reviewed limitations of the seeded fixture, separately from plan budgets.
	exceptions := map[string]string{}
	coveragePath := filepath.Join(filepath.Dir(path), "coverage-exceptions.json")
	if data, err := os.ReadFile(coveragePath); err == nil {
		if err = json.Unmarshal(data, &exceptions); err != nil {
			return fmt.Errorf("parse coverage exceptions: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read coverage exceptions: %w", err)
	}
	for _, r := range results {
		if r.Status != "" && r.Status != "analyzed" && exceptions[budgetKey(r)] != r.Status {
			violations = append(violations, fmt.Sprintf("%s: unaccepted coverage gap %s: %s", budgetKey(r), r.Status, r.Reason))
		}
		e, ok := budget[budgetKey(r)]
		allowed := map[string]bool{}
		for _, f := range e.Flags {
			allowed[f] = true
		}
		for _, f := range r.Flags {
			// SLOW alone depends on the machine; it is reported, never gated.
			if f == "SLOW" || allowed[f] {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s: new %s (%s)", budgetKey(r), f, r.Source))
		}
		if ok && r.Metrics != nil {
			if r.Metrics.StorageRowsRead > 2*e.RowsScanned+largeTableRows {
				violations = append(violations, fmt.Sprintf("%s: scans %.0f rows, budget %.0f", budgetKey(r), r.Metrics.StorageRowsRead, e.RowsScanned))
			}
			if r.Metrics.ReadRequests > 2*e.ReadRequests+10 {
				violations = append(violations, fmt.Sprintf("%s: %.0f read requests, budget %.0f", budgetKey(r), r.Metrics.ReadRequests, e.ReadRequests))
			}
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("%d plan regressions beyond %s:\n  %s", len(violations), path, strings.Join(violations, "\n  "))
	}
	return nil
}
