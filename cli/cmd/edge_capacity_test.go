package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/clients/bridge"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdgeCapacityCommandIsRegistered(t *testing.T) {
	root := newEdgeCmd()
	for _, name := range []string{"show", "set"} {
		command, _, err := root.Find([]string{"capacity", name, "edge"})
		if err != nil || command.Name() != name {
			t.Errorf("edge capacity %s unavailable: command=%s err=%v", name, command.Name(), err)
		}
	}
}

func TestManifestCapacityRunsAfterControlConnectEvenWithoutHostDrift(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			connected := false
			writes := 0
			ops := &capacityRolloutOps{fakeRolloutOps: &fakeRolloutOps{name: "edge", log: &rolloutLog{}}, connected: &connected}
			node := edgeRolloutNode{Name: "edge", Live: live, Ops: ops, CommitCapacity: func(context.Context) error {
				if !connected {
					t.Error("budget written before control connected")
				}
				writes++
				return nil
			}}
			result := runEdgeRollout(t.Context(), io.Discard, []edgeRolloutNode{node}, 1, false)
			if result[0].Err != nil || writes != 1 {
				t.Fatalf("manifest budget not written on every provision: writes=%d err=%v", writes, result[0].Err)
			}
		})
	}
}

type capacityRolloutOps struct {
	*fakeRolloutOps
	connected *bool
}

func (ops *capacityRolloutOps) AwaitControlConnected(context.Context) error {
	*ops.connected = true
	return ops.controlAfterApply
}

func TestCapacitySetPreservesOtherDirectionAndUsesCAS(t *testing.T) {
	writes := 0
	backend := edgeCapacityBackend{
		read: func(context.Context, string) (*edgeCapacitySnapshot, error) {
			return &edgeCapacitySnapshot{EgressBudget: 125000000, IngressBudget: 250000000, EgressSource: "DEFAULT", IngressSource: "OPERATOR", Revision: 8}, nil
		},
		write: func(_ context.Context, node string, egress, ingress *uint64, revision uint64) (*edgeCapacitySnapshot, error) {
			writes++
			if node != "edge" || egress != nil || ingress == nil || *ingress != 250000000 || revision != 8 {
				t.Errorf("direction/reset/CAS lost: node=%s egress=%v ingress=%v revision=%d", node, egress, ingress, revision)
			}
			return &edgeCapacitySnapshot{}, nil
		},
	}
	if _, err := setEdgeCapacity(t.Context(), backend, "edge", "default", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := setEdgeCapacity(t.Context(), backend, "edge", "-1", "", nil); err == nil {
		t.Fatal("invalid budget accepted")
	}
	if writes != 1 {
		t.Fatalf("invalid input caused a write: %d", writes)
	}
}

type manifestCapacityFake struct {
	writes []*quartermasterpb.SetNodeCapacityRequest
	pages  int
}

func (f *manifestCapacityFake) ListNodes(_ context.Context, cluster, nodeType, region string, page *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	f.pages++
	if cluster != "cell" || nodeType != "edge" {
		return nil, fmt.Errorf("wrong scope")
	}
	if page.GetAfter() == "" {
		return &quartermasterpb.ListNodesResponse{Nodes: []*quartermasterpb.InfrastructureNode{{NodeId: "other"}}, Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: proto.String("next")}}, nil
	}
	return &quartermasterpb.ListNodesResponse{Nodes: []*quartermasterpb.InfrastructureNode{{NodeId: "canonical", NodeName: "manifest-name"}}}, nil
}
func (f *manifestCapacityFake) GetNodeCapacity(context.Context, string) (*quartermasterpb.NodeCapacity, error) {
	return &quartermasterpb.NodeCapacity{Revision: 7}, nil
}
func (f *manifestCapacityFake) SetNodeCapacity(_ context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	f.writes = append(f.writes, req)
	return &quartermasterpb.NodeCapacity{}, nil
}

