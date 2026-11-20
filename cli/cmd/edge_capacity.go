package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/pkg/clients/bridge"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
)

type edgeCapacitySnapshot struct {
	NodeID          string  `json:"node_id"`
	EgressBudget    uint64  `json:"egress_budget_bytes_per_sec"`
	IngressBudget   uint64  `json:"ingress_budget_bytes_per_sec"`
	EgressSource    string  `json:"egress_source"`
	IngressSource   string  `json:"ingress_source"`
	Revision        uint64  `json:"revision"`
	AppliedRevision *uint64 `json:"applied_revision"`
	DeliveryError   string  `json:"delivery_error,omitempty"`
}

type edgeCapacityBackend struct {
	read  func(context.Context, string) (*edgeCapacitySnapshot, error)
	write func(context.Context, string, *uint64, *uint64, uint64) (*edgeCapacitySnapshot, error)
	close func()
}

func newEdgeCapacityCmd() *cobra.Command {
	command := &cobra.Command{Use: "capacity", Short: "Read or set operator-owned node bandwidth budgets through the API"}
	show := &cobra.Command{Use: "show NODE", Short: "Show budgets, defaults and the applied revision", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		backend, err := openEdgeCapacityBackend(cmd.Context())
		if err != nil {
			return err
		}
		defer backend.close()
		result, err := backend.read(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return renderEdgeCapacity(cmd.OutOrStdout(), result, fwcfg.GetRuntimeOverrides().OutputJSON)
	}}
	var egress, ingress string
	var expected uint64
	set := &cobra.Command{Use: "set NODE", Short: "Set directional Mbit/s budgets; use 'default' to clear an override", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("egress-mbps") && !cmd.Flags().Changed("ingress-mbps") {
			return fmt.Errorf("provide --egress-mbps or --ingress-mbps; use default to restore 1 Gbit/s")
		}
		backend, err := openEdgeCapacityBackend(cmd.Context())
		if err != nil {
			return err
		}
		defer backend.close()
		var revision *uint64
		if cmd.Flags().Changed("expected-revision") {
			revision = &expected
		}
		result, err := setEdgeCapacity(cmd.Context(), backend, args[0], egress, ingress, revision)
		if err != nil {
			return err
		}
		return renderEdgeCapacity(cmd.OutOrStdout(), result, fwcfg.GetRuntimeOverrides().OutputJSON)
	}}
	set.Flags().StringVar(&egress, "egress-mbps", "", "Egress budget in decimal Mbit/s, or default; omitted keeps the current override")
	set.Flags().StringVar(&ingress, "ingress-mbps", "", "Ingress budget in decimal Mbit/s, or default; omitted keeps the current override")
	set.Flags().Uint64Var(&expected, "expected-revision", 0, "Require this revision instead of the one just read")
	command.AddCommand(show, set)
	return command
}

func parseCapacityMbps(value string) (*uint64, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") {
		return nil, nil
	}
	mbps, err := strconv.ParseFloat(value, 64)
	bytes := mbps * 125000
	if err != nil || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes < 1 || bytes > 9007199254740991 || math.Trunc(bytes) != bytes {
		return nil, fmt.Errorf("capacity must be positive Mbit/s convertible to exact whole bytes/s, or default")
	}
	result := uint64(bytes)
	return &result, nil
}

