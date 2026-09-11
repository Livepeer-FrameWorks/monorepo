package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func sourceConnectionTrigger() *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{TriggerType: "CONN_PLAY", Blocking: true, NodeId: "source-node", ClusterId: proto.String("source-cluster"),
		TriggerPayload: &ipcpb.MistTrigger_ConnectionPlay{ConnectionPlay: &ipcpb.ConnectionPlayTrigger{StreamName: "live+stream", Host: "::ffff:192.0.2.1", Connector: "DTSC", RequestUrl: "dtsc://source/live+stream"}}}
}

func TestSourceConnectionAdmission(t *testing.T) {
	p := &Processor{}
	if _, abort, err := p.ProcessTypedTrigger(sourceConnectionTrigger()); !abort || err == nil {
		t.Fatal("absent source gate allowed a pull")
	}
	p.SetSourceConnectionAdmission(func(ctx context.Context, in SourceConnection) (time.Time, error) {
		if in.SourceNodeID != "source-node" || in.SourceClusterID != "source-cluster" || in.RuntimeName != "live+stream" || in.RemoteAddress != "192.0.2.1" || in.RequestURL != "dtsc://source/live+stream" {
			t.Fatalf("wrong connection facts: %+v", in)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("unbounded source gate")
		}
		return time.Now().Add(time.Second), nil
	})
	if response, abort, err := p.ProcessTypedTrigger(sourceConnectionTrigger()); err != nil || abort || response != "true" {
		t.Fatalf("valid source decision failed: %q %v %v", response, abort, err)
	}
	for _, outcome := range []struct {
		expiry time.Time
		err    error
	}{{time.Time{}, nil}, {time.Now().Add(-time.Second), nil}, {time.Now().Add(time.Second), errors.New("sensitive grant")}} {
		p.SetSourceConnectionAdmission(func(context.Context, SourceConnection) (time.Time, error) { return outcome.expiry, outcome.err })
		if _, abort, err := p.ProcessTypedTrigger(sourceConnectionTrigger()); !abort || err == nil || err.Error() == "sensitive grant" {
			t.Fatal("invalid source decision was accepted or disclosed")
		}
	}
	p.SetSourceConnectionAdmission(nil)
	if _, abort, err := p.ProcessTypedTrigger(sourceConnectionTrigger()); !abort || err == nil {
		t.Fatal("clearing source gate restored admission")
	}
}

func TestSourceConnectionRejectsUnboundFacts(t *testing.T) {
	p := &Processor{}
	p.SetSourceConnectionAdmission(func(context.Context, SourceConnection) (time.Time, error) {
		t.Fatal("invalid connection reached policy")
		return time.Time{}, nil
	})
	for _, mutate := range []func(*ipcpb.MistTrigger){
		func(t *ipcpb.MistTrigger) { t.NodeId = "" },
		func(t *ipcpb.MistTrigger) { t.ClusterId = nil },
		func(t *ipcpb.MistTrigger) { t.TriggerType = "USER_NEW" },
		func(t *ipcpb.MistTrigger) { t.Blocking = false },
		func(t *ipcpb.MistTrigger) { t.GetConnectionPlay().Host = "unverified-host" },
		func(t *ipcpb.MistTrigger) { t.GetConnectionPlay().Host = "0.0.0.0" },
		func(t *ipcpb.MistTrigger) { t.GetConnectionPlay().RequestUrl = "dtsc://source/live+other" },
		func(t *ipcpb.MistTrigger) { t.GetConnectionPlay().RequestUrl = "dtsc://secret@source/live+stream" },
		func(t *ipcpb.MistTrigger) { t.GetConnectionPlay().RequestUrl = "https://source/live+stream" },
	} {
		trigger := sourceConnectionTrigger()
		mutate(trigger)
		if _, abort, err := p.ProcessTypedTrigger(trigger); !abort || err == nil {
			t.Fatal("invalid connection admitted")
		}
	}
	viewer := sourceConnectionTrigger()
	viewer.GetConnectionPlay().Connector = "HLS"
	if response, abort, err := p.ProcessTypedTrigger(viewer); abort || err != nil || response != "true" {
		t.Fatal("source gate acquired ordinary viewer admission")
	}
}
