package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"frameworks/cli/pkg/clients/bridge"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/provisioner"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/spf13/cobra"
)

func newDeployTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

func TestRenderEdgeDeployResultRetainsCreatedClusterAfterPreregisterFailure(t *testing.T) {
	cmd, buf := newDeployTestCmd()
	renderEdgeDeployResult(cmd, edgeDeployResultFields{
		modeA: true, bridgeCreated: true, failed: errors.New("public identity pending"),
	})
	if strings.Contains(buf.String(), "not created") || !strings.Contains(buf.String(), "created via Bridge") {
		t.Fatalf("committed cluster disappeared from partial result: %s", buf.String())
	}
}

func TestRenderEdgeDeployResult_modeASuccess(t *testing.T) {
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{}) })

	cmd, buf := newDeployTestCmd()
	renderEdgeDeployResult(cmd, edgeDeployResultFields{
		modeA:         true,
		bridgeCreated: true,
		nodeID:        "edge-1",
		domain:        "edge.example.com",
		clusterSlug:   "my-edge-cluster",
		provisioned:   true,
	})

	out := buf.String()
	for _, want := range []string{"Result:", "cluster", "created via Bridge", "enrollment", "issued", "stack", "https", "edge-1", "edge.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderEdgeDeployResult_modeBUsesExistingCluster(t *testing.T) {
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{}) })

	cmd, buf := newDeployTestCmd()
	renderEdgeDeployResult(cmd, edgeDeployResultFields{
		modeA:         false,
		bridgeCreated: false,
		nodeID:        "edge-2",
		domain:        "stream.example.com",
		clusterSlug:   "existing",
		provisioned:   true,
	})

	out := buf.String()
	if !strings.Contains(out, "N/A (token mode)") {
		t.Errorf("mode B should show N/A for cluster field, got:\n%s", out)
	}
}

func TestRenderEdgeDeployResult_partialFailureShowsProvisioningDidNotComplete(t *testing.T) {
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{}) })

	cmd, buf := newDeployTestCmd()
	renderEdgeDeployResult(cmd, edgeDeployResultFields{
		modeA:         true,
		bridgeCreated: true,
		nodeID:        "edge-3",
		domain:        "edge3.example.com",
		clusterSlug:   "partial",
		provisioned:   false,
		failed:        errors.New("ssh timeout"),
	})

	out := buf.String()
	if !strings.Contains(out, "provisioning did not complete") {
		t.Errorf("partial failure should note incomplete provisioning, got:\n%s", out)
	}
	if !strings.Contains(out, "not verified") {
		t.Errorf("partial failure should mark https as not verified, got:\n%s", out)
	}
}

func TestRenderEdgeDeployResult_noOutputInJSONMode(t *testing.T) {
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{OutputJSON: true})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{}) })

	cmd, buf := newDeployTestCmd()
	renderEdgeDeployResult(cmd, edgeDeployResultFields{
		modeA:       true,
		nodeID:      "edge-x",
		domain:      "x.example.com",
		provisioned: true,
	})

	if buf.Len() != 0 {
		t.Errorf("JSON mode should produce no ux output, got:\n%s", buf.String())
	}
}

func TestApplyEdgeDeployTelemetryConfigCopiesPreRegistrationTelemetry(t *testing.T) {
	cfg := &provisioner.EdgeProvisionConfig{}
	resp := &foghornpb.PreRegisterEdgeResponse{
		Telemetry: &commonpb.EdgeTelemetryConfig{
			Enabled:     true,
			WriteUrl:    "https://telemetry.example.com/api/v1/write",
			BearerToken: "token",
		},
	}

	if err := applyEdgeDeployTelemetryConfig(cfg, resp); err != nil {
		t.Fatalf("applyEdgeDeployTelemetryConfig returned error: %v", err)
	}
	if cfg.TelemetryURL != "https://telemetry.example.com/api/v1/write" {
		t.Fatalf("TelemetryURL = %q", cfg.TelemetryURL)
	}
	if cfg.TelemetryToken != "token" {
		t.Fatalf("TelemetryToken = %q", cfg.TelemetryToken)
	}
}

func TestApplyEdgeDeployTelemetryConfigRequiresTokenForTelemetryURL(t *testing.T) {
	cfg := &provisioner.EdgeProvisionConfig{}
	resp := &foghornpb.PreRegisterEdgeResponse{
		Telemetry: &commonpb.EdgeTelemetryConfig{
			Enabled:  true,
			WriteUrl: "https://telemetry.example.com/api/v1/write",
		},
	}

	if err := applyEdgeDeployTelemetryConfig(cfg, resp); err == nil {
		t.Fatal("expected missing telemetry token to fail")
	}
}

func TestEnrollmentResumeUsesCommittedClusterInsteadOfCreatingAnother(t *testing.T) {
	creates, tokens := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(body.Query, "mutation CreateEdgeCluster") {
			creates++
			_, _ = w.Write([]byte(`{"data":{"createEdgeCluster":{"__typename":"CreateEdgeClusterResponse","cluster":{"id":"cluster-1","clusterId":"committed-private","clusterName":"Test"},"bootstrapToken":{"id":"token-1","token":"fake-once"}}}}`))
		} else {
			tokens++
			encoded, _ := json.Marshal(body.Variables)
			if !strings.Contains(string(encoded), "committed-private") {
				t.Error("resume did not bind to the committed ID")
			}
			_, _ = w.Write([]byte(`{"data":{"createEnrollmentToken":{"__typename":"CreateEnrollmentTokenResponse","bootstrapToken":{"token":"fake-resume","id":"token-2"}}}}`))
		}
	}))
	defer server.Close()
	cfg := &deployConfig{clusterName: "Test"}
	cmd, _ := newDeployTestCmd()
	client := bridge.New(server.URL)
	token, created, err := resolveEnrollmentToken(context.Background(), cmd, client, "fake-session", cfg)
	if err != nil || !created || token != "fake-once" || cfg.clusterID != "committed-private" {
		t.Fatalf("committed identity lost: created=%v id=%s err=%v", created, cfg.clusterID, err)
	}
	if _, created, err := resolveEnrollmentToken(context.Background(), cmd, client, "fake-session", cfg); err != nil || created {
		t.Fatalf("resume recreated cluster: created=%v err=%v", created, err)
	}
	if creates != 1 || tokens != 1 {
		t.Fatalf("duplicate side effects: creates=%d tokens=%d", creates, tokens)
	}
}

func TestDeployReceiptAtomicallyRetainsPrivateNonsecretProgress(t *testing.T) {
	dir := t.TempDir()
	receipt := edgeDeployReceipt{ClusterID: "private-cluster", BridgeURL: "https://bridge.example", SSHTarget: "kansas", Phase: "enrollment_issued", ClusterCreated: true, EnrollmentTokenIssued: true}
	path, err := writeEdgeDeployReceipt(dir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode=%o", info.Mode().Perm())
	}
	receipt.Phase = "pre_registered"
	receipt.NodeID = "owned-node"
	newPath, err := writeEdgeDeployReceipt(dir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if newPath != path {
		t.Fatal("receipt lost its stable identity")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["phase"] != "pre_registered" || decoded["node_id"] != "owned-node" {
		t.Fatal("partial JSON/progress lost after replacement")
	}
	for key := range decoded {
		if strings.Contains(key, "token") && key != "enrollment_token_issued" {
			t.Fatalf("credential field persisted: %s", key)
		}
	}
}
