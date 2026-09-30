package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// TestCommandExamplesParse parses every `frameworks ...` line of every
// command's Example with Cobra's own flag parser, so an example that names a
// flag or shorthand the command does not define (such as -o for the global
// --output) fails here instead of for the operator who copies it.
func TestCommandExamplesParse(t *testing.T) {
	var checked int
	for path, cmd := range commandsByPath(NewRootCmd(), nil) {
		for _, line := range logicalShellLines(cmd.Example) {
			fields := strings.Fields(line)
			for len(fields) > 0 && isShellAssignment(fields[0]) {
				fields = fields[1:]
			}
			if len(fields) == 0 || fields[0] != "frameworks" {
				continue
			}
			checked++
			if err := parseFrameworksInvocation(fields); err != nil {
				t.Errorf("%s example %q: %v", path, line, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no command examples found")
	}
}

// parseFrameworksInvocation resolves an invocation on a fresh command tree
// and parses its flags the way Cobra would when it runs.
func parseFrameworksInvocation(fields []string) error {
	root := NewRootCmd()
	cmd, err := resolveFrameworksInvocation(root, fields)
	if err != nil {
		return err
	}
	if cmd == nil {
		return fmt.Errorf("no command resolved")
	}
	args := fields[1:]
	for i, field := range args {
		if isShellOperator(field) {
			args = args[:i]
			break
		}
	}
	// Drop the command path; what remains are flags and positional args.
	depth := 0
	for c := cmd; c != root && c != nil; c = c.Parent() {
		depth++
	}
	if depth > len(args) {
		return fmt.Errorf("invocation shorter than command path %q", cmd.CommandPath())
	}
	// Examples stand in for user values with placeholders; only flag names
	// and shorthands are under test.
	return cmd.ParseFlags(args[depth:])
}

func TestParseFrameworksInvocationRejectsUnknownShorthand(t *testing.T) {
	err := parseFrameworksInvocation(strings.Fields("frameworks admin billing prepaid-double-charges --tenant-id x -o json"))
	if err == nil || !strings.Contains(err.Error(), "unknown shorthand flag") {
		t.Fatalf("err = %v, want unknown shorthand flag", err)
	}
	if err := parseFrameworksInvocation(strings.Fields("frameworks admin billing prepaid-double-charges --tenant-id x --output json")); err != nil {
		t.Fatalf("global --output must parse: %v", err)
	}
}
