package provisioner

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// makeTarget is one Makefile rule as the contract inventory tests read it: its prerequisites and recipe lines.
type makeTarget struct {
	prereqs []string
	recipe  []makeRecipeLine
}

// makeRecipeLine carries the `ifeq ($(VAR),value)` branch conditions around a recipe line. Conditions on variables
// the caller does not set, and ifdef/ifneq blocks, leave the line active.
type makeRecipeLine struct {
	text       string
	conditions []makeCondition
}

type makeCondition struct {
	variable string
	value    string
	equal    bool
}

func (l makeRecipeLine) activeFor(vars map[string]string) bool {
	for _, condition := range l.conditions {
		value, ok := vars[condition.variable]
		if ok && (value == condition.value) != condition.equal {
			return false
		}
	}
	return true
}

var (
	makeRuleLine        = regexp.MustCompile(`^([A-Za-z0-9_.%-]+):(?:[^=]|$)(.*)$`)
	makeSimpleVariable  = regexp.MustCompile(`^([A-Z0-9_]+) := (.*)$`)
	makeConditionalLine = regexp.MustCompile(`^(ifeq|ifneq|ifdef|ifndef|else|endif)\b`)
	makeIfeqVariable    = regexp.MustCompile(`ifeq \(\$\(([A-Z_]+)\),([^)]*)\)$`)
	makeSubInvocation   = regexp.MustCompile(`\$\(MAKE\)((?:\s+\S+)+)`)
	contractProfileEmit = regexp.MustCompile(`\$\(CONTRACT_GO_TEST\)\s+\S+\s+(postgres|clickhouse|valkey|yugabyte)/([a-z0-9-]+)\s`)
	ciMakeCommand       = regexp.MustCompile(`(?m)^\s+run: make ([a-z0-9-]+)((?:[ \t]+[A-Z_]+=[a-z0-9-]+)*)[ \t]*$`)
	// schemaUnitTarget names one Yugabyte schema contract group for one database; the verify-schema-yugabyte-unit-%
	// pattern rule runs it.
	schemaUnitTarget = regexp.MustCompile(`^verify-schema-yugabyte-unit-([a-z]+)-(compat|completion|preflight)$`)
)

var makeVariableReference = regexp.MustCompile(`\$\(([A-Z0-9_]+)\)`)

// expandMakeSimpleVariable returns value with every $(NAME) reference to a variable in variables replaced by its
// words, and false when value still holds any other reference.
func expandMakeSimpleVariable(variables map[string]string, value string) (string, bool) {
	expanded := makeVariableReference.ReplaceAllStringFunc(value, func(reference string) string {
		if words, known := variables[makeVariableReference.FindStringSubmatch(reference)[1]]; known {
			return words
		}
		return reference
	})
	return expanded, !strings.Contains(expanded, "$")
}

// makeSimpleVariables returns every simple (:=) variable whose value is a word list, once the references it makes to
// such variables defined before it are expanded.
func makeSimpleVariables(makefile string) map[string]string {
	variables := map[string]string{}
	for _, line := range strings.Split(makefile, "\n") {
		if match := makeSimpleVariable.FindStringSubmatch(line); match != nil {
			if value, ok := expandMakeSimpleVariable(variables, match[2]); ok {
				variables[match[1]] = value
			}
		}
	}
	return variables
}

// parseMakeTargets reads every rule, including pattern rules. A prerequisite that names a simple (:=) variable defined
// earlier with a word list, directly or through other such variables, is expanded to those words.
func parseMakeTargets(makefile string) map[string]*makeTarget {
	targets := map[string]*makeTarget{}
	variables := map[string]string{}
	var current *makeTarget
	// Each frame holds one if/else chain: the conditions of its current branch.
	var frames [][]makeCondition
	for _, line := range strings.Split(makefile, "\n") {
		if match := makeSimpleVariable.FindStringSubmatch(line); match != nil {
			if value, ok := expandMakeSimpleVariable(variables, match[2]); ok {
				variables[match[1]] = value
			}
		}
		switch {
		case strings.HasPrefix(line, "\t"):
			if current != nil {
				var conditions []makeCondition
				for _, frame := range frames {
					conditions = append(conditions, frame...)
				}
				current.recipe = append(current.recipe, makeRecipeLine{text: line, conditions: conditions})
			}
			continue
		case makeConditionalLine.MatchString(line):
			frames = applyMakeConditional(frames, line)
			continue
		}
		current = nil
		match := makeRuleLine.FindStringSubmatch(line)
		if match == nil || strings.HasPrefix(line, ".") {
			continue
		}
		target := targets[match[1]]
		if target == nil {
			target = &makeTarget{}
			targets[match[1]] = target
		}
		for _, prereq := range strings.Fields(strings.TrimPrefix(line, match[1]+":")) {
			if name, ok := strings.CutPrefix(prereq, "$("); ok {
				if value, known := variables[strings.TrimSuffix(name, ")")]; known {
					target.prereqs = append(target.prereqs, strings.Fields(value)...)
					continue
				}
			}
			target.prereqs = append(target.prereqs, prereq)
		}
		current = target
	}
	return targets
}

