package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"github.com/spf13/cobra"
)

// newDeployTestCmd wires a cobra command + capture buffer so renderEdgeDeployResult
// can be tested end-to-end through its real ux.Result output path.
func newDeployTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
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
		clusterSlug:   "my-vpc",
		provisioned:   true,
	})

	out := buf.String()
	for _, want := range []string{"Result:", "vpc", "created via Bridge", "enrollment", "issued", "stack", "https", "edge-1", "edge.example.com"} {
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
		t.Errorf("mode B should show N/A for vpc field, got:\n%s", out)
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
