package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	var (
		monorepoRoot   = flag.String("monorepo", ".", "Path to the monorepo root (must contain .github/release-components.json)")
		gitopsDir      = flag.String("gitops", "../gitops", "Path to the sibling gitops repo (must contain releases/)")
		newTag         = flag.String("tag", "", "The platform tag being released (e.g. v0.2.40). Required.")
		componentsPath = flag.String("components", "", "Override for .github/release-components.json (defaults to <monorepo>/.github/release-components.json)")
		out            = flag.String("out", "", "Write JSON output to this path; if empty, write to stdout")
	)
	flag.Parse()

	if *newTag == "" {
		fmt.Fprintln(os.Stderr, "release-plan: --tag is required")
		os.Exit(2)
	}
	if *componentsPath == "" {
		*componentsPath = filepath.Join(*monorepoRoot, ".github", "release-components.json")
	}

	components, err := LoadComponentsFromFile(*componentsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "release-plan: load components: %v\n", err)
		os.Exit(1)
	}

	planner := NewPlanner(*monorepoRoot, *gitopsDir, *newTag, components)
	plan, err := planner.Plan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "release-plan: %v\n", err)
		os.Exit(1)
	}

	if *out == "" {
		// Write to stdout for ad-hoc inspection; full file pretty-printed.
		if err := writeStdoutJSON(plan); err != nil {
			fmt.Fprintf(os.Stderr, "release-plan: write stdout: %v\n", err)
			os.Exit(1)
		}
		printSummary(os.Stderr, plan)
		return
	}
	if err := WriteJSON(*out, plan); err != nil {
		fmt.Fprintf(os.Stderr, "release-plan: write %s: %v\n", *out, err)
		os.Exit(1)
	}
	printSummary(os.Stderr, plan)
}

func writeStdoutJSON(plan *PlanOutput) error {
	tmp, err := os.CreateTemp("", "release-plan-*.json")
	if err != nil {
		return err
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	defer os.Remove(tmp.Name())
	if writeErr := WriteJSON(tmp.Name(), plan); writeErr != nil {
		return writeErr
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(b)
	return err
}

func printSummary(w *os.File, plan *PlanOutput) {
	fmt.Fprintf(w, "release-plan: platform=%s track=%s baseline=%s build=%d carry_forward=%d\n",
		plan.PlatformVersion, plan.Track, plan.BaselineTag, plan.Summary.BuildCount, plan.Summary.CarryForwardCount)
}
