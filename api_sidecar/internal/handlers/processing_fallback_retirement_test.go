package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

type sourceProbingRetirementRuntime struct {
	fakeProcessingRuntimeClient
	probe func()
}

func (f *sourceProbingRetirementRuntime) NukeStream(name string) error {
	f.probe()
	return f.fakeProcessingRuntimeClient.NukeStream(name)
}

func (f *sourceProbingRetirementRuntime) GetActiveStreams() (map[string]interface{}, error) {
	f.probe()
	return f.fakeProcessingRuntimeClient.GetActiveStreams()
}

func TestProcessingFallbackRefusesSourceRebootsUntilRetiredWriterDrains(t *testing.T) {
	setupTriggerTest(t, "processing-retirement")
	const stream = "processing+retirement"
	const source = "/tmp/retirement-source.mp4"
	setProcessingSourceOverride(stream, source)
	t.Cleanup(func() { clearProcessingSourceOverride(stream) })
	responses := []string{}
	probe := func() {
		ctx, recorder := newWebhookContext(stream)
		HandleStreamSource(ctx)
		responses = append(responses, recorder.Body.String())
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mkv")
	if err := os.WriteFile(output, []byte("old recording"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &sourceProbingRetirementRuntime{probe: probe}
	h := NewProcessingJobHandler(logrus.New(), "", dir, dir)
	if err := h.restartProcessingStreamForLocalFallback(logrus.NewEntry(logrus.New()), client, stream, output, 42); err != nil {
		t.Fatal(err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected source probes during nuke and drain: %v", responses)
	}
	for _, response := range responses {
		if !strings.HasPrefix(response, "offline:") {
			t.Fatalf("retired process can reboot its source during teardown: %q", response)
		}
	}
	probe()
	if responses[2] != source {
		t.Fatalf("local retry cannot resolve its source after drain: %q", responses[2])
	}
}

func TestProcessingFallbackUnblocksSourceAfterCleanupError(t *testing.T) {
	setupTriggerTest(t, "processing-retirement-error")
	const stream = "processing+retirement-error"
	const source = "/tmp/retirement-source.mp4"
	setProcessingSourceOverride(stream, source)
	t.Cleanup(func() { clearProcessingSourceOverride(stream) })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retained-file"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewProcessingJobHandler(logrus.New(), "", dir, dir)
	if err := h.restartProcessingStreamForLocalFallback(logrus.NewEntry(logrus.New()), &fakeProcessingRuntimeClient{}, stream, dir, 42); err == nil {
		t.Fatal("expected cleanup error for nonempty output directory")
	}
	ctx, recorder := newWebhookContext(stream)
	HandleStreamSource(ctx)
	if recorder.Body.String() != source {
		t.Fatalf("cleanup error left a permanent source block: %q", recorder.Body.String())
	}
}
