// Explain audit: loads every service database baseline on a real YugabyteDB engine in the layouts the release
// supports (each database's declared layout from pkg/database/sql/layout, and fully distributed, the shape a
// database keeps until it is relaid out), seeds production-shaped volumes, and runs
// EXPLAIN (ANALYZE, DIST, FORMAT JSON) for every sqlc query and every statically resolvable hand-written statement
// with bound parameters sampled from the seeded rows. Each plan is checked for full scans of large tables, sorts
// over whole tables, read amplification, storage round trips, per-row subplans and unbatched nested loops, and the
// findings are compared with a budget so a regression fails.
//
// Run through `make verify-yugabyte-explain-audit`, which provides the engine (scripts/run-yugabyte-contract-fixture.sh).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type options struct {
	repo         string
	out          string
	databases    map[string]bool
	layouts      []string
	budget       string
	volumes      string
	writeBudget  bool
	catalogOnly  bool
	catalogProbe bool
	hotRows      int
	defaultRows  int
	queryTimeout time.Duration
	only         string
}

func main() {
	var opts options
	var databases, layouts string
	flag.StringVar(&opts.repo, "repo", "../..", "monorepo root")
	flag.StringVar(&opts.out, "out", "", "directory for raw plans and the report (required unless -catalog-only)")
	flag.StringVar(&databases, "databases", "", "comma-separated databases to audit (default: all)")
	flag.StringVar(&layouts, "layouts", "declared,distributed", "comma-separated layouts: declared (pkg/database/sql/layout) and/or distributed")
	flag.StringVar(&opts.volumes, "volumes", "volumes.txt", "row counts for tables that stay small in production")
	flag.StringVar(&opts.budget, "budget", "budget.json", "accepted findings; a finding beyond it fails the run")
	flag.BoolVar(&opts.writeBudget, "write-budget", false, "rewrite -budget from this run's findings instead of checking against it")
	flag.BoolVar(&opts.catalogOnly, "catalog-only", false, "print the extracted statement catalog and exit")
	flag.BoolVar(&opts.catalogProbe, "catalog-preload-probe", false, "measure cold/warm admission query and backend memory in the isolated fixture")
	flag.IntVar(&opts.hotRows, "hot-rows", 20000, "rows seeded into queue, outbox, event and other high-volume tables")
	flag.IntVar(&opts.defaultRows, "default-rows", 3000, "rows seeded into every other table without an override")
	flag.DurationVar(&opts.queryTimeout, "query-timeout", 60*time.Second, "statement timeout for one EXPLAIN ANALYZE")
	flag.StringVar(&opts.only, "only", "", "audit only statements whose id contains this substring")
	flag.Parse()
	if databases != "" {
		opts.databases = map[string]bool{}
		for _, d := range strings.Split(databases, ",") {
			opts.databases[strings.TrimSpace(d)] = true
		}
	}
	for _, l := range strings.Split(layouts, ",") {
		l = strings.TrimSpace(l)
		if l != "declared" && l != "distributed" {
			log.Fatalf("unknown layout %q", l)
		}
		opts.layouts = append(opts.layouts, l)
	}
	repo, err := filepath.Abs(opts.repo)
	if err != nil {
		log.Fatal(err)
	}
	opts.repo = repo
	if err := run(opts); err != nil {
		log.Fatalf("explainaudit: %v", err)
	}
}

func run(opts options) error {
	if opts.catalogProbe {
		return probeCatalogPreload(opts)
	}
	started := time.Now()
	perDB, shared, unresolved, err := buildCatalog(opts.repo)
	if err != nil {
		return err
	}
	if opts.catalogOnly {
		total := 0
		for _, svc := range services {
			qs := perDB[svc.database]
			kinds := map[string]int{}
			dynamic := 0
			for _, q := range qs {
				kinds[q.Kind]++
				if q.Dynamic {
					dynamic++
				}
			}
			total += len(qs)
			fmt.Printf("%-14s sqlc=%d handwritten=%d dynamic=%d\n", svc.database, kinds["sqlc"], kinds["handwritten"], dynamic)
		}
		fmt.Printf("shared (pkg) statements: %d, unresolved call sites: %d, total per-database statements: %d\n", len(shared), len(unresolved), total)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if os.Getenv("EXPLAIN_AUDIT_DUMP") != "" {
			if err = enc.Encode(map[string]any{"databases": perDB, "shared": shared, "unresolved": unresolved}); err != nil {
				return err
			}
		}
		return nil
	}
	if opts.out == "" {
		return fmt.Errorf("-out is required")
	}
	if err = loadVolumeOverrides(opts.volumes); err != nil {
		return err
	}
	if err = os.MkdirAll(opts.out, 0o755); err != nil {
		return err
	}
	eng, err := engineFromEnv()
	if err != nil {
		return err
	}
	var results []result
	var runs []databaseRun
	for _, svc := range services {
		if opts.databases != nil && !opts.databases[svc.database] {
			continue
		}
		for _, layout := range opts.layouts {
			dbStarted := time.Now()
			rs, info, err := auditDatabase(eng, opts, svc, layout, perDB[svc.database], shared)
			if err != nil {
				return fmt.Errorf("%s/%s: %w", svc.database, layout, err)
			}
			info.Duration = time.Since(dbStarted).Round(time.Second).String()
			runs = append(runs, info)
			results = append(results, rs...)
			log.Printf("%s/%s: %d statements in %s", svc.database, layout, len(rs), info.Duration)
		}
	}
	if len(results) == 0 {
		return fmt.Errorf("no SQL statements matched the audit selection")
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ID != results[j].ID {
			return results[i].ID < results[j].ID
		}
		return results[i].Layout < results[j].Layout
	})
	if err := writeReport(opts, results, runs, unresolved, time.Since(started)); err != nil {
		return err
	}
	if opts.writeBudget {
		return writeBudget(opts.budget, results)
	}
	return checkBudget(opts.budget, results)
}
