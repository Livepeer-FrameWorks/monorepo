package exec

import (
	"strings"
	"testing"
)

func TestCommand_Native(t *testing.T) {
	got, err := Command(Spec{Mode: ModeNative, BinaryName: "purser"}, []string{"data-migrations", "list", "--format", "json"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "/usr/local/bin/purser data-migrations list --format json"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommand_NativeCustomPath(t *testing.T) {
	got, err := Command(Spec{Mode: ModeNative, BinaryName: "purser", InstallPath: "/opt/purser/bin/purser"}, []string{"data-migrations", "list"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(got, "/opt/purser/bin/purser data-migrations list") {
		t.Errorf("custom InstallPath not honored: %q", got)
	}
}

func TestCommand_DockerDefaultContainer(t *testing.T) {
	got, err := Command(Spec{Mode: ModeDocker, BinaryName: "purser"}, []string{"data-migrations", "list"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "docker exec purser purser data-migrations list"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommand_DockerExplicitContainer(t *testing.T) {
	got, err := Command(Spec{Mode: ModeDocker, BinaryName: "purser", ContainerName: "frameworks-purser"}, []string{"data-migrations", "status", "billing-rating-v2"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "docker exec frameworks-purser purser data-migrations status billing-rating-v2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommand_QuotesArgsWithSpaces(t *testing.T) {
	got, err := Command(Spec{Mode: ModeNative, BinaryName: "purser"}, []string{"--scope-value", "a b c"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "'a b c'") {
		t.Errorf("expected quoted arg, got %q", got)
	}
}

func TestCommand_EscapesSingleQuotes(t *testing.T) {
	got, err := Command(Spec{Mode: ModeNative, BinaryName: "purser"}, []string{"--scope-value", "ten'ant"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, `'ten'\''ant'`) {
		t.Errorf("single quote not escaped: %q", got)
	}
}

func TestCommand_EmptyBinaryName(t *testing.T) {
	if _, err := Command(Spec{Mode: ModeNative}, nil); err == nil {
		t.Error("empty BinaryName must error")
	}
}

func TestCommand_UnknownMode(t *testing.T) {
	if _, err := Command(Spec{Mode: "k8s", BinaryName: "purser"}, nil); err == nil {
		t.Error("unknown mode must error")
	}
}
