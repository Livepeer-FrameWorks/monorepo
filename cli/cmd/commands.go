package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type commandCatalog struct {
	Commands []commandCatalogEntry `json:"commands"`
}

type commandCatalogEntry struct {
	Path        []string                 `json:"path"`
	Command     string                   `json:"command"`
	Use         string                   `json:"use"`
	Short       string                   `json:"short,omitempty"`
	Long        string                   `json:"long,omitempty"`
	Runnable    bool                     `json:"runnable"`
	Interactive bool                     `json:"interactive,omitempty"`
	Hidden      bool                     `json:"hidden,omitempty"`
	Deprecated  string                   `json:"deprecated,omitempty"`
	Risk        string                   `json:"risk,omitempty"`
	Arguments   []commandCatalogArgument `json:"arguments,omitempty"`
	Flags       []commandCatalogFlag     `json:"flags,omitempty"`
}

type commandCatalogFlag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Usage      string `json:"usage,omitempty"`
	Default    string `json:"default,omitempty"`
	Type       string `json:"type,omitempty"`
	Scope      string `json:"scope"`
	Source     string `json:"source,omitempty"`
	Required   bool   `json:"required,omitempty"`
	Sensitive  bool   `json:"sensitive,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
}

type commandCatalogArgument struct {
	Name     string `json:"name"`
	Raw      string `json:"raw"`
	Required bool   `json:"required,omitempty"`
	Variadic bool   `json:"variadic,omitempty"`
}

func newCommandsCmd() *cobra.Command {
	var includeHidden bool
	cmd := &cobra.Command{
		Use:   "commands",
		Short: "Print the CLI command catalog",
		Long: `Print a machine-readable catalog of the CLI command tree.

The macOS tray uses this to discover command paths and flags directly from
Cobra instead of hardcoding CLI capabilities.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog := buildCommandCatalog(cmd.Root(), includeHidden)
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(catalog)
			}
			for _, entry := range catalog.Commands {
				if entry.Runnable {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", entry.Command, entry.Short)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&includeHidden, "include-hidden", false, "include hidden commands and flags")
	return cmd
}

func buildCommandCatalog(root *cobra.Command, includeHidden bool) commandCatalog {
	var entries []commandCatalogEntry
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden && !includeHidden {
			return
		}
		entries = append(entries, commandCatalogEntry{
			Path:        commandPathParts(c),
			Command:     c.CommandPath(),
			Use:         c.Use,
			Short:       c.Short,
			Long:        strings.TrimSpace(c.Long),
			Runnable:    catalogCommandRunnable(c),
			Interactive: inferCommandInteractive(c),
			Hidden:      c.Hidden,
			Deprecated:  c.Deprecated,
			Risk:        inferCommandRisk(c),
			Arguments:   commandArguments(c),
			Flags:       commandFlags(c, includeHidden),
		})
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)
	return commandCatalog{Commands: entries}
}

func commandPathParts(cmd *cobra.Command) []string {
	parts := strings.Fields(cmd.CommandPath())
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, "-") {
			continue
		}
		out = append(out, part)
	}
	return out
}

func catalogCommandRunnable(cmd *cobra.Command) bool {
	return cmd.Run != nil || cmd.RunE != nil
}

func commandFlags(cmd *cobra.Command, includeHidden bool) []commandCatalogFlag {
	seen := map[string]bool{}
	flags := []commandCatalogFlag{}
	add := func(scope string, source *cobra.Command, set *pflag.FlagSet) {
		set.VisitAll(func(flag *pflag.Flag) {
			if seen[flag.Name] {
				return
			}
			if flag.Hidden && !includeHidden {
				return
			}
			seen[flag.Name] = true
			flags = append(flags, commandCatalogFlag{
				Name:       flag.Name,
				Shorthand:  flag.Shorthand,
				Usage:      flag.Usage,
				Default:    flag.DefValue,
				Type:       flag.Value.Type(),
				Scope:      scope,
				Source:     source.CommandPath(),
				Required:   flagRequired(flag),
				Sensitive:  flagSensitive(flag),
				Hidden:     flag.Hidden,
				Deprecated: flag.Deprecated,
			})
		})
	}
	add("local", cmd, cmd.LocalNonPersistentFlags())
	add("persistent", cmd, cmd.PersistentFlags())
	for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
		add("inherited", parent, parent.PersistentFlags())
	}
	return flags
}

func commandArguments(cmd *cobra.Command) []commandCatalogArgument {
	fields := strings.Fields(cmd.Use)
	if len(fields) <= 1 {
		return nil
	}
	args := []commandCatalogArgument{}
	for _, field := range fields[1:] {
		arg, ok := commandArgument(field)
		if ok {
			args = append(args, arg)
		}
	}
	return args
}

func commandArgument(field string) (commandCatalogArgument, bool) {
	raw := strings.Trim(strings.TrimSpace(field), ",")
	if raw == "" {
		return commandCatalogArgument{}, false
	}
	normalized := strings.ToLower(strings.Trim(raw, "[]<>"))
	if normalized == "flags" || normalized == "options" || normalized == "command" || normalized == "commands" {
		return commandCatalogArgument{}, false
	}

	required := !strings.HasPrefix(raw, "[")
	name := strings.Trim(raw, "[]<>")
	variadic := strings.Contains(name, "...")
	name = strings.TrimPrefix(strings.TrimSuffix(name, "..."), "...")
	if name == "" {
		return commandCatalogArgument{}, false
	}
	return commandCatalogArgument{
		Name:     name,
		Raw:      raw,
		Required: required,
		Variadic: variadic,
	}, true
}

func flagRequired(flag *pflag.Flag) bool {
	if flag.Annotations == nil {
		return flagUsageMarksRequired(flag.Usage)
	}
	if _, ok := flag.Annotations[cobra.BashCompOneRequiredFlag]; ok {
		return true
	}
	return flagUsageMarksRequired(flag.Usage)
}

func flagUsageMarksRequired(usage string) bool {
	usage = strings.ToLower(strings.TrimSpace(usage))
	return strings.Contains(usage, "(required)") ||
		strings.Contains(usage, ", required)") ||
		strings.Contains(usage, " required)")
}

func inferCommandRisk(cmd *cobra.Command) string {
	path := strings.Join(commandPathParts(cmd), " ")
	for _, token := range []string{
		" delete", " revoke", " remove", " down", " drain", " restore", " migrate",
		" provision", " init", " upgrade", " restart", " update", " set ", " create",
		" grant", " accept", " promote", " fund", " withdraw", " unlock",
	} {
		if strings.Contains(" "+path+" ", token) {
			return "mutating"
		}
	}
	return ""
}

func inferCommandInteractive(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "frameworks login", "frameworks menu", "frameworks setup":
		return true
	default:
		return false
	}
}

func flagSensitive(flag *pflag.Flag) bool {
	name := strings.ToLower(flag.Name)
	return strings.Contains(name, "password") ||
		strings.Contains(name, "secret") ||
		strings.Contains(name, "token")
}