func TestManifestCapacityResolvesAllPagesAndResetsZero(t *testing.T) {
	fake := &manifestCapacityFake{}
	for _, mbps := range []int{2000, 0} {
		node, err := writeProvisionedCapacity(t.Context(), fake, "cell", "manifest-name", mbps)
		if err != nil || node != "canonical" {
			t.Fatalf("canonical node not resolved: %q %v", node, err)
		}
		req := fake.writes[len(fake.writes)-1]
		if req.GetNodeId() != "canonical" || req.GetExpectedRevision() != 7 {
			t.Fatalf("wrong request: %v", req)
		}
		if mbps == 0 {
			if req.EgressBudgetBytesPerSec != nil || req.IngressBudgetBytesPerSec != nil {
				t.Fatal("zero must reset both defaults")
			}
		} else if req.GetEgressBudgetBytesPerSec() != 250000000 || req.GetIngressBudgetBytesPerSec() != 250000000 {
			t.Fatal("manifest Mbps conversion wrong")
		}
	}
	if fake.pages != 4 {
		t.Fatalf("pagination calls=%d", fake.pages)
	}
}

func TestTokenOnlyDeployRefusesCapacityBeforeProvisioning(t *testing.T) {
	for _, value := range []string{"2000", "0"} {
		cmd := newEdgeDeployCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.ParseFlags([]string{"--enrollment-token", "test-token", "--bandwidth-mbps", value}); err != nil {
			t.Fatal(err)
		}
		err := cmd.RunE(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "frameworks edge capacity set") {
			t.Fatalf("token-only flag not refused with a follow-up: %v", err)
		}
		if value == "0" && !strings.Contains(err.Error(), "--egress-mbps default") {
			t.Fatalf("default follow-up is not executable: %v", err)
		}
	}
}

func TestDeployRejectsInvalidBudgetBeforeResolvingCredentials(t *testing.T) {
	previous := fwcfg.GetRuntimeOverrides()
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"), ConfigPathExplicit: true})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(previous) })
	cmd := newEdgeDeployCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ParseFlags([]string{"--bandwidth-mbps", "-1"}); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bandwidth") {
		t.Fatalf("bad budget reached credential/deploy resolution: %v", err)
	}
}

func TestLoggedInDeployWritesCapacityAfterRegistration(t *testing.T) {
	reads, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string                     `json:"query"`
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(body.Query, "query GetNodeCapacity") {
			reads++
			_, _ = w.Write([]byte(`{"data":{"node":{"capacity":{"egressBudgetBytesPerSec":125000000,"ingressBudgetBytesPerSec":125000000,"egressSource":"DEFAULT","ingressSource":"DEFAULT","revision":2}}}}`))
		} else {
			writes++
			var input map[string]any
			if err := json.Unmarshal(body.Variables["input"], &input); err != nil {
				t.Error(err)
			}
			if input["nodeId"] != "canonical" || input["egressBudgetBytesPerSec"] != float64(250000000) || input["ingressBudgetBytesPerSec"] != float64(250000000) || input["expectedRevision"] != float64(2) {
				t.Errorf("deploy budget request=%v", input)
			}
			_, _ = w.Write([]byte(`{"data":{"setNodeCapacity":{"__typename":"InfrastructureNode","capacity":{"egressBudgetBytesPerSec":250000000,"ingressBudgetBytesPerSec":250000000,"revision":3}}}}`))
		}
	}))
	defer server.Close()
	if err := writeDeployedBridgeCapacity(t.Context(), bridge.NewWithRefresh(server.URL, nil), "jwt", "canonical", 2000, time.Second); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || writes != 1 {
		t.Fatalf("deploy read/write=%d/%d", reads, writes)
	}
}

func TestDeployCapacityTimeoutDoesNotWriteAnUnregisteredNode(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(body.Query, "mutation") {
			writes++
		}
		_, _ = w.Write([]byte(`{"data":{"node":null}}`))
	}))
	defer server.Close()
	err := writeDeployedBridgeCapacity(t.Context(), bridge.NewWithRefresh(server.URL, nil), "jwt", "edge", 2000, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || writes != 0 {
		t.Fatalf("unregistered node write: err=%v writes=%d", err, writes)
	}
}

func TestClusterDoctorReportsDefaultsAndUnappliedBudgets(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{{NodeId: "edge", Capacity: &quartermasterpb.NodeCapacity{Revision: 3, EgressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT, IngressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR}}}
	result := evaluateEdgeCapacities(nodes, map[string]*foghorncontrolpb.GetNodeHealthResponse{"edge": {CapacityRevisionApplied: proto.Uint64(2)}})
	if result.OK || result.Status != yugabyteLayoutWarning || !strings.Contains(result.Message, "default 1 Gbit/s") || !strings.Contains(result.Message, "not applied") {
		t.Fatalf("doctor hid capacity warnings: %+v", result)
	}
}
