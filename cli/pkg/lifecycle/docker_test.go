package lifecycle

import (
	"context"
	"testing"

	"frameworks/cli/pkg/system"
)

type recordingRunner struct {
	calls []string
}

func (r *recordingRunner) Run(_ context.Context, command string) (string, int, error) {
	r.calls = append(r.calls, command)
	return "running", 0, nil
}

func TestDockerManagerWrapsComposeWithSudoFallback(t *testing.T) {
	r := &recordingRunner{}
	m, err := NewManager("docker", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Start(ctx, "foghorn"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, "foghorn"); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(ctx, "foghorn"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(ctx, "foghorn"); err != nil {
		t.Fatal(err)
	}
	const compose = "compose -f /opt/frameworks/foghorn/docker-compose.yml "
	want := []string{
		system.DockerCommand(compose + "up -d"),
		system.DockerCommand(compose + "down"),
		system.DockerCommand(compose + "restart"),
		system.DockerCommand(compose + "ps --format '{{.State}}'"),
	}
	if len(r.calls) != len(want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	for i := range want {
		if r.calls[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, r.calls[i], want[i])
		}
	}
}
