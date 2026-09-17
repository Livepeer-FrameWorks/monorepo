package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
)

// adminControlCellClient is the Quartermaster surface of the control-cell
// reassignment commands.
type adminControlCellClient interface {
	ReassignClusterControlCell(ctx context.Context, req *quartermasterpb.ReassignClusterControlCellRequest) (*quartermasterpb.ClusterControlCellReassignment, error)
	GetClusterControlCellReassignment(ctx context.Context, clusterID string) (*quartermasterpb.ClusterControlCellReassignment, error)
}

func newAdminClustersReassignControlCellCmd() *cobra.Command {
	var target string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reassign-control-cell <cluster-id>",
		Short: "Move a tenant-private cluster to another Foghorn control cell",
		Long: `Move a tenant-private cluster to another platform control cell.

The previous cell releases the cluster's edges and they reconnect to the new
cell. The reassignment completes when no live edge is still observed by another
cell and fails if edges have not moved by the timeout. Reassigning back to the
previous cell is the rollback. Follow progress with "control-cell <cluster-id>".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			qm, ctxCfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			return runClusterReassignControlCell(cmd.Context(), cmd.OutOrStdout(), qm, ctxCfg.Auth.JWT, args[0], target, timeout, output == "json")
		},
	}
	cmd.Flags().StringVar(&target, "to", "", "Target platform control cell ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Time edges have to move before the reassignment fails")
	return cmd
}

func newAdminClustersControlCellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "control-cell <cluster-id>",
		Short: "Show a tenant-private cluster's control cell and reassignment progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			qm, ctxCfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			return runClusterControlCellStatus(cmd.Context(), cmd.OutOrStdout(), qm, ctxCfg.Auth.JWT, args[0], output == "json")
		},
	}
}

func runClusterReassignControlCell(ctx context.Context, w io.Writer, qm adminControlCellClient, jwt, clusterID, target string, timeout time.Duration, outputJSON bool) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("--to is required")
	}
	if timeout < time.Minute {
		return fmt.Errorf("--timeout must be at least 1m")
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	resp, err := qm.ReassignClusterControlCell(cctx, &quartermasterpb.ReassignClusterControlCellRequest{
		ClusterId:           strings.TrimSpace(clusterID),
		TargetControlCellId: target,
		TimeoutSeconds:      int64(timeout / time.Second),
	})
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	ux.Success(w, fmt.Sprintf("Cluster %s is moving from control cell %s to %s", resp.GetClusterId(), resp.GetPreviousControlCellId(), resp.GetControlCellId()))
	printControlCellReassignment(w, resp)
	return nil
}

func runClusterControlCellStatus(ctx context.Context, w io.Writer, qm adminControlCellClient, jwt, clusterID string, outputJSON bool) error {
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	resp, err := qm.GetClusterControlCellReassignment(cctx, strings.TrimSpace(clusterID))
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	ux.Heading(w, fmt.Sprintf("Cluster %s", resp.GetClusterId()))
	printControlCellReassignment(w, resp)
	return nil
}

func printControlCellReassignment(w io.Writer, r *quartermasterpb.ClusterControlCellReassignment) {
	state := r.GetState()
	if state == "" {
		state = "idle"
	}
	_, _ = fmt.Fprintf(w, "  control cell:  %s\n", r.GetControlCellId())
	if previous := r.GetPreviousControlCellId(); previous != "" {
		_, _ = fmt.Fprintf(w, "  previous cell: %s\n", previous)
	}
	_, _ = fmt.Fprintf(w, "  state:         %s\n", state)
	if r.GetState() != "" && r.GetDeadlineAt() != nil {
		_, _ = fmt.Fprintf(w, "  deadline:      %s\n", r.GetDeadlineAt().AsTime().UTC().Format(time.RFC3339))
	}
	if reason := r.GetError(); reason != "" {
		_, _ = fmt.Fprintf(w, "  error:         %s\n", reason)
	}
	if pending := r.GetPendingNodeIds(); len(pending) > 0 {
		_, _ = fmt.Fprintf(w, "  pending edges: %s\n", strings.Join(pending, ", "))
	}
}
