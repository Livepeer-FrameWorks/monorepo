package config

import (
	"reflect"
	"testing"

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
	if got := streams["live"]["source"]; got != "balance:http://foghorn:18008?fallback=push://" {
		t.Fatalf("live source = %v", got)
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
