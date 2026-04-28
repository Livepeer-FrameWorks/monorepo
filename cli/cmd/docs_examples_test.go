package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestWebsiteDocsFrameworksCommandsExist(t *testing.T) {
	docsRoot := filepath.Join("..", "..", "website_docs", "src", "content", "docs")
	root := NewRootCmd()

	var checked int
	err := filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, invocation := range extractFrameworksInvocations(string(content)) {
			checked++
			if err := validateFrameworksInvocation(root, invocation); err != nil {
				t.Errorf("%s: %s: %v", path, strings.Join(invocation, " "), err)
			}
			if err := validateFrameworksInvocationFlags(root, invocation); err != nil {
				t.Errorf("%s: %s: %v", path, strings.Join(invocation, " "), err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no frameworks command examples found in website docs")
	}
}

func TestCLIReferenceCoversCommandTree(t *testing.T) {
	referencePath := filepath.Join("..", "..", "website_docs", "src", "content", "docs", "operators", "cli-reference.mdx")
	contentBytes, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	for _, commandPath := range commandPaths(NewRootCmd(), nil) {
		if !strings.Contains(content, commandPath) {
			t.Errorf("cli reference does not mention %q", commandPath)
		}
	}
}

func TestCLIReferenceFlagsMatchCobra(t *testing.T) {
	referencePath := filepath.Join("..", "..", "website_docs", "src", "content", "docs", "operators", "cli-reference.mdx")
	contentBytes, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	root := NewRootCmd()
	commands := commandsByPath(root, nil)
	sections := cliReferenceCommandSections(content)

	for path, section := range sections {
		cmd := commands[path]
		if cmd == nil || !commandRunnable(cmd) || cmd.HasSubCommands() {
			continue
		}
		for flagName := range extractLongFlagsFromFlagList(section) {
			if !flagAvailable(cmd, flagName) {
				t.Errorf("cli reference documents --%s for %q, but Cobra does not define that flag", flagName, path)
			}
		}
	}

	for path, cmd := range commands {
		required := requiredLocalFlags(cmd)
		if len(required) == 0 {
			continue
		}
		section := cliReferenceSectionFor(path, sections)
		if section == "" {
			t.Errorf("cli reference has no section covering required flags for %q", path)
			continue
		}
		documented := extractLongFlagsFromText(section)
		for _, flagName := range required {
			if !documented[flagName] {
				t.Errorf("cli reference section for %q does not document required flag --%s", path, flagName)
			}
		}
	}
}

func commandPaths(cmd *cobra.Command, parent []string) []string {
	if cmd.Hidden {
		return nil
	}
	name := cmd.Name()
	pathParts := append(append([]string{}, parent...), name)
	var paths []string
	if len(parent) > 0 {
		paths = append(paths, strings.Join(pathParts, " "))
	}
	for _, child := range cmd.Commands() {
		paths = append(paths, commandPaths(child, pathParts)...)
	}
	return paths
}

func commandsByPath(cmd *cobra.Command, parent []string) map[string]*cobra.Command {
	commands := map[string]*cobra.Command{}
	if cmd.Hidden {
		return commands
	}
	name := cmd.Name()
	pathParts := append(append([]string{}, parent...), name)
	if len(parent) > 0 {
		commands[strings.Join(pathParts, " ")] = cmd
	}
	for _, child := range cmd.Commands() {
		for path, childCmd := range commandsByPath(child, pathParts) {
			commands[path] = childCmd
		}
	}
	return commands
}

func extractFrameworksInvocations(content string) [][]string {
	var invocations [][]string
	fenceRE := regexp.MustCompile("(?ms)^```(?:bash|sh|shell)\\s*\n(.*?)^```")
	for _, match := range fenceRE.FindAllStringSubmatch(content, -1) {
		for _, command := range logicalShellLines(match[1]) {
			fields := strings.Fields(command)
			for len(fields) > 0 && isShellAssignment(fields[0]) {
				fields = fields[1:]
			}
			if len(fields) > 0 && fields[0] == "frameworks" {
				invocations = append(invocations, fields)
			}
		}
	}
	return invocations
}

func logicalShellLines(script string) []string {
	var commands []string
	var current strings.Builder
	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "$ "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(line)
		if continued {
			continue
		}
		commands = append(commands, current.String())
		current.Reset()
	}
	if current.Len() > 0 {
		commands = append(commands, current.String())
	}
	return commands
}

func validateFrameworksInvocation(root *cobra.Command, fields []string) error {
	_, err := resolveFrameworksInvocation(root, fields)
	return err
}

func validateFrameworksInvocationFlags(root *cobra.Command, fields []string) error {
	cmd, err := resolveFrameworksInvocation(root, fields)
	if err != nil || cmd == nil {
		return err
	}
	for flagName := range extractLongFlagsFromFields(fields) {
		if !flagAvailable(cmd, flagName) {
			return fmt.Errorf("unknown flag --%s for %q", flagName, cmd.CommandPath())
		}
	}
	return nil
}

func resolveFrameworksInvocation(root *cobra.Command, fields []string) (*cobra.Command, error) {
	current := root
	path := []string{"frameworks"}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") || isShellOperator(field) {
			return current, nil
		}
		if next := findChildCommand(current, field); next != nil {
			current = next
			path = append(path, field)
			continue
		}
		if current == root || (!commandRunnable(current) && current.HasSubCommands()) {
			return nil, &unknownSubcommandError{parent: strings.Join(path, " "), child: field}
		}
		return current, nil
	}
	return current, nil
}