func setEdgeCapacity(ctx context.Context, backend edgeCapacityBackend, nodeID, egress, ingress string, revision *uint64) (*edgeCapacitySnapshot, error) {
	// Parse before opening a request so bad input cannot cause a partial change.
	var egressOverride, ingressOverride *uint64
	var err error
	if egress != "" {
		egressOverride, err = parseCapacityMbps(egress)
		if err != nil {
			return nil, fmt.Errorf("egress: %w", err)
		}
	}
	if ingress != "" {
		ingressOverride, err = parseCapacityMbps(ingress)
		if err != nil {
			return nil, fmt.Errorf("ingress: %w", err)
		}
	}
	current, err := backend.read(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if egress == "" && current.EgressSource == "OPERATOR" {
		value := current.EgressBudget
		egressOverride = &value
	}
	if ingress == "" && current.IngressSource == "OPERATOR" {
		value := current.IngressBudget
		ingressOverride = &value
	}
	expected := current.Revision
	if revision != nil {
		expected = *revision
	}
	return backend.write(ctx, nodeID, egressOverride, ingressOverride, expected)
}

func capacitySnapshotFromRecord(nodeID string, record *quartermasterpb.NodeCapacity) *edgeCapacitySnapshot {
	return &edgeCapacitySnapshot{NodeID: nodeID, EgressBudget: record.GetEffectiveEgressBytesPerSec(), IngressBudget: record.GetEffectiveIngressBytesPerSec(), EgressSource: strings.TrimPrefix(record.GetEgressSource().String(), "CAPACITY_SOURCE_"), IngressSource: strings.TrimPrefix(record.GetIngressSource().String(), "CAPACITY_SOURCE_"), Revision: record.GetRevision()}
}
func capacitySnapshotFromBridge(nodeID string, record *bridge.NodeCapacity) *edgeCapacitySnapshot {
	return &edgeCapacitySnapshot{NodeID: nodeID, EgressBudget: uint64(record.EgressBudgetBytesPerSec), IngressBudget: uint64(record.IngressBudgetBytesPerSec), EgressSource: record.EgressSource, IngressSource: record.IngressSource, Revision: record.Revision, AppliedRevision: record.AppliedRevision}
}

func openEdgeCapacityBackend(ctx context.Context) (edgeCapacityBackend, error) {
	cfg, contextErr := loadActiveContextLax()
	if contextErr != nil {
		return edgeCapacityBackend{}, contextErr
	}
	if cfg.Persona == fwcfg.PersonaPlatform {
		qm, authenticated, cleanup, clientErr := qmGRPCClientFromContext(ctx)
		if clientErr != nil {
			return edgeCapacityBackend{}, clientErr
		}
		resolver := controlplane.NewResolver(authenticated)
		closeBackend := func() { _ = qm.Close(); resolver.Close(); cleanup() }
		enrich := func(ctx context.Context, nodeID string, record *quartermasterpb.NodeCapacity, push bool) *edgeCapacitySnapshot {
			result := capacitySnapshotFromRecord(nodeID, record)
			owner, err := qm.GetNodeOwner(ctx, nodeID)
			if err != nil {
				return result
			}
			fh, err := clusterNodesFoghornClient(ctx, resolver, authenticated, owner.GetClusterId())
			if err != nil {
				return result
			}
			defer func() { _ = fh.Close() }()
			if push {
				if _, pushErr := fh.PushNodeCapacity(ctx, nodeID); pushErr != nil {
					result.DeliveryError = pushErr.Error()
				}
			}
			health, _, err := fh.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: nodeID})
			if err == nil {
				result.AppliedRevision = health.CapacityRevisionApplied
			}
			return result
		}
		return edgeCapacityBackend{
			close: closeBackend,
			read: func(ctx context.Context, nodeID string) (*edgeCapacitySnapshot, error) {
				record, err := qm.GetNodeCapacity(ctx, nodeID)
				if err != nil {
					return nil, err
				}
				return enrich(ctx, nodeID, record, false), nil
			},
			write: func(ctx context.Context, nodeID string, egress, ingress *uint64, revision uint64) (*edgeCapacitySnapshot, error) {
				record, err := qm.SetNodeCapacity(ctx, &quartermasterpb.SetNodeCapacityRequest{NodeId: nodeID, EgressBudgetBytesPerSec: egress, IngressBudgetBytesPerSec: ingress, ExpectedRevision: revision, Actor: &commonpb.RequestActor{AuthType: "service", UserId: "frameworks-cli"}})
				if err != nil {
					return nil, err
				}
				return enrich(ctx, nodeID, record, true), nil
			},
		}, nil
	}
	if strings.TrimSpace(cfg.Endpoints.BridgeURL) == "" {
		return edgeCapacityBackend{}, fmt.Errorf("node capacity requires a Bridge URL; run frameworks setup")
	}
	jwt, err := fwcredentials.Resolve(fwcredentials.AccountUserSession)
	if err != nil {
		return edgeCapacityBackend{}, err
	}
	if jwt == "" {
		return edgeCapacityBackend{}, fmt.Errorf("node capacity requires login; run frameworks login")
	}
	client := bridge.New(cfg.Endpoints.BridgeURL)
	return edgeCapacityBackend{close: func() {}, read: func(ctx context.Context, nodeID string) (*edgeCapacitySnapshot, error) {
		record, err := client.GetNodeCapacity(ctx, jwt, nodeID)
		if err != nil {
			return nil, err
		}
		return capacitySnapshotFromBridge(nodeID, record), nil
	}, write: func(ctx context.Context, nodeID string, egress, ingress *uint64, revision uint64) (*edgeCapacitySnapshot, error) {
		record, err := client.SetNodeCapacity(ctx, jwt, nodeID, egress, ingress, revision)
		if err != nil {
			return nil, err
		}
		return capacitySnapshotFromBridge(nodeID, record), nil
	}}, nil
}

func renderEdgeCapacity(w io.Writer, snapshot *edgeCapacitySnapshot, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(snapshot)
	}
	fmt.Fprintf(w, "Node: %s\nEgress: %g Mbit/s (%s)\nIngress: %g Mbit/s (%s)\nRevision: %d\n", snapshot.NodeID, float64(snapshot.EgressBudget)/125000, snapshot.EgressSource, float64(snapshot.IngressBudget)/125000, snapshot.IngressSource, snapshot.Revision)
	fmt.Fprintf(w, "Usable egress: %g Mbit/s\n", float64((capacity.Link{Budget: snapshot.EgressBudget}).Usable())/125000)
	if snapshot.AppliedRevision == nil {
		fmt.Fprintln(w, "Applied revision: unknown (edge offline or Helmsman does not report capacity)")
	} else {
		fmt.Fprintf(w, "Applied revision: %d\n", *snapshot.AppliedRevision)
		if *snapshot.AppliedRevision != snapshot.Revision {
			fmt.Fprintln(w, "Capacity not applied yet")
		}
	}
	if snapshot.DeliveryError != "" {
		fmt.Fprintf(w, "Capacity saved; live delivery pending: %s\n", snapshot.DeliveryError)
	}
	if snapshot.EgressSource == "DEFAULT" || snapshot.IngressSource == "DEFAULT" {
		fmt.Fprintln(w, "Default budget: 1 Gbit/s per defaulted direction")
	}
	return nil
}

