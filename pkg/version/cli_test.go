package version

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleCommandVersionText(t *testing.T) {
	old := GetInfo()
	Version = "v1.2.3"
	GitCommit = "abcdef123456"
	BuildDate = "2026-04-27T00:00:00Z"
	ComponentName = "privateer"
	ComponentVersion = "0.2.0"
	t.Cleanup(func() {
		Version = old.Version
		GitCommit = old.GitCommit
		BuildDate = old.BuildDate
		ComponentName = old.ComponentName
		ComponentVersion = old.ComponentVersion
	})

	var out bytes.Buffer
	handled, err := HandleCommand([]string{"version"}, &out)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}
	if !handled {
		t.Fatal("version command was not handled")
	}
	got := out.String()
	for _, want := range []string{
		"Frameworks privateer",
		"platform version: v1.2.3",
		"component: privateer 0.2.0",
		"git: abcdef123456",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q missing %q", got, want)
		}
	}
}

func TestHandleCommandVersionJSON(t *testing.T) {
	old := GetInfo()
	Version = "v1.2.3"
	GitCommit = "abcdef123456"
	BuildDate = "2026-04-27T00:00:00Z"
	ComponentName = "bridge"
	ComponentVersion = "0.2.0"
	t.Cleanup(func() {
		Version = old.Version
		GitCommit = old.GitCommit
		BuildDate = old.BuildDate
		ComponentName = old.ComponentName
		ComponentVersion = old.ComponentVersion
	})

	var out bytes.Buffer
	handled, err := HandleCommand([]string{"version", "--json"}, &out)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}
	if !handled {
		t.Fatal("version --json command was not handled")
	}
	var info Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if info.Version != "v1.2.3" || info.ComponentName != "bridge" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestHandleCommandIgnoresNonVersion(t *testing.T) {
	var out bytes.Buffer
	handled, err := HandleCommand([]string{"serve"}, &out)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}
	if handled {
		t.Fatal("non-version command should not be handled")
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
