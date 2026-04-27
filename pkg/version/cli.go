package version

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// HandleCLI handles no-config version commands for service binaries. It must
// run before env/config loading so installers can inspect an existing binary
// even when the service is not currently startable.
func HandleCLI() bool {
	handled, err := HandleCommand(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "version: %v\n", err)
		os.Exit(1)
	}
	return handled
}

func HandleCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "version", "--version", "-v":
	default:
		return false, nil
	}
	if len(args) > 1 && args[1] == "--json" {
		if err := json.NewEncoder(out).Encode(GetInfo()); err != nil {
			return true, err
		}
		return true, nil
	}
	name := ComponentName
	if name == "" || name == "unknown" {
		name = "service"
	}
	if _, err := fmt.Fprintf(out, "Frameworks %s\n", name); err != nil {
		return true, err
	}
	if _, err := fmt.Fprintf(out, " - platform version: %s\n", Version); err != nil {
		return true, err
	}
	if _, err := fmt.Fprintf(out, " - component: %s %s\n", ComponentName, ComponentVersion); err != nil {
		return true, err
	}
	if _, err := fmt.Fprintf(out, " - git: %s\n", GitCommit); err != nil {
		return true, err
	}
	return true, nil
}