type provisionCapacityQM interface {
	ListNodes(context.Context, string, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error)
	GetNodeCapacity(context.Context, string) (*quartermasterpb.NodeCapacity, error)
	SetNodeCapacity(context.Context, *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error)
}

func writeProvisionedCapacity(ctx context.Context, qm provisionCapacityQM, clusterID, selector string, mbps int) (string, error) {
	if mbps < 0 {
		return "", fmt.Errorf("bandwidth_mbps must be non-negative")
	}
	var budget *uint64
	var err error
	if mbps > 0 {
		budget, err = parseCapacityMbps(strconv.Itoa(mbps))
		if err != nil {
			return "", err
		}
	}
	nodes, err := listCapacityNodes(ctx, qm, clusterID)
	if err != nil {
		return "", err
	}
	node, err := matchClusterNode(nodes, selector, clusterID)
	if err != nil {
		return "", err
	}
	current, err := qm.GetNodeCapacity(ctx, node.GetNodeId())
	if err != nil {
		return "", err
	}
	_, err = qm.SetNodeCapacity(ctx, &quartermasterpb.SetNodeCapacityRequest{NodeId: node.GetNodeId(), EgressBudgetBytesPerSec: budget, IngressBudgetBytesPerSec: budget, ExpectedRevision: current.GetRevision(), Actor: &commonpb.RequestActor{AuthType: "service", UserId: "frameworks-cli"}})
	return node.GetNodeId(), err
}

func provisionedCapacityCommit(w io.Writer, access edgeControlAccess, clusterID, selector string, mbps int) func(context.Context) error {
	return func(ctx context.Context) error {
		cfg, resolver, cleanup, err := access(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		endpoint, err := resolver.ResolveGRPC(ctx, "quartermaster")
		if err != nil {
			return err
		}
		qm, err := qmclient.NewGRPCClient(clusterNodesQuartermasterGRPCConfig(endpoint, cfg))
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := clusterNodesRPCContext(ctx, cfg, 30*time.Second)
		defer cancel()
		nodeID, err := writeProvisionedCapacity(cctx, qm, clusterID, selector, mbps)
		if err != nil {
			return err
		}
		fh, pushErr := clusterNodesFoghornClient(cctx, resolver, cfg, clusterID)
		if pushErr == nil {
			defer func() { _ = fh.Close() }()
			_, pushErr = fh.PushNodeCapacity(cctx, nodeID)
		}
		if pushErr != nil {
			fmt.Fprintf(w, "[%s] Capacity saved; live delivery pending: %v\n", nodeID, pushErr)
		}
		return nil
	}
}

func edgeCapacityFollowup(nodeID string, mbps int) string {
	value := strconv.Itoa(mbps)
	if mbps == 0 {
		value = "default"
	}
	return fmt.Sprintf("frameworks edge capacity set %s --egress-mbps %s --ingress-mbps %s", nodeID, value, value)
}

// writeDeployedBridgeCapacity waits for inventory registration, then performs one fenced write.
func writeDeployedBridgeCapacity(ctx context.Context, client *bridge.Client, jwt, nodeID string, mbps int, timeout time.Duration) error {
	if mbps < 0 {
		return fmt.Errorf("bandwidth_mbps must be non-negative")
	}
	var budget *uint64
	var err error
	if mbps > 0 {
		budget, err = parseCapacityMbps(strconv.Itoa(mbps))
		if err != nil {
			return err
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		current, err := client.GetNodeCapacity(waitCtx, jwt, nodeID)
		if err == nil {
			_, err = client.SetNodeCapacity(waitCtx, jwt, nodeID, budget, budget, current.Revision)
			return err
		}
		lastErr = err
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("node registration or capacity API unavailable: %w (%w)", waitCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func listCapacityNodes(ctx context.Context, qm clusterNodesListQM, clusterID string) ([]*quartermasterpb.InfrastructureNode, error) {
	page := &commonpb.CursorPaginationRequest{First: 500}
	var nodes []*quartermasterpb.InfrastructureNode
	for {
		response, listErr := qm.ListNodes(ctx, clusterID, "edge", "", page)
		if listErr != nil {
			return nil, listErr
		}
		nodes = append(nodes, response.GetNodes()...)
		if !response.GetPagination().GetHasNextPage() {
			break
		}
		cursor := response.GetPagination().GetEndCursor()
		if cursor == "" || cursor == page.GetAfter() {
			return nil, fmt.Errorf("node pagination did not advance")
		}
		page.After = &cursor
	}
	return nodes, nil
}
