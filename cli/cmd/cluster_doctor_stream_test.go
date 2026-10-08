package cmd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"frameworks/cli/pkg/inventory"
)

// lockedBuffer lets a test read doctor output while runDoctor is still writing it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// silentKafkaBroker accepts connections and never answers, so a Kafka probe against it hangs until its read timeout.
func silentKafkaBroker(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return listener.Addr().(*net.TCPAddr).Port
}

// refusedPort is a loopback port nothing listens on, so a Kafka probe against it fails at once.
func refusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// startKafkaDoctor runs runDoctor against a cluster whose first broker hangs and whose second refuses connections.
func startKafkaDoctor(t *testing.T) (*lockedBuffer, context.CancelFunc, <-chan error) {
	t.Helper()
	manifest := &inventory.Manifest{
		Type:  "cluster",
		Hosts: map[string]inventory.Host{"kafka-1": {Name: "kafka-1", ExternalIP: "127.0.0.1"}},
		Infrastructure: inventory.InfrastructureConfig{Kafka: &inventory.KafkaConfig{Enabled: true, Brokers: []inventory.KafkaBroker{
			{Host: "kafka-1", ID: 1, Port: silentKafkaBroker(t)},
			{Host: "kafka-1", ID: 2, Port: refusedPort(t)},
		}}},
	}
	rc := &resolvedCluster{Manifest: manifest, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml"), Cleanup: func() {}}
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runDoctor(cmd, rc, false) }()
	t.Cleanup(cancel)
	return out, cancel, done
}

func waitForOutput(out *lockedBuffer, want string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestDoctorPrintsAFinishedCheckWhileAnotherHangs proves checks run concurrently and each result prints when it
// finishes: the refused broker is reported while the silent broker's probe is still waiting for its 5s timeout.
func TestDoctorPrintsAFinishedCheckWhileAnotherHangs(t *testing.T) {
	out, cancel, done := startKafkaDoctor(t)
	if !waitForOutput(out, "Kafka Broker 2", 3*time.Second) {
		t.Fatalf("Kafka Broker 2 was not reported while Kafka Broker 1 hung; output so far:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Kafka Broker 1 ") {
		t.Fatalf("Kafka Broker 1 reported before its probe timed out; output:\n%s", out.String())
	}
	cancel()
	<-done
	t.Logf("output:\n%s", out.String())
}

// TestDoctorSectionsAnnounceSlowChecksStillRunning pins the section rendering: a later section's finished checks print
// first, then a progress line for each slow check still running, then that check's result when it finishes.
func TestDoctorSectionsAnnounceSlowChecksStillRunning(t *testing.T) {
	releaseFirst, releaseSlow, fastDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	block := func(release <-chan struct{}) func(context.Context) doctorOutcome {
		return func(context.Context) doctorOutcome {
			<-release
			return doctorOutcome{miss: "done"}
		}
	}
	sections := []doctorSection{
		{title: "First", checks: []doctorCheck{{name: "first", run: block(releaseFirst)}}},
		{title: "Second", checks: []doctorCheck{
			{name: "fast", run: func(context.Context) doctorOutcome { return doctorOutcome{miss: "done"} }},
			{name: "slow", progress: "still reading", run: func(ctx context.Context) doctorOutcome {
				// The other worker is blocked in First, so reaching this job proves
				// the fast result was delivered before First is released.
				close(fastDone)
				return block(releaseSlow)(ctx)
			}},
		}},
	}
	out := &lockedBuffer{}
	type result struct {
		outcomes    [][]doctorOutcome
		interrupted bool
	}
	finished := make(chan result, 1)
	go func() {
		outcomes, interrupted := runDoctorSections(context.Background(), out, sections, 2, func(name string, _ doctorOutcome) {
			_, _ = out.Write([]byte("result " + name + "\n"))
		})
		finished <- result{outcomes, interrupted}
	}()
	<-fastDone
	close(releaseFirst)
	if !waitForOutput(out, "… slow: still reading", 2*time.Second) {
		t.Fatalf("no progress line for the running slow check; output:\n%s", out.String())
	}
	close(releaseSlow)
	got := <-finished
	if got.interrupted || got.outcomes[1][0].miss != "done" || got.outcomes[1][1].miss != "done" {
		t.Fatalf("outcomes = %+v interrupted %v, want both second-section outcomes in check order", got.outcomes, got.interrupted)
	}
	want := []string{"First:", "result first", "Second:", "result fast", "… slow: still reading", "result slow"}
	text, last := out.String(), -1
	for _, line := range want {
		at := strings.Index(text, line)
		if at <= last {
			t.Fatalf("output order wrong at %q; want %v in order:\n%s", line, want, text)
		}
		last = at
	}
}

// TestDoctorInterruptPrintsInterruptedOnceAndNoFailures pins Ctrl+C: doctor stops at once, says so once, and does not
// render the checks it abandoned as failures.
func TestDoctorInterruptPrintsInterruptedOnceAndNoFailures(t *testing.T) {
	out, cancel, done := startKafkaDoctor(t)
	if !waitForOutput(out, "Infrastructure Health", 3*time.Second) {
		t.Fatalf("doctor did not start; output:\n%s", out.String())
	}
	time.Sleep(300 * time.Millisecond)
	before := out.String()
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("doctor kept running after interrupt; output:\n%s", out.String())
	}
	after := strings.TrimPrefix(out.String(), before)
	if got := strings.Count(out.String(), "interrupted"); got != 1 {
		t.Fatalf("output mentions interrupted %d times, want once:\n%s", got, out.String())
	}
	for _, unwanted := range []string{"context canceled", "Kafka Broker 1", "Edge config version", "healthy"} {
		if strings.Contains(after, unwanted) {
			t.Fatalf("output after the interrupt contains %q:\n%s", unwanted, after)
		}
	}
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 130 {
		t.Fatalf("runDoctor error = %v, want exit code 130", err)
	}
}
