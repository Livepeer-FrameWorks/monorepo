package services

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// InteractiveSelect returns a selected set of services and the profile used (if any).
// It presents a simple numeric menu grouped by role and allows toggling selections.
func InteractiveSelect(c Catalog) ([]ServiceSpec, string, error) {
	// Build role -> services map
	groups := map[string][]ServiceSpec{}
	roles := []string{}
	for name, s := range c.Services {
		s.Name = name // ensure key name
		groups[s.Role] = append(groups[s.Role], s)
	}
	for r := range groups {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		sort.Slice(groups[r], func(i, j int) bool { return groups[r][i].Name < groups[r][j].Name })
	}
	// Initial selection: central-all if present
	selected := map[string]bool{}
	profile := ""
	if list, ok := c.Profiles["central-all"]; ok {
		for _, n := range list {
			selected[n] = true
		}
		profile = "central-all"
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Select services to include. You can:")
	fmt.Println(" - type numbers separated by spaces to toggle (e.g., '1 3 5')")
	fmt.Println(" - type 'all' or 'none' to select/deselect all")
	fmt.Println(" - type 'profile <name>' to load a preset (e.g., profile control-core)")
	fmt.Println(" - press Enter with no input to accept current selection")
	// Build index list
	idxToName := []string{}
	for _, r := range roles {
		for _, s := range groups[r] {
			idxToName = append(idxToName, s.Name)
		}
	}
	for {
		// Print menu
		i := 1
		for _, r := range roles {
			fmt.Printf("\n[%s]\n", cases.Title(language.English).String(r))
			for _, s := range groups[r] {
				mark := "[ ]"
				if selected[s.Name] {
					mark = "[x]"
				}
				fmt.Printf(" %2d. %-16s %-22s %s\n", i, s.Name, s.Title, mark)
				i++
			}
		}
		fmt.Print("\nSelection> ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		lower := strings.ToLower(line)
		switch {
		case lower == "all":
			for _, name := range idxToName {
				selected[name] = true
			}
			profile = "custom"
			continue
		case lower == "none":
			for _, name := range idxToName {
				selected[name] = false
			}
			profile = "custom"
			continue
		case strings.HasPrefix(lower, "profile "):
			p := strings.TrimSpace(strings.TrimPrefix(lower, "profile "))
			if list, ok := c.Profiles[p]; ok {
				for _, name := range idxToName {
					selected[name] = false
				}
				for _, n := range list {
					selected[n] = true
				}
				profile = p
			} else {
				fmt.Printf("unknown profile: %s\n", p)
			}
			continue
		}
		toks := strings.Fields(line)
		for _, t := range toks {
			n, err := strconv.Atoi(t)
			if err != nil || n < 1 || n > len(idxToName) {
				fmt.Printf("skip %q\n", t)
				continue
			}
			name := idxToName[n-1]
			selected[name] = !selected[name]
			profile = "custom"
		}
	}
	// Build output
	out := []ServiceSpec{}
	for _, r := range roles {
		for _, s := range groups[r] {
			if selected[s.Name] {
				out = append(out, s)
			}
		}
	}
	return out, profile, nil
}