// applyMakeConditional advances the if/else frames for one conditional directive line.
func applyMakeConditional(frames [][]makeCondition, line string) [][]makeCondition {
	positive := func(directive string) []makeCondition {
		if match := makeIfeqVariable.FindStringSubmatch(strings.TrimSpace(directive)); match != nil {
			return []makeCondition{{variable: match[1], value: match[2], equal: true}}
		}
		return nil
	}
	negatedChain := func(frame []makeCondition) []makeCondition {
		var negated []makeCondition
		for _, condition := range frame {
			if condition.equal {
				negated = append(negated, makeCondition{variable: condition.variable, value: condition.value})
			} else {
				negated = append(negated, condition)
			}
		}
		return negated
	}
	switch {
	case strings.HasPrefix(line, "endif"):
		if len(frames) > 0 {
			frames = frames[:len(frames)-1]
		}
	case strings.HasPrefix(line, "else"):
		if len(frames) == 0 {
			return frames
		}
		top := negatedChain(frames[len(frames)-1])
		frames[len(frames)-1] = append(top, positive(strings.TrimPrefix(line, "else"))...)
	default:
		frames = append(frames, positive(line))
	}
	return frames
}

// resolveMakeTarget returns the rule for name: its explicit rule, or else the pattern rule whose % matches it.
func resolveMakeTarget(targets map[string]*makeTarget, name string) (*makeTarget, bool) {
	if target, ok := targets[name]; ok {
		return target, true
	}
	for pattern, target := range targets {
		prefix, suffix, isPattern := strings.Cut(pattern, "%")
		if isPattern && len(name) > len(prefix)+len(suffix) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			return target, true
		}
	}
	return nil, false
}

