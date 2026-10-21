package control

import (
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type fanOutRecorder struct {
	vod, clip map[string][]string // node -> hashes
	fail      map[string]bool
}

func installFanOutSeams(t *testing.T, nodes *[]string) *fanOutRecorder {
	t.Helper()
	rec := &fanOutRecorder{vod: map[string][]string{}, clip: map[string][]string{}, fail: map[string]bool{}}
	prevNodes, prevVod, prevClip, prevLedger, prevNow := fanOutNodeIDs, fanOutSendVod, fanOutSendClip, fanOutDelivery, fanOutNow
	t.Cleanup(func() {
		fanOutNodeIDs, fanOutSendVod, fanOutSendClip, fanOutDelivery, fanOutNow = prevNodes, prevVod, prevClip, prevLedger, prevNow
	})
	fanOutNodeIDs = func() []string { return *nodes }
	fanOutSendVod = func(nodeID string, req *ipcpb.VodDeleteRequest) error {
		if rec.fail[nodeID] {
			return errors.New("not connected")
		}
		rec.vod[nodeID] = append(rec.vod[nodeID], req.GetVodHash())
		return nil
	}
	fanOutSendClip = func(nodeID string, req *ipcpb.ClipDeleteRequest) error {
		if rec.fail[nodeID] {
			return errors.New("not connected")
		}
		rec.clip[nodeID] = append(rec.clip[nodeID], req.GetClipHash())
		return nil
	}
	fanOutDelivery = &fanOutLedger{sent: map[string]*fanOutEntry{}}
	return rec
}

// A deleted VOD's delete reaches every node in the cluster, not only the
// catalog holders: an edge that read the VOD through its relay holds caches of
// it that no inventory reports.
func TestSendArtifactDeleteToClusterNodesReachesNodesWithoutCatalogCopies(t *testing.T) {
	nodes := []string{"edge-holder", "edge-relay", "edge-other"}
	rec := installFanOutSeams(t, &nodes)

	if failures := SendArtifactDeleteToClusterNodes("vod", "vodhash", []string{"edge-holder"}); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	for _, node := range []string{"edge-relay", "edge-other"} {
		if got := rec.vod[node]; len(got) != 1 || got[0] != "vodhash" {
			t.Errorf("%s got VOD deletes %v, want [vodhash]", node, got)
		}
	}
	if got := rec.vod["edge-holder"]; len(got) != 0 {
		t.Errorf("holder got the fan-out delete %v; its own delete already went", got)
	}

	SendArtifactDeleteToClusterNodes("clip", "cliphash", nil)
	SendArtifactDeleteToClusterNodes("chapter", "chapterhash", nil)
	if got := rec.clip["edge-relay"]; len(got) != 1 || got[0] != "cliphash" {
		t.Errorf("clip fan-out sent %v, want a clip delete of cliphash", got)
	}
	if got := rec.vod["edge-relay"]; len(got) != 2 || got[1] != "chapterhash" {
		t.Errorf("chapter fan-out sent %v, want a VOD delete of chapterhash", got)
	}
	if failures := SendArtifactDeleteToClusterNodes("dvr", "dvrhash", nil); failures != nil {
		t.Errorf("dvr fan-out = %v, want nothing sent", failures)
	}
}

// Each node gets an artifact's delete once; a node that was unreachable, or
// joined after the deletion, gets it on a later pass.
func TestSendArtifactDeleteToClusterNodesDeliversOncePerNode(t *testing.T) {
	nodes := []string{"edge-a", "edge-b"}
	rec := installFanOutSeams(t, &nodes)
	rec.fail["edge-b"] = true

	failures := SendArtifactDeleteToClusterNodes("vod", "vodhash", nil)
	if _, ok := failures["edge-b"]; !ok || len(failures) != 1 {
		t.Fatalf("failures = %v, want edge-b only", failures)
	}

	rec.fail["edge-b"] = false
	nodes = append(nodes, "edge-late")
	if failures := SendArtifactDeleteToClusterNodes("vod", "vodhash", nil); len(failures) != 0 {
		t.Fatalf("second pass failures = %v", failures)
	}
	SendArtifactDeleteToClusterNodes("vod", "vodhash", nil)

	for _, node := range nodes {
		if got := rec.vod[node]; len(got) != 1 {
			t.Errorf("%s got %d deletes (%v), want exactly 1", node, len(got), got)
		}
	}
}

// The delivery record of an artifact deleted longer ago than the fan-out
// window is dropped with the next delivery.
func TestFanOutLedgerDropsEntriesPastTheWindow(t *testing.T) {
	nodes := []string{"edge-a"}
	installFanOutSeams(t, &nodes)
	now := time.Now()
	fanOutNow = func() time.Time { return now }
	SendArtifactDeleteToClusterNodes("vod", "old", nil)

	now = now.Add(RelayCacheDeleteWindow + time.Minute)
	SendArtifactDeleteToClusterNodes("vod", "new", nil)
	if _, ok := fanOutDelivery.sent["old"]; ok {
		t.Fatal("expired delivery record kept")
	}
	if !fanOutDelivery.delivered("new", "edge-a") {
		t.Fatal("new delivery not recorded")
	}
}
