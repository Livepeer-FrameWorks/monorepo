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
	want := "/opt/frameworks/purser/purser data-migrations list --format json"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSpecFromDetectionNativeUsesDetectedBinaryPath(t *testing.T) {
	t.Parallel()

	spec := SpecFromDetection("native", map[string]string{
		"binary_path": "/srv/frameworks/quartermaster/current",
	}, "quartermaster")
	got, err := Command(spec, []string{"data-migrations", "list"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, want := range []string{
		"sudo -u frameworks -- /bin/bash -eu -c",
		`set -a; . "$1"; set +a`,
		"/etc/frameworks/quartermaster.env",
		"/srv/frameworks/quartermaster",
		"/srv/frameworks/quartermaster/current data-migrations list",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command %q does not contain %q", got, want)
		}
	}
}

func TestSpecFromDetectionNativeUsesDetectedServiceContext(t *testing.T) {
	t.Parallel()

	spec := SpecFromDetection("native", map[string]string{
		"binary_path":       "/srv/bin/commodore",
		"environment_file":  "/srv/etc/commodore.env",
		"working_directory": "/srv/state/commodore",
		"service_user":      "media",
	}, "commodore")
	got, err := Command(spec, []string{"data-migrations", "status", "migration-id"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, want := range []string{
		"sudo -u media --",
		"/srv/etc/commodore.env",
		"/srv/state/commodore",
		"/srv/bin/commodore data-migrations status migration-id",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command %q does not contain %q", got, want)
		}
	}
}

func TestSpecFromDetectionDockerUsesDetectedContainer(t *testing.T) {
	t.Parallel()

	spec := SpecFromDetection("docker", map[string]string{
		"container_name": "frameworks-quartermaster",
	}, "quartermaster")
	got, err := Command(spec, []string{"data-migrations", "list"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := "docker exec frameworks-quartermaster quartermaster data-migrations list"; got != want {
		t.Fatalf("got %q, want %q", got, want)
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
