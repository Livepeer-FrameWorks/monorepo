package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	commodore "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

const dvrDiagnoseRPCTimeout = 30 * time.Second

// dvrDiagnosisSource is what `cluster diagnose dvr` reads: Commodore's DVR
// registry, the manifest that names each cell's Foghorn entry, and the
// DiagnoseDVR RPC of one entry. An empty entry is the context's single Foghorn
// endpoint (local access).
type dvrDiagnosisSource interface {
	ResolveDVRHash(ctx context.Context, dvrHash string) (*commodorepb.ResolveDVRHashResponse, error)
	Manifest(ctx context.Context) (*inventory.Manifest, error)
	DiagnoseDVR(ctx context.Context, entry, dvrHash string) (*foghornpb.DiagnoseDVRResponse, error)
}

func newClusterDiagnoseDVRCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   "dvr <dvr-hash>",
		Short: "Show a DVR recording's segments, chapters, and finalize queue on its cell",
		Long: `Show one DVR recording as its origin cell's Foghorn sees it: lifecycle and
storage state, the segment ledger (count, media range, gaps), every chapter's
finalization state (attempts, last failure, finalize job ID), and the chapters
waiting in the finalize queue.

Commodore resolves the hash to its origin cluster; the manifest Foghorn entry
whose clusters include it serves the request. --service names the entry
instead. Requires a platform-operator login.`,
		Example: `  frameworks cluster diagnose dvr 5f0c2a9e8b7d4c1e9a3b6d2f7e8c1a4b
  frameworks cluster diagnose dvr 5f0c2a9e8b7d4c1e9a3b6d2f7e8c1a4b --service foghorn-eu --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctxCfg, err := activeContextWithAuth(cmd.Context())
			if err != nil {
				return err
			}
			src := &liveDVRDiagnosisSource{ctxCfg: ctxCfg, resolver: controlplane.NewResolver(ctxCfg)}
			defer src.resolver.Close()
			return runDVRDiagnose(cmd.Context(), cmd.OutOrStdout(), src, ctxCfg.Auth.JWT, args[0], service, output == "json")
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "manifest Foghorn service entry to query (e.g. foghorn-eu)")
	return cmd
}

func runDVRDiagnose(ctx context.Context, w io.Writer, src dvrDiagnosisSource, jwt, dvrHash, service string, outputJSON bool) error {
	dvrHash = strings.TrimSpace(dvrHash)
	service = strings.TrimSpace(service)
	if dvrHash == "" {
		return fmt.Errorf("DVR hash is required")
	}
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}

	// ResolveDVRHash is an internal Commodore read; it runs under the manifest
	// service token, while DiagnoseDVR below carries the operator's session.
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resolved, err := src.ResolveDVRHash(rctx, dvrHash)
	cancel()
	if err != nil {
		return fmt.Errorf("resolve DVR hash in Commodore: %w", err)
	}
	if !resolved.GetFound() {
		return fmt.Errorf("commodore has no DVR recording with hash %s", dvrHash)
	}

	manifest, err := src.Manifest(ctx)
	if err != nil {
		return err
	}
	entry, err := foghornEntryForDVR(manifest, resolved.GetOriginClusterId(), service)
	if err != nil {
		return err
	}

	dctx, dcancel := adminRPCContextTimeout(ctx, jwt, dvrDiagnoseRPCTimeout)
	defer dcancel()
	diagnosis, err := src.DiagnoseDVR(fhclient.WithOperatorBearer(dctx, jwt), entry, dvrHash)
	if err != nil {
		return fmt.Errorf("diagnose DVR on %s: %w", foghornEntryLabel(entry), err)
	}

	if outputJSON {
		body, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(diagnosis)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			DVRHash         string          `json:"dvr_hash"`
			TenantID        string          `json:"tenant_id"`
			OriginClusterID string          `json:"origin_cluster_id"`
			FoghornService  string          `json:"foghorn_service"`
			Diagnosis       json.RawMessage `json:"diagnosis"`
		}{dvrHash, resolved.GetTenantId(), resolved.GetOriginClusterId(), entry, body})
	}
	renderDVRDiagnosis(w, resolved, entry, diagnosis)
	return nil
}

// foghornEntryForDVR picks the manifest Foghorn entry that serves the origin
// cluster. A nil manifest (local access) has one Foghorn endpoint and returns
// the empty entry. An explicit service must be an enabled Foghorn entry.
func foghornEntryForDVR(manifest *inventory.Manifest, originClusterID, service string) (string, error) {
	if manifest == nil {
		return service, nil
	}
	var entries, unassigned, serving []string
	for name, svc := range manifest.Services {
		if !svc.Enabled || !serviceDeployMatches(name, svc, "foghorn") {
			continue
		}
		entries = append(entries, name)
		switch {
		case svc.Cluster == "" && len(svc.Clusters) == 0:
			unassigned = append(unassigned, name)
		case originClusterID != "" && (svc.Cluster == originClusterID || slices.Contains(svc.Clusters, originClusterID)):
			serving = append(serving, name)
		}
	}
	sort.Strings(entries)
	sort.Strings(unassigned)
	sort.Strings(serving)
	if service != "" {
		if !slices.Contains(entries, service) {
			return "", fmt.Errorf("--service %q is not an enabled Foghorn entry in the manifest (entries: %s)", service, strings.Join(entries, ", "))
		}
		return service, nil
	}
	switch {
	case len(serving) == 1:
		return serving[0], nil
	case len(serving) > 1:
		return "", fmt.Errorf("foghorn entries %s all serve cluster %q; pass --service", strings.Join(serving, ", "), originClusterID)
	case len(entries) == 1 && len(unassigned) == 1:
		return unassigned[0], nil
	case originClusterID == "":
		return "", fmt.Errorf("commodore records no origin cluster for this DVR; pass --service (entries: %s)", strings.Join(entries, ", "))
	default:
		return "", fmt.Errorf("no manifest Foghorn entry lists cluster %q in its clusters; pass --service (entries: %s)", originClusterID, strings.Join(entries, ", "))
	}
}

func foghornEntryLabel(entry string) string {
	if entry == "" {
		return "foghorn"
	}
	return entry
}

func renderDVRDiagnosis(w io.Writer, resolved *commodorepb.ResolveDVRHashResponse, entry string, d *foghornpb.DiagnoseDVRResponse) {
	rec := d.GetRecording()
	ux.Heading(w, fmt.Sprintf("DVR %s on %s", rec.GetDvrHash(), foghornEntryLabel(entry)))
	origin := resolved.GetOriginClusterId()
	if origin == "" {
		origin = "-"
	}
	_, _ = fmt.Fprintf(w, "  tenant=%s stream=%s origin_cluster=%s\n", resolved.GetTenantId(), rec.GetStreamInternalName(), origin)
	_, _ = fmt.Fprintf(w, "  status=%s", rec.GetStatus())
	if rec.GetErrorMessage() != "" {
		_, _ = fmt.Fprintf(w, " error=%q", rec.GetErrorMessage())
	}
	_, _ = fmt.Fprintf(w, " started=%s ended=%s duration=%ds\n", formatOptionalTime(rec.GetStartedAt()), formatOptionalTime(rec.GetEndedAt()), rec.GetDurationSeconds())
	_, _ = fmt.Fprintf(w, "  storage=%s sync=%s failures=%d last_attempt=%s", rec.GetStorageLocation(), rec.GetSyncStatus(), rec.GetSyncFailureCount(), formatOptionalTime(rec.GetLastSyncAttempt()))
	if rec.GetSyncNodeId() != "" {
		_, _ = fmt.Fprintf(w, " node=%s", rec.GetSyncNodeId())
	}
	if rec.GetSyncError() != "" {
		_, _ = fmt.Fprintf(w, " sync_error=%q", rec.GetSyncError())
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "  dtsh_synced=%t dtsh_status=%s dtsh_failures=%d frozen=%s size=%d\n", rec.GetDtshSynced(), dashIfEmpty(rec.GetDtshStatus()), rec.GetDtshFailureCount(), formatOptionalTime(rec.GetFrozenAt()), rec.GetSizeBytes())
	_, _ = fmt.Fprintf(w, "  retention_until=%s chapter_mode=%s interval=%ds backfill_complete=%t", formatOptionalTime(rec.GetRetentionUntil()), dashIfEmpty(rec.GetChapterMode()), rec.GetChapterIntervalSeconds(), rec.GetChapterBackfillComplete())
	if rec.GetStartDispatchState() != "" {
		_, _ = fmt.Fprintf(w, " start_dispatch=%s", rec.GetStartDispatchState())
	}
	_, _ = fmt.Fprintln(w)

	seg := d.GetSegments()
	_, _ = fmt.Fprintf(w, "\nSegments: %d, media %d-%d ms, %d ms recorded, %d bytes\n", seg.GetCount(), seg.GetFirstMediaStartMs(), seg.GetLastMediaEndMs(), seg.GetTotalDurationMs(), seg.GetTotalSizeBytes())
	if len(seg.GetByStatus()) > 0 {
		parts := make([]string, 0, len(seg.GetByStatus()))
		for _, s := range seg.GetByStatus() {
			parts = append(parts, fmt.Sprintf("%s=%d", s.GetStatus(), s.GetCount()))
		}
		_, _ = fmt.Fprintf(w, "  by status: %s\n", strings.Join(parts, " "))
	}
	if seg.GetGapCount() > 0 {
		_, _ = fmt.Fprintf(w, "  gaps: %d", seg.GetGapCount())
		if int64(len(seg.GetGaps())) < seg.GetGapCount() {
			_, _ = fmt.Fprintf(w, " (first %d shown)", len(seg.GetGaps()))
		}
		_, _ = fmt.Fprintln(w)
		for _, g := range seg.GetGaps() {
			_, _ = fmt.Fprintf(w, "    %d-%d ms (%d ms) before sequence %d\n", g.GetStartMs(), g.GetEndMs(), g.GetEndMs()-g.GetStartMs(), g.GetNextSequence())
		}
	} else {
		_, _ = fmt.Fprintln(w, "  gaps: none")
	}

	_, _ = fmt.Fprintf(w, "\nChapters (%d)\n", len(d.GetChapters()))
	for _, c := range d.GetChapters() {
		_, _ = fmt.Fprintf(w, "  - %s state=%s [%d,%d) attempts=%d", c.GetChapterId(), c.GetState(), c.GetStartMs(), c.GetEndMs(), c.GetFinalizeAttempts())
		if c.GetFinalizeJobId() != "" {
			_, _ = fmt.Fprintf(w, " job=%s", c.GetFinalizeJobId())
		}
		if c.GetFinalizeNodeId() != "" {
			_, _ = fmt.Fprintf(w, " node=%s", c.GetFinalizeNodeId())
		}
		if c.GetLastFailureReason() != "" {
			_, _ = fmt.Fprintf(w, " last_failure=%q", c.GetLastFailureReason())
		}
		_, _ = fmt.Fprintln(w)
	}

	_, _ = fmt.Fprintf(w, "\nFinalize queue (%d)\n", len(d.GetPendingFinalize()))
	for _, p := range d.GetPendingFinalize() {
		_, _ = fmt.Fprintf(w, "  - %s state=%s attempts=%d queued=%s started=%s", p.GetChapterId(), p.GetState(), p.GetFinalizeAttempts(), formatOptionalTime(p.GetQueuedAt()), formatOptionalTime(p.GetFinalizeStartedAt()))
		if p.GetFinalizeJobId() != "" {
			_, _ = fmt.Fprintf(w, " job=%s", p.GetFinalizeJobId())
		}
		if p.GetFinalizeNodeId() != "" {
			_, _ = fmt.Fprintf(w, " node=%s", p.GetFinalizeNodeId())
		}
		if p.GetLastFailureReason() != "" {
			_, _ = fmt.Fprintf(w, " last_failure=%q", p.GetLastFailureReason())
		}
		_, _ = fmt.Fprintln(w)
	}
}

// liveDVRDiagnosisSource dials Commodore and the chosen Foghorn entry through
// one resolver, so both share the context's SSH session.
type liveDVRDiagnosisSource struct {
	ctxCfg   fwcfg.Context
	resolver *controlplane.Resolver
}

func (s *liveDVRDiagnosisSource) ResolveDVRHash(ctx context.Context, dvrHash string) (*commodorepb.ResolveDVRHashResponse, error) {
	ep, err := s.resolver.ResolveGRPC(ctx, "commodore")
	if err != nil {
		return nil, err
	}
	cli, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  s.ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		CACertFile:    ep.CACertFile,
		CACertPEM:     ep.CACertPEM,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Commodore gRPC: %w", err)
	}
	defer func() { _ = cli.Close() }()
	return cli.ResolveDVRHash(ctx, dvrHash)
}

func (s *liveDVRDiagnosisSource) Manifest(ctx context.Context) (*inventory.Manifest, error) {
	return s.resolver.Manifest(ctx)
}

func (s *liveDVRDiagnosisSource) DiagnoseDVR(ctx context.Context, entry, dvrHash string) (*foghornpb.DiagnoseDVRResponse, error) {
	var ep controlplane.Endpoint
	var err error
	if entry == "" {
		ep, err = s.resolver.ResolveGRPC(ctx, "foghorn")
	} else {
		ep, err = s.resolver.ResolveGRPCEntry(ctx, "foghorn", entry)
	}
	if err != nil {
		return nil, err
	}
	fh, err := fhclient.NewGRPCClient(adminFoghornGRPCConfig(ep, s.ctxCfg))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}
	defer func() { _ = fh.Close() }()
	return fh.DiagnoseDVR(ctx, dvrHash)
}
