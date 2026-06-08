package state

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestProcessingClassesFromLimits(t *testing.T) {
	if got := ProcessingClassesFromLimits(nil); got != nil {
		t.Fatalf("nil limits: want nil, got %v", got)
	}
	if got := ProcessingClassesFromLimits(&ipcpb.NodeLimits{}); got != nil {
		t.Fatalf("no classes: want nil, got %v", got)
	}

	limits := &ipcpb.NodeLimits{
		ProcessingClasses: []*ipcpb.ProcessingClassCapacity{
			{Class: "video_transcode", SlotsTotal: 8, SlotsUsed: 3},
			{Class: "ai_inference", SlotsTotal: 2, SlotsUsed: 1, Ready: []string{"llama-3"}},
			{Class: "", SlotsTotal: 5}, // skipped: no class name
		},
	}
	got := ProcessingClassesFromLimits(limits)
	if len(got) != 2 {
		t.Fatalf("want 2 classes, got %d (%v)", len(got), got)
	}
	if c := got["video_transcode"]; c.Total != 8 || c.Used != 3 {
		t.Errorf("video_transcode: want {8,3}, got {%d,%d}", c.Total, c.Used)
	}
	if c := got["ai_inference"]; c.Total != 2 || c.Used != 1 || len(c.Ready) != 1 || c.Ready[0] != "llama-3" {
		t.Errorf("ai_inference: unexpected %+v", c)
	}
}

func TestCanRunClass(t *testing.T) {
	n := &NodeState{ProcessingClasses: map[string]ClassCapacity{
		"video_transcode": {Total: 2, Used: 2},   // at capacity
		"ai_inference":    {Total: 0, Used: 100}, // unbounded
		"cpu_heavy":       {Total: 4, Used: 1},   // free
	}}
	if n.CanRunClass("video_transcode") {
		t.Error("full class should not be runnable")
	}
	if !n.CanRunClass("ai_inference") {
		t.Error("unbounded class should be runnable")
	}
	if !n.CanRunClass("cpu_heavy") {
		t.Error("class with free slots should be runnable")
	}
	if n.CanRunClass("not_advertised") {
		t.Error("unadvertised class should not be runnable")
	}

	if used, ok := n.ClassLoad("cpu_heavy"); !ok || used != 1 {
		t.Errorf("ClassLoad(cpu_heavy): want (1,true), got (%d,%v)", used, ok)
	}
	if _, ok := n.ClassLoad("not_advertised"); ok {
		t.Error("ClassLoad of unadvertised class should report ok=false")
	}
}
