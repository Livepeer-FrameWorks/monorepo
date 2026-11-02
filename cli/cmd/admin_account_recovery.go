package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ingestCellRetirementClient interface {
	AdminRetireIngestCell(context.Context, *commodorepb.AdminRetireIngestCellRequest) (*commodorepb.AdminRetireIngestCellResponse, error)
	AdminUnretireIngestCell(context.Context, *commodorepb.AdminUnretireIngestCellRequest) (*commodorepb.AdminUnretireIngestCellResponse, error)
}

// newAdminClustersRetireCmd records a cell removed for good in Commodore so
// the session stops it can no longer receive settle.
func newAdminClustersRetireCmd() *cobra.Command {
	var reason string
	var yes bool
	cmd := &cobra.Command{
		Use:   "retire <cluster-id>",
		Short: "Record a cell removed for good so session stops owed to it settle",
		Long: `Record a cell removed for good so session stops owed to it settle.

Account closure and user deactivation stop active ingest on every cell that
recorded the account's streams. A cell that was removed for good can never
confirm the stop, which keeps the stop pending and CommodoreAccountIngestRevocationStuck
firing. Retiring the cell counts a stop that cannot reach it as delivered there.

Commodore refuses while Quartermaster cannot be read or any registered Foghorn
of the cell answers. A retired cell that comes back still receives its stops.
Retirement changes nothing else about the cluster. 'frameworks admin clusters
unretire' removes the record, for a cell retired by mistake.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cell := strings.TrimSpace(args[0])
			if cell == "" {
				return fmt.Errorf("cluster ID is required")
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "Retire cell %s; stops it cannot receive will count as delivered. Type the cluster ID to confirm: ", cell)
				typed, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil {
					return fmt.Errorf("read cluster ID confirmation: %w", err)
				}
				if strings.TrimSpace(typed) != cell {
					return fmt.Errorf("cluster ID confirmation does not match; nothing was retired")
				}
			}
			client, cfg, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer client.Close()
			return runRetireIngestCell(cmd.Context(), cmd.OutOrStdout(), client, cfg.Auth.JWT, cell, reason, output == "json")
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded with the retirement (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip typing the cluster ID to confirm")
	return cmd
}

func runRetireIngestCell(ctx context.Context, w io.Writer, client ingestCellRetirementClient, jwt, cell, reason string, jsonOutput bool) error {
	callCtx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	response, err := client.AdminRetireIngestCell(callCtx, &commodorepb.AdminRetireIngestCellRequest{ClusterId: cell, Reason: reason})
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, response)
	}
	if response.GetAlreadyRetired() {
		ux.Warn(w, fmt.Sprintf("Cell %s was already retired; the first record is kept", response.GetClusterId()))
	} else {
		ux.Success(w, fmt.Sprintf("Cell %s retired", response.GetClusterId()))
	}
	ux.Result(w, []ux.ResultField{
		{Key: "retired at", OK: true, Detail: formatAccountTime(response.GetRetiredAt())},
		{Key: "retired by", OK: true, Detail: response.GetRetiredBy()},
		{Key: "reason", OK: true, Detail: strconv.Quote(response.GetReason())},
		{Key: "pending session stops", OK: true, Detail: fmt.Sprintf("%d include this cell and retry now", response.GetPendingIngestStops())},
	})
	ux.PrintNextSteps(w, []ux.NextStep{{
		Cmd: "frameworks cluster doctor --deep",
		Why: "Confirm no session stop is still pending; a stop that also names another unreachable cell keeps waiting for it.",
	}})
	return nil
}

// newAdminClustersUnretireCmd removes a cell's retirement record, so stops the
// cell does not confirm stay pending again.
func newAdminClustersUnretireCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "unretire <cluster-id>",
		Short: "Remove a cell's retirement record so its session stops wait for it again",
		Long: `Remove a cell's retirement record, for a cell retired by mistake or one that
returns to service. From then on a session stop the cell does not confirm stays
pending. Stops that already settled while the cell was retired are not reopened.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cell := strings.TrimSpace(args[0])
			if cell == "" {
				return fmt.Errorf("cluster ID is required")
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			client, cfg, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer client.Close()
			return runUnretireIngestCell(cmd.Context(), cmd.OutOrStdout(), client, cfg.Auth.JWT, cell, reason, output == "json")
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason logged with the removal (required)")
	return cmd
}

func runUnretireIngestCell(ctx context.Context, w io.Writer, client ingestCellRetirementClient, jwt, cell, reason string, jsonOutput bool) error {
	callCtx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	response, err := client.AdminUnretireIngestCell(callCtx, &commodorepb.AdminUnretireIngestCellRequest{ClusterId: cell, Reason: reason})
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, response)
	}
	if !response.GetWasRetired() {
		ux.Warn(w, fmt.Sprintf("Cell %s was not retired; nothing changed", response.GetClusterId()))
		return nil
	}
	ux.Success(w, fmt.Sprintf("Retirement of cell %s removed", response.GetClusterId()))
	ux.Result(w, []ux.ResultField{
		{Key: "was retired at", OK: true, Detail: formatAccountTime(response.GetRetiredAt())},
		{Key: "retired by", OK: true, Detail: response.GetRetiredBy()},
		{Key: "retirement reason", OK: true, Detail: strconv.Quote(response.GetRetirementReason())},
	})
	return nil
}

