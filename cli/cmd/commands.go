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
	Path       []string             `json:"path"`
	Command    string               `json:"command"`
	Use        string               `json:"use"`
	Short      string               `json:"short,omitempty"`
	Long       string               `json:"long,omitempty"`
	Runnable   bool                 `json:"runnable"`
	Hidden     bool                 `json:"hidden,omitempty"`
	Deprecated string               `json:"deprecated,omitempty"`
	Risk       string               `json:"risk,omitempty"`
	Flags      []commandCatalogFlag `json:"flags,omitempty"`
}

type commandCatalogFlag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Usage      string `json:"usage,omitempty"`
	Default    string `json:"default,omitempty"`
	Type       string `json:"type,omitempty"`
	Scope      string `json:"scope"`
	Required   bool   `json:"required,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
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
			Path:       commandPathParts(c),
			Command:    c.CommandPath(),
			Use:        c.Use,
			Short:      c.Short,
			Long:       strings.TrimSpace(c.Long),
			Runnable:   catalogCommandRunnable(c),
			Hidden:     c.Hidden,
			Deprecated: c.Deprecated,
			Risk:       inferCommandRisk(c),
			Flags:      commandFlags(c, includeHidden),
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
	var flags []commandCatalogFlag
	add := func(scope string, set *pflag.FlagSet) {
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
				Required:   flagRequired(flag),
				Hidden:     flag.Hidden,
				Deprecated: flag.Deprecated,
			})
		})
	}
	add("local", cmd.LocalNonPersistentFlags())
	add("persistent", cmd.PersistentFlags())
	add("inherited", cmd.InheritedFlags())
	return flags
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
