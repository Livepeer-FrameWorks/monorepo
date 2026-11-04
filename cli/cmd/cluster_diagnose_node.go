package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

type nodeDiagnosisInventory interface {
	GetNode(context.Context, string) (*quartermasterpb.NodeResponse, error)
	ListNodeFingerprints(context.Context, string, bool, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodeFingerprintsResponse, error)
}

type nodeBindingDiagnosis struct {
	ID                string `json:"id"`
	HasIdentityKey    bool   `json:"has_identity_key"`
	MachineDuplicates int32  `json:"machine_duplicates"`
	MACDuplicates     int32  `json:"mac_duplicates"`
}

type nodeInventoryDiagnosis struct {
	ID                  string                 `json:"id"`
	NodeID              string                 `json:"node_id"`
	ClusterID           string                 `json:"cluster_id"`
	Status              string                 `json:"status"`
	EnrollmentOrigin    string                 `json:"enrollment_origin"`
	LastHeartbeat       string                 `json:"last_heartbeat"`
	AppliedMeshRevision string                 `json:"applied_mesh_revision"`
	Bindings            []nodeBindingDiagnosis `json:"bindings"`
}

func newClusterDiagnoseNodeCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{Use: "node <node-id>", Short: "Inspect node enrollment, heartbeat, capabilities, and reported config", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := recordDiagnosticRead(cmd.Context(), "node", args[0], service); err != nil {
				return err
			}
			qm, cfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer qm.Close()
			resolver := controlplane.NewResolver(cfg)
			defer resolver.Close()
			src := &liveDVRDiagnosisSource{ctxCfg: cfg, resolver: resolver}
			manifest, err := resolver.Manifest(cmd.Context())
			if err != nil {
				return err
			}
			health := func(ctx context.Context, entry, nodeID string) (*controlpb.GetNodeHealthResponse, error) {
				fh, err := src.foghornClient(ctx, entry)
				if err != nil {
					return nil, err
				}
				defer fh.Close()
				h, _, err := fh.GetNodeHealth(fhclient.WithOperatorBearer(ctx, cfg.Auth.JWT), &controlpb.GetNodeHealthRequest{NodeId: nodeID})
				return h, err
			}
			return runNodeDiagnose(cmd.Context(), cmd.OutOrStdout(), qm, health, manifest, cfg.Auth.JWT, strings.TrimSpace(args[0]), service, output == "json")
		}}
	cmd.Flags().StringVar(&service, "service", "", "manifest Foghorn service entry to query")
	return cmd
}

func runNodeDiagnose(ctx context.Context, w io.Writer, qm nodeDiagnosisInventory, health func(context.Context, string, string) (*controlpb.GetNodeHealthResponse, error), manifest *inventory.Manifest, jwt, nodeID, service string, jsonOutput bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	ctx, cancel := adminRPCContextTimeout(ctx, jwt, dvrDiagnoseRPCTimeout)
	defer cancel()
	node, err := qm.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("resolve node: %w", err)
	}
	n := node.GetNode()
	summary := nodeInventoryDiagnosis{ID: n.GetId(), NodeID: n.GetNodeId(), ClusterID: n.GetClusterId(), Status: n.GetStatus(), EnrollmentOrigin: n.GetEnrollmentOrigin(), LastHeartbeat: formatOptionalTime(n.GetLastHeartbeat()), AppliedMeshRevision: n.GetAppliedMeshRevision(), Bindings: []nodeBindingDiagnosis{}}
	if err = collectPages(ctx, jwt, func(ctx context.Context, page *commonpb.CursorPaginationRequest) (*commonpb.CursorPaginationResponse, error) {
		out, pageErr := qm.ListNodeFingerprints(ctx, n.GetClusterId(), false, page)
		if pageErr != nil {
			return nil, pageErr
		}
		for _, f := range out.GetFingerprints() {
			if f.GetNodeId() == n.GetId() || f.GetNodeId() == n.GetNodeId() {
				summary.Bindings = append(summary.Bindings, nodeBindingDiagnosis{f.GetFingerprintId(), f.GetHasIdentityKey(), f.GetMachineDuplicateCount(), f.GetMacsDuplicateCount()})
			}
		}
		return out.GetPagination(), nil
	}); err != nil {
		return fmt.Errorf("read node bindings: %w", err)
	}
	entry, healthErr := foghornEntryForDVR(manifest, n.GetClusterId(), service)
	var h *controlpb.GetNodeHealthResponse
	if healthErr == nil {
		h, healthErr = health(ctx, entry, n.GetNodeId())
	}
	failure := ""
	if healthErr != nil {
		failure = healthErr.Error()
	}
	if jsonOutput {
		var body json.RawMessage
		if h != nil {
			body, err = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(h)
			if err != nil {
				return err
			}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Inventory         nodeInventoryDiagnosis `json:"inventory"`
			Service           string                 `json:"service"`
			Health            json.RawMessage        `json:"health,omitempty"`
			Error             string                 `json:"error,omitempty"`
			AuthorityVersions string                 `json:"authority_versions"`
		}{summary, entry, body, failure, "unavailable: no per-node report"}); err != nil {
			return err
		}
	} else {
		ux.Heading(w, "Node "+summary.NodeID)
		fmt.Fprintf(w, "  id=%s cluster=%s service=%s inventory_status=%s\n  enrollment=%s inventory_heartbeat=%s mesh_revision=%s\n", summary.ID, summary.ClusterID, foghornEntryLabel(entry), summary.Status, summary.EnrollmentOrigin, summary.LastHeartbeat, dashIfEmpty(summary.AppliedMeshRevision))
		fmt.Fprintf(w, "  Fingerprint bindings: %d\n", len(summary.Bindings))
		for _, f := range summary.Bindings {
			fmt.Fprintf(w, "    %s identity_key=%t machine_duplicates=%d mac_duplicates=%d\n", f.ID, f.HasIdentityKey, f.MachineDuplicates, f.MACDuplicates)
		}
		if h != nil {
			fmt.Fprintf(w, "  healthy=%t mode=%s heartbeat=%s capabilities=%s active_streams=%d active_viewers=%d\n", h.GetIsHealthy(), h.GetOperationalMode(), h.GetLastHeartbeat(), strings.Join(h.GetCapabilities(), ","), h.GetActiveStreams(), h.GetActiveViewers())
			fmt.Fprintf(w, "  reported_config_cli=%s config_digest=%s render_digest=%s\n", dashIfEmpty(h.GetProvisionedConfigCliVersion()), dashIfEmpty(h.GetProvisionedConfigDigest()), dashIfEmpty(h.GetProvisionedConfigRenderDigest()))
			for _, c := range h.GetComponentVersions() {
				fmt.Fprintf(w, "  component=%s version=%s\n", c.GetComponent(), c.GetVersion())
			}
		}
		if failure != "" {
			fmt.Fprintf(w, "  runtime unavailable: %s\n", failure)
		}
		fmt.Fprintln(w, "  Per-node authority versions: unavailable (no existing report).")
	}
	if healthErr != nil {
		return fmt.Errorf("node diagnosis incomplete: %w", healthErr)
	}
	return nil
}
