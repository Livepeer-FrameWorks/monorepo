package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
)

// edgeOperatorComposeFile is the compose file `edge init` renders; the edge
// role names its file docker-compose.yml.
const edgeOperatorComposeFile = "docker-compose.edge.yml"

// edgeStackStore reads and writes files of a deployed edge stack directory.
type edgeStackStore interface {
	// Read returns a file's content; ok is false when the file is absent.
	Read(name string) (content string, ok bool, err error)
	Write(name, content string) error
}

// edgeStackFiles is the edge stack directory on this machine or, with
// sshTarget, on a remote node.
type edgeStackFiles struct {
	ctx       context.Context
	sshTarget string
	sshKey    string
	dir       string
}

func (f edgeStackFiles) path(name string) string {
	if strings.TrimSpace(f.sshTarget) == "" {
		return filepath.Join(f.dir, name)
	}
	return strings.TrimRight(f.dir, "/") + "/" + name
}

func (f edgeStackFiles) Read(name string) (string, bool, error) {
	path := f.path(name)
	if strings.TrimSpace(f.sshTarget) == "" {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return string(raw), err == nil, err
	}
	code, out, errOut, err := xexec.RunSSHWithKey(f.ctx, f.sshTarget, f.sshKey, "sh", []string{"-c", `test -e "$1" || exit 3; cat "$1"`, "_", path}, "")
	if code == 3 {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w: %s", path, err, strings.TrimSpace(errOut))
	}
	return out, true, nil
}

// Write replaces the file through a sibling temp file so a failed write
// never leaves a truncated compose or env file, keeping the existing mode.
func (f edgeStackFiles) Write(name, content string) error {
	path := f.path(name)
	if strings.TrimSpace(f.sshTarget) == "" {
		mode := os.FileMode(0o644)
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	script := `umask 022; printf '%s' "$1" > "$2.tmp" && { chmod --reference="$2" "$2.tmp" 2>/dev/null || true; } && mv "$2.tmp" "$2"`
	if _, _, errOut, err := xexec.RunSSHWithKey(f.ctx, f.sshTarget, f.sshKey, "sh", []string{"-c", script, "_", content, path}, ""); err != nil {
		return fmt.Errorf("write %s: %w: %s", path, err, strings.TrimSpace(errOut))
	}
	return nil
}

// rerenderEdgeStackFiles re-renders the compose file and .edge.env of an
// operator-local container stack from this CLI's templates and writes the
// ones that changed, returning their names.
func rerenderEdgeStackFiles(store edgeStackStore) ([]string, error) {
	read := func(name string, required bool) (string, error) {
		content, ok, err := store.Read(name)
		if err != nil {
			return "", err
		}
		if !ok && required {
			return "", fmt.Errorf("%s not found", name)
		}
		return content, nil
	}
	env, err := read(".edge.env", true)
	if err != nil {
		return nil, err
	}
	compose, err := read(edgeOperatorComposeFile, true)
	if err != nil {
		return nil, err
	}
	vmagent, err := read("vmagent-edge.yml", false)
	if err != nil {
		return nil, err
	}
	token, err := read("telemetry/token", false)
	if err != nil {
		return nil, err
	}
	newCompose, newEnv, err := templates.RerenderEdgeStack(templates.EdgeDeployedStack{
		Env:            env,
		Compose:        compose,
		VMAgentConfig:  vmagent,
		TelemetryToken: token,
	})
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range []struct{ name, old, next string }{
		{edgeOperatorComposeFile, compose, newCompose},
		{".edge.env", env, newEnv},
	} {
		if f.old == f.next {
			continue
		}
		if err := store.Write(f.name, f.next); err != nil {
			return changed, err
		}
		changed = append(changed, f.name)
	}
	return changed, nil
}
