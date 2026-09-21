package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreFenceDatabaseEnumeration(t *testing.T) {
	for _, test := range []struct {
		name    string
		listing string
		apply   string
		wantOK  bool
	}{
		{"connection failure", "exit 2", "echo fence-applied", false},
		{"SQL failure", "exit 3", "echo fence-applied", false},
		{"empty result", "exit 0", "echo fence-applied", false},
		{"fence SQL failure", "echo foghorn", "exit 3", false},
		{"successful enumeration", "echo postgres; echo foghorn", "echo fence-applied", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\ncase \"$*\" in\n  *datallowconn*) " + test.listing + ";;\n  *) " + test.apply + ";;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(raiseMediaAuthorityRestoreFenceCommand(), "/opt/frameworks/postgres", dir)
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != test.wantOK {
				t.Fatalf("exit=%v output=%s", err, out)
			}
			if strings.Contains(string(out), "fence-applied") != test.wantOK {
				t.Fatalf("unexpected fence execution: %s", out)
			}
		})
	}
}
