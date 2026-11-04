package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

type streamDiagnosisSource interface {
	ResolveStream(context.Context, string) (*commodorepb.DiagnoseStreamResponse, error)
	Manifest(context.Context) (*inventory.Manifest, error)
	DiagnoseStream(context.Context, string, *foghornpb.DiagnoseStreamRequest) (*foghornpb.DiagnoseStreamResponse, error)
}

func newClusterDiagnoseStreamCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{Use: "stream <stream-or-playback-id>", Short: "Inspect ingest claims, authority delivery, sessions, and push targets", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := recordDiagnosticRead(cmd.Context(), "stream", args[0], service); err != nil {
				return err
			}
			cfg, err := activeContextWithAuth(cmd.Context())
			if err != nil {
				return err
			}
			resolver := controlplane.NewResolver(cfg)
			defer resolver.Close()
			c, _, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer c.Close()
			src := &liveStreamDiagnosisSource{liveDVRDiagnosisSource: &liveDVRDiagnosisSource{ctxCfg: cfg, resolver: resolver}, resolve: c.DiagnoseStream}
			return runStreamDiagnose(cmd.Context(), cmd.OutOrStdout(), src, cfg.Auth.JWT, strings.TrimSpace(args[0]), service, output == "json")
		}}
	cmd.Flags().StringVar(&service, "service", "", "manifest Foghorn service entry to query")
	return cmd
}