// writeTenantAccountState renders closure synchronization and pending stops.
func writeTenantAccountState(w io.Writer, state *commodorepb.TenantAccountState) {
	switch state.GetClosureSync() {
	case "open", "":
		fmt.Fprintln(w, "Account: open")
	case "synchronized":
		fmt.Fprintf(w, "Account: closed %s by %s; Quartermaster synchronized %s\n", formatAccountTime(state.GetClosedAt()), state.GetClosedBy(), formatAccountTime(state.GetQuartermasterClosedAt()))
	case "retrying":
		fmt.Fprintf(w, "Account: closed %s by %s; Quartermaster synchronization failing (%d attempts, next after %s): %s\n", formatAccountTime(state.GetClosedAt()), state.GetClosedBy(), state.GetClosureFailures(), formatAccountTime(state.GetClosureRetryAfter()), strconv.Quote(state.GetClosureError()))
	default:
		fmt.Fprintf(w, "Account: closed %s by %s; Quartermaster synchronization pending\n", formatAccountTime(state.GetClosedAt()), state.GetClosedBy())
	}
	stops := state.GetPendingIngestStops()
	if len(stops) == 0 {
		fmt.Fprintln(w, "Pending session stops: none")
		return
	}
	if state.GetPendingIngestStopsTruncated() {
		fmt.Fprintf(w, "Pending session stops: %d+ (showing the oldest %d)\n", len(stops), len(stops))
	} else {
		fmt.Fprintf(w, "Pending session stops: %d\n", len(stops))
	}
	for _, stop := range stops {
		cells := strings.Join(stop.GetCells(), ",")
		if cells == "" {
			cells = "-"
		}
		line := fmt.Sprintf("  %s %s  %s  attempts=%d  since=%s  next=%s  cells=%s", stop.GetTargetKind(), stop.GetTargetId(), stop.GetStatus(), stop.GetAttempts(), formatAccountTime(stop.GetPendingSince()), formatAccountTime(stop.GetNextAttemptAt()), cells)
		if retired := stop.GetRetiredCells(); len(retired) > 0 {
			line += "  retired=" + strings.Join(retired, ",")
		}
		fmt.Fprintln(w, line)
		if stop.GetLastError() != "" {
			fmt.Fprintf(w, "    last error: %s\n", strconv.Quote(stop.GetLastError()))
		}
	}
	fmt.Fprintln(w, "A stop owed to a cell removed for good settles after 'frameworks admin clusters retire <cluster-id>'.")
}

func formatAccountTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

// writeTenantWithAccountStateJSON adds account_state (or account_state_error)
// beside the Quartermaster tenant fields, keeping their names unchanged.
func writeTenantWithAccountStateJSON(w io.Writer, tenant proto.Message, state *commodorepb.TenantAccountState, stateErr error) error {
	body, err := protoOutputJSON.Marshal(tenant)
	if err != nil {
		return err
	}
	fields := map[string]json.RawMessage{}
	if err = json.Unmarshal(body, &fields); err != nil {
		return err
	}
	if stateErr != nil {
		if fields["account_state_error"], err = json.Marshal(stateErr.Error()); err != nil {
			return err
		}
	} else if fields["account_state"], err = protoOutputJSON.Marshal(state); err != nil {
		return err
	}
	combined, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err = json.Indent(&out, combined, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = w.Write(out.Bytes())
	return err
}
