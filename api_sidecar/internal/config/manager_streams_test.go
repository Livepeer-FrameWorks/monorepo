package config

import (
	"reflect"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestStreamConfigsFromSeedSkipsWildcardInstances(t *testing.T) {
	seed := &pb.ConfigSeed{
		Templates: []*pb.StreamTemplate{
			{Def: &pb.StreamDef{Name: "live", Tags: []string{"live"}}},
			{Def: &pb.StreamDef{Name: "processing", Realtime: true, Tags: []string{"processing"}}},
			{Def: &pb.StreamDef{Name: "processing+$", Realtime: true}},
			{Def: &pb.StreamDef{Name: "processing+artifact-hash", Realtime: true}},
			{Def: &pb.StreamDef{Name: "dvr", Tags: []string{"dvr"}}},
			{Def: &pb.StreamDef{Name: "pull", Tags: []string{"pull"}}},
		},
	}

	streams := streamConfigsFromSeed(seed, "http://foghorn:18008")

	if _, ok := streams["processing+$"]; ok {
		t.Fatal("processing+$ must not be synced as a configured Mist stream")
	}
	if _, ok := streams["processing+artifact-hash"]; ok {
		t.Fatal("processing+ wildcard instances must not be synced as configured Mist streams")
	}
	if got := streams["processing"]["source"]; got != inertMistSource {
		t.Fatalf("processing source = %v, want %q", got, inertMistSource)
	}
	if got := streams["dvr"]["source"]; got != inertMistSource {
		t.Fatalf("dvr source = %v, want %q", got, inertMistSource)
	}
	if got := streams["dvr"]["realtime"]; got != false {
		t.Fatalf("dvr realtime = %v, want false from seed", got)
	}
	if got := streams["dvr"]["DVR"]; got != 120000 {
		t.Fatalf("dvr DVR = %v, want 120000", got)
	}
	if got := streams["dvr"]["bufferTime"]; got != 120000 {
		t.Fatalf("dvr bufferTime = %v, want 120000", got)
	}
	if got := streams["dvr"]["inputtimeout"]; got != 12 {
		t.Fatalf("dvr inputtimeout = %v, want 12", got)
	}
	if got := streams["pull"]["source"]; got != "balance:http://foghorn:18008" {
		t.Fatalf("pull source = %v", got)
	}
	// Live wildcard source: balance:<foghorn>, identical shape to pull.
	// Foghorn's /source dispatch decides the terminal answer: DTSC when
	// the stream is live anywhere, push:// as the publisher safety net,
	// offline:<reason> when neither applies.
	if got := streams["live"]["source"]; got != "balance:http://foghorn:18008" {
		t.Fatalf("live source = %v, want balance:http://foghorn:18008", got)
	}
	if got := streams["live"]["DVR"]; got != 120000 {
		t.Fatalf("live DVR = %v, want 120000", got)
	}
	if got := streams["live"]["resume"]; got != 1 {
		t.Fatalf("live resume = %v, want 1", got)
	}
	if got := streams["live"]["inputtimeout"]; got != 12 {
		t.Fatalf("live inputtimeout = %v, want 12", got)
	}
}

func TestReconcileConfiguresGlobalStreamProcessTrigger(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &pb.ConfigSeed{
			FoghornBalancerBase: "http://foghorn:18008",
			Templates: []*pb.StreamTemplate{
				{Def: &pb.StreamDef{Name: "live"}},
			},
		},
	}

	manager.reconcile()

	if len(mist.updatedConfigs) == 0 {
		t.Fatal("expected UpdateConfig call")
	}
	triggers, ok := mist.updatedConfigs[0]["triggers"].(map[string]any)
	if !ok {
		t.Fatalf("missing triggers in UpdateConfig: %#v", mist.updatedConfigs[0])
	}
	rawHandlers, ok := triggers["STREAM_PROCESS"].([]any)
	if !ok || len(rawHandlers) != 1 {
		t.Fatalf("STREAM_PROCESS trigger = %#v", triggers["STREAM_PROCESS"])
	}
	handler, ok := rawHandlers[0].(map[string]any)
	if !ok {
		t.Fatalf("STREAM_PROCESS handler = %#v", rawHandlers[0])
	}
	if _, scoped := handler["streams"]; scoped {
		t.Fatalf("STREAM_PROCESS must not be stream-scoped; managed streams use bare names: %#v", handler)
	}
}

func TestReconcileConfiguresPushInputCloseTrigger(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &pb.ConfigSeed{
			FoghornBalancerBase: "http://foghorn:18008",
			Templates: []*pb.StreamTemplate{
				{Def: &pb.StreamDef{Name: "live"}},
			},
		},
	}

	manager.reconcile()

	if len(mist.updatedConfigs) == 0 {
		t.Fatal("expected UpdateConfig call")
	}
	triggers, ok := mist.updatedConfigs[0]["triggers"].(map[string]any)
	if !ok {
		t.Fatalf("missing triggers in UpdateConfig: %#v", mist.updatedConfigs[0])
	}
	rawHandlers, ok := triggers["PUSH_INPUT_CLOSE"].([]any)
	if !ok || len(rawHandlers) != 1 {
		t.Fatalf("PUSH_INPUT_CLOSE trigger = %#v", triggers["PUSH_INPUT_CLOSE"])
	}
	handler, ok := rawHandlers[0].(map[string]any)
	if !ok {
		t.Fatalf("PUSH_INPUT_CLOSE handler = %#v", rawHandlers[0])
	}
	if got := handler["sync"]; got != false {
		t.Fatalf("PUSH_INPUT_CLOSE must be async, got sync=%v", got)
	}
	if got, _ := handler["handler"].(string); got == "" || got[len(got)-len("/push_input_close"):] != "/push_input_close" {
		t.Fatalf("PUSH_INPUT_CLOSE handler URL = %v", got)
	}
}

func TestStaleManagedWildcardStreams(t *testing.T) {
	current := map[string]any{
		"streams": map[string]any{
			"live":                     map[string]any{},
			"processing":               map[string]any{},
			"processing+":              map[string]any{},
			"processing+$":             map[string]any{},
			"processing+artifact-hash": map[string]any{},
			"pull+$":                   map[string]any{},
			"dvr+$":                    map[string]any{},
		},
	}

	got := staleManagedWildcardStreams(current)
	want := []string{"dvr+$", "processing+", "processing+$", "pull+$"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staleManagedWildcardStreams() = %#v, want %#v", got, want)
	}
}

func TestMissingManagedStreams(t *testing.T) {
	expected := map[string]map[string]any{
		"live":       {},
		"vod":        {},
		"dvr":        {},
		"processing": {},
		"pull":       {},
	}
	current := map[string]any{
		"streams": map[string]any{
			"live": map[string]any{},
			"vod":  map[string]any{},
		},
	}

	got := missingManagedStreams(current, expected)
	want := []string{"dvr", "processing", "pull"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingManagedStreams() = %#v, want %#v", got, want)
	}
}

func TestMissingManagedStreamsTreatsEmptyConfigAsMissingAll(t *testing.T) {
	expected := map[string]map[string]any{
		"live": {},
		"vod":  {},
	}

	got := missingManagedStreams(map[string]any{"streams": map[string]any{}}, expected)
	want := []string{"live", "vod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingManagedStreams() = %#v, want %#v", got, want)
	}
}