type streamCellDiagnosis struct {
	runtime   *foghornpb.DiagnoseStreamResponse
	Service   string          `json:"service"`
	Diagnosis json.RawMessage `json:"diagnosis,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func runStreamDiagnose(ctx context.Context, w io.Writer, src streamDiagnosisSource, jwt, identifier, service string, jsonOutput bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	ctx, cancel := adminRPCContextTimeout(ctx, jwt, dvrDiagnoseRPCTimeout)
	defer cancel()
	stream, err := src.ResolveStream(ctx, identifier)
	if err != nil {
		return fmt.Errorf("resolve stream: %w", err)
	}
	manifest, err := src.Manifest(ctx)
	if err != nil {
		return err
	}
	targets := map[string]bool{}
	if service != "" || manifest == nil {
		entry, entryErr := foghornEntryForDVR(manifest, "", service)
		if entryErr != nil {
			return entryErr
		}
		targets[entry] = true
	} else {
		cluster := stream.GetActiveClusterId()
		if cluster == "" {
			cluster = stream.GetLastClusterId()
		}
		if cluster != "" {
			targets[cluster] = true
		}
		for _, a := range stream.GetAuthorities() {
			if a.GetCellId() != "" {
				targets[a.GetCellId()] = true
			}
		}
	}
	keys := make([]string, 0, len(targets))
	for k := range targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cells []streamCellDiagnosis
	incomplete := false
	seenEntries := map[string]bool{}
	for _, target := range keys {
		entry := target
		if manifest != nil && service == "" {
			entry, err = foghornEntryForDVR(manifest, target, "")
		} else {
			err = nil
		}
		if err == nil && seenEntries[entry] {
			continue
		}
		if err == nil {
			seenEntries[entry] = true
		}
		item := streamCellDiagnosis{Service: entry}
		if err == nil {
			var runtime *foghornpb.DiagnoseStreamResponse
			runtime, err = src.DiagnoseStream(fhclient.WithOperatorBearer(ctx, jwt), entry, &foghornpb.DiagnoseStreamRequest{TenantId: stream.GetTenantId(), StreamId: stream.GetStreamId(), InternalName: stream.GetInternalName()})
			if err == nil {
				item.Diagnosis, err = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(runtime)
				item.runtime = runtime
			}
		}
		if err != nil {
			item.Service = target
			item.Error = err.Error()
			incomplete = true
		}
		cells = append(cells, item)
	}
	if jsonOutput {
		body, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(stream)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Stream json.RawMessage       `json:"stream"`
			Cells  []streamCellDiagnosis `json:"cells"`
		}{body, cells}); err != nil {
			return err
		}
	} else {
		ux.Heading(w, "Stream "+stream.GetStreamId())
		fmt.Fprintf(w, "  tenant=%s playback=%s ingest_mode=%s\n  active_cluster=%s last_cluster=%s node=%s control_cell=%s\n  claim=%s last_renewed=%s (lease observation; not proof of liveness)\n", stream.GetTenantId(), stream.GetPlaybackId(), stream.GetIngestMode(), dashIfEmpty(stream.GetActiveClusterId()), dashIfEmpty(stream.GetLastClusterId()), dashIfEmpty(stream.GetNodeId()), dashIfEmpty(stream.GetControlCellId()), dashIfEmpty(stream.GetClaimId()), dashIfEmpty(stream.GetClaimUpdatedAt()))
		fmt.Fprintln(w, "\nAuthority delivery (acknowledgment is distinct from edge application):")
		for _, a := range stream.GetAuthorities() {
			fmt.Fprintf(w, "  %s %s cell=%s current=%d targeted=%d acknowledged=%d at=%s\n", a.GetKind(), a.GetAuthorityId(), a.GetCellId(), a.GetCurrentVersion(), a.GetTargetedVersion(), a.GetAcknowledgedVersion(), dashIfEmpty(a.GetAcknowledgedAt()))
		}
		fmt.Fprintf(w, "\nPush targets (%d of %d):\n", len(stream.GetPushTargets()), stream.GetPushTargetCount())
		for _, p := range stream.GetPushTargets() {
			fmt.Fprintf(w, "  %s platform=%s enabled=%t status=%s reason=%s\n", p.GetId(), p.GetPlatform(), p.GetEnabled(), p.GetStatus(), p.GetReasonCode())
		}
		for _, cell := range cells {
			fmt.Fprintf(w, "\nCell %s:\n", foghornEntryLabel(cell.Service))
			if cell.Error != "" {
				fmt.Fprintf(w, "  unavailable: %s\n", cell.Error)
				continue
			}
			r := cell.runtime
			for _, a := range r.GetAuthorities() {
				fmt.Fprintf(w, "  held %s version=%d valid_until=%s lifecycle=%s billing=%s reason=%q\n", a.GetKind(), a.GetVersion(), a.GetValidUntil(), dashIfEmpty(a.GetLifecycle()), dashIfEmpty(a.GetBillingDecision()), a.GetDecisionReason())
			}
			fmt.Fprintf(w, "  Recent ingest sessions (%d of %d):\n", len(r.GetSessions()), r.GetSessionCount())
			for _, s := range r.GetSessions() {
				fmt.Fprintf(w, "    %s node=%s cluster=%s claim=%s projection=%s started=%s ended=%s reason=%s\n", s.GetId(), s.GetNodeId(), s.GetClusterId(), s.GetClaimId(), s.GetProjectionState(), s.GetStartedAt(), dashIfEmpty(s.GetEndedAt()), dashIfEmpty(s.GetEndedReason()))
			}
		}
		if len(cells) == 0 {
			fmt.Fprintln(w, "  No recorded cell to query.")
		}
		fmt.Fprintln(w, "\nHistorical placement reason: unavailable; recent lifecycle evidence is the ingest-session ledger.")
	}
	if incomplete {
		return fmt.Errorf("stream diagnosis incomplete; one or more cells could not be read")
	}
	return nil
}

type liveStreamDiagnosisSource struct {
	*liveDVRDiagnosisSource
	resolve func(context.Context, string) (*commodorepb.DiagnoseStreamResponse, error)
}

func (s *liveStreamDiagnosisSource) ResolveStream(ctx context.Context, identifier string) (*commodorepb.DiagnoseStreamResponse, error) {
	return s.resolve(ctx, identifier)
}
func (s *liveStreamDiagnosisSource) DiagnoseStream(ctx context.Context, entry string, req *foghornpb.DiagnoseStreamRequest) (*foghornpb.DiagnoseStreamResponse, error) {
	fh, err := s.foghornClient(ctx, entry)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return fh.DiagnoseStream(ctx, req)
}