func cliReferenceCommandSections(content string) map[string]string {
	sections := map[string]string{}
	headingRE := regexp.MustCompile(`(?m)^### (frameworks(?: [^\n]+)?)\s*$`)
	matches := headingRE.FindAllStringSubmatchIndex(content, -1)
	for i, match := range matches {
		heading := content[match[2]:match[3]]
		if strings.Contains(heading, `\*`) || strings.Contains(heading, "*") {
			continue
		}
		start := match[1]
		end := len(content)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		sections[heading] = content[start:end]
	}
	return sections
}

func cliReferenceSectionFor(commandPath string, sections map[string]string) string {
	parts := strings.Fields(commandPath)
	for len(parts) > 1 {
		path := strings.Join(parts, " ")
		if section := sections[path]; section != "" {
			return section
		}
		parts = parts[:len(parts)-1]
	}
	return ""
}

func extractLongFlagsFromText(content string) map[string]bool {
	return extractLongFlagsFromFields(strings.Fields(content))
}

func extractLongFlagsFromFlagList(content string) map[string]bool {
	var fields []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- `--") {
			fields = append(fields, strings.Fields(line)...)
		}
	}
	return extractLongFlagsFromFields(fields)
}

func extractLongFlagsFromFields(fields []string) map[string]bool {
	flags := map[string]bool{}
	for _, field := range fields {
		if field == "--" || !strings.HasPrefix(field, "--") || strings.Contains(field, "*") {
			continue
		}
		name := strings.TrimPrefix(field, "--")
		name = strings.Trim(name, "`'\"“”‘’.,:;()[]{}")
		if idx := strings.IndexAny(name, "=,"); idx >= 0 {
			name = name[:idx]
		}
		var b strings.Builder
		for _, r := range name {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				b.WriteRune(r)
				continue
			}
			break
		}
		name = strings.Trim(b.String(), "-")
		if name != "" {
			flags[name] = true
		}
	}
	return flags
}

func flagAvailable(cmd *cobra.Command, name string) bool {
	if name == "help" {
		return true
	}
	if cmd.Flag(name) != nil {
		return true
	}
	for current := cmd; current != nil; current = current.Parent() {
		if current.LocalFlags().Lookup(name) != nil || current.PersistentFlags().Lookup(name) != nil {
			return true
		}
	}
	return false
}

func requiredLocalFlags(cmd *cobra.Command) []string {
	var required []string
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		if _, ok := flag.Annotations[cobra.BashCompOneRequiredFlag]; ok {
			required = append(required, flag.Name)
		}
	})
	return required
}

func findChildCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
		for _, alias := range child.Aliases {
			if alias == name {
				return child
			}
		}
	}
	return nil
}

func commandRunnable(cmd *cobra.Command) bool {
	return cmd.Run != nil || cmd.RunE != nil
}

func isShellAssignment(field string) bool {
	idx := strings.IndexByte(field, '=')
	if idx <= 0 {
		return false
	}
	for _, r := range field[:idx] {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isShellOperator(field string) bool {
	switch field {
	case "|", "||", "&&", ";", ">":
		return true
	default:
		return false
	}
}

type unknownSubcommandError struct {
	parent string
	child  string
}

func (e *unknownSubcommandError) Error() string {
	return "unknown subcommand " + e.child + " after " + e.parent
}