// reachableMakeTargets follows prerequisites and $(MAKE) sub-invocations from root. vars substitutes $(NAME)
// references in sub-invocation target names; a name that still holds a variable afterwards is not followed, and
// neither is a prerequisite that names a variable parseMakeTargets could not expand.
func reachableMakeTargets(t *testing.T, targets map[string]*makeTarget, root string, vars map[string]string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] || strings.Contains(name, "$") {
			continue
		}
		target, ok := resolveMakeTarget(targets, name)
		if !ok {
			t.Errorf("Makefile target %q (reached from %q) not found", name, root)
			continue
		}
		seen[name] = true
		queue = append(queue, target.prereqs...)
		for _, line := range target.recipe {
			if !line.activeFor(vars) {
				continue
			}
			for _, invocation := range makeSubInvocation.FindAllStringSubmatch(line.text, -1) {
				for _, field := range strings.Fields(invocation[1]) {
					if strings.HasPrefix(field, "-") || strings.Contains(field, "=") || field == `\` {
						continue
					}
					for name, value := range vars {
						field = strings.ReplaceAll(field, "$("+name+")", value)
					}
					if strings.Contains(field, "$") || strings.ContainsAny(field, ";|&") {
						continue
					}
					queue = append(queue, field)
				}
			}
		}
	}
	return seen
}

// emittedContractProfiles lists "<engine>/<profile>" coverage paths the reached targets write through
// run-go-contract-test.sh. A schema unit target writes yugabyte/schema-<database> for its compat group and
// yugabyte/schema-<database>-<group> otherwise, when its pattern rule assigns that coverage name.
func emittedContractProfiles(targets map[string]*makeTarget, reached map[string]bool) map[string]bool {
	profiles := map[string]bool{}
	for name := range reached {
		target, _ := resolveMakeTarget(targets, name)
		recipe := ""
		for _, recipeLine := range target.recipe {
			recipe += recipeLine.text + "\n"
			for _, match := range contractProfileEmit.FindAllStringSubmatch(recipeLine.text, -1) {
				profiles[match[1]+"/"+match[2]] = true
			}
		}
		if unit := schemaUnitTarget.FindStringSubmatch(name); unit != nil {
			database, group := unit[1], unit[2]
			assignment, profile := `coverage="schema-$$database"`, "yugabyte/schema-"+database
			if group != "compat" {
				assignment, profile = `coverage="schema-$$database-`+group+`"`, profile+"-"+group
			}
			if strings.Contains(recipe, group+") ") && strings.Contains(recipe, assignment) {
				profiles[profile] = true
			}
		}
	}
	return profiles
}

func makeVariableWords(makefile, name string) []string {
	for _, line := range strings.Split(makefile, "\n") {
		if value, ok := strings.CutPrefix(line, name+" := "); ok {
			return strings.Fields(value)
		}
	}
	return nil
}

func ciJob(t *testing.T, workflow, job, next string) string {
	t.Helper()
	start := strings.Index(workflow, "\n  "+job+":\n")
	if start < 0 {
		t.Fatalf("CI job %s not found", job)
	}
	end := strings.Index(workflow[start:], "\n  "+next+":\n")
	if end < 0 {
		t.Fatalf("CI job %s terminator %s not found", job, next)
	}
	return workflow[start : start+end]
}

// ciContractProfiles walks every `run: make <target> [VAR=value]` step of a CI job and returns the contract
// coverage profiles those targets write.
func ciContractProfiles(t *testing.T, makefile, job string) map[string]bool {
	t.Helper()
	commands := ciMakeCommand.FindAllStringSubmatch(job, -1)
	if len(commands) == 0 {
		t.Fatal("CI job runs no make targets")
	}
	invocations := make([]ciMakeInvocation, 0, len(commands))
	for _, command := range commands {
		vars := map[string]string{}
		for _, assignment := range strings.Fields(command[2]) {
			name, value, _ := strings.Cut(assignment, "=")
			vars[name] = value
		}
		invocations = append(invocations, ciMakeInvocation{target: command[1], vars: vars})
	}
	return makeContractProfiles(t, makefile, invocations)
}

// ciMakeInvocation is one `make <target> [VAR=value]` a CI step runs.
type ciMakeInvocation struct {
	target string
	vars   map[string]string
}

// makeContractProfiles returns the contract coverage profiles the targets the invocations reach write.
func makeContractProfiles(t *testing.T, makefile string, invocations []ciMakeInvocation) map[string]bool {
	t.Helper()
	targets := parseMakeTargets(makefile)
	reached := map[string]bool{}
	for _, invocation := range invocations {
		for name := range reachableMakeTargets(t, targets, invocation.target, invocation.vars) {
			reached[name] = true
		}
	}
	return emittedContractProfiles(targets, reached)
}

// ciYugabyteMatrixTargets returns the Make targets the database-yugabyte CI job runs, one matrix job each: the
// YUGABYTE_CI_JOBS words the changes job lists through `make yugabyte-ci-matrix`. It fails unless the workflow wires
// the matrix from that listing and each matrix job runs its target.
func ciYugabyteMatrixTargets(t *testing.T, workflow, makefile string) []string {
	t.Helper()
	changes := ciJob(t, workflow, "changes", "go-build")
	if !strings.Contains(changes, "yugabyte_matrix: ${{ steps.yugabyte_matrix.outputs.matrix }}") ||
		!strings.Contains(changes, `run: echo "matrix=$(make --no-print-directory -s yugabyte-ci-matrix)" >> "$GITHUB_OUTPUT"`) {
		t.Fatal("changes CI job must publish the yugabyte-ci-matrix listing as its yugabyte_matrix output")
	}
	job := ciJob(t, workflow, "database-yugabyte", "database-yugabyte-services")
	if !strings.Contains(job, "matrix: ${{ fromJSON(needs.changes.outputs.yugabyte_matrix) }}") ||
		!strings.Contains(job, "run: make ${{ matrix.target }}\n") {
		t.Fatal("database-yugabyte CI job must run one matrix job per yugabyte-ci-matrix target")
	}
	matrix, ok := parseMakeTargets(makefile)["yugabyte-ci-matrix"]
	if !ok || len(matrix.recipe) != 1 || !strings.Contains(matrix.recipe[0].text, "$(YUGABYTE_CI_JOBS)") {
		t.Fatal("yugabyte-ci-matrix must list YUGABYTE_CI_JOBS")
	}
	jobs := strings.Fields(makeSimpleVariables(makefile)["YUGABYTE_CI_JOBS"])
	if len(jobs) == 0 {
		t.Fatal("YUGABYTE_CI_JOBS names no targets")
	}
	return jobs
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestDatabaseContractCIJobUploadsEveryProfileItWrites(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	job := ciJob(t, readRepoFile(t, ".github/workflows/ci.yml"), "migration-release-state", "database-yugabyte")
	profiles := ciContractProfiles(t, makefile, job)
	for _, engine := range []string{"postgres", "clickhouse", "valkey"} {
		found := false
		for _, profile := range sortedKeys(profiles) {
			if !strings.HasPrefix(profile, engine+"/") {
				continue
			}
			found = true
			if !strings.Contains(job, "coverage/contracts/"+profile+".out") {
				t.Errorf("migration-release-state writes %s contract profile %q but does not upload it to Codecov", engine, profile)
			}
		}
		if !found {
			t.Errorf("migration-release-state writes no %s contract profiles", engine)
		}
	}
	for _, profile := range []string{"postgres/cli-backup-restore", "clickhouse/cli-backup-restore", "postgres/quartermaster-capabilities", "postgres/bosun"} {
		if !profiles[profile] {
			t.Errorf("migration-release-state no longer runs the %q contract", profile)
		}
	}
}
