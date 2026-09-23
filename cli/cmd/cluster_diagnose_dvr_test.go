package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
)

type fakeDVRDiagnosisSource struct {
	resolved    *commodorepb.ResolveDVRHashResponse
	resolveErr  error
	resolveJWT  string
	manifest    *inventory.Manifest
	diagnosis   *foghornpb.DiagnoseDVRResponse
	diagnoseErr error
	entries     []string
	diagnoseJWT string
}

func (f *fakeDVRDiagnosisSource) ResolveDVRHash(ctx context.Context, _ string) (*commodorepb.ResolveDVRHashResponse, error) {
	f.resolveJWT = ctxkeys.GetJWTToken(ctx)
	return f.resolved, f.resolveErr
}

func (f *fakeDVRDiagnosisSource) Manifest(context.Context) (*inventory.Manifest, error) {
	return f.manifest, nil
}

func (f *fakeDVRDiagnosisSource) DiagnoseDVR(ctx context.Context, entry, _ string) (*foghornpb.DiagnoseDVRResponse, error) {
	f.entries = append(f.entries, entry)
	f.diagnoseJWT = ctxkeys.GetJWTToken(ctx)
	return f.diagnosis, f.diagnoseErr
}

func perCellFoghornManifest() *inventory.Manifest {
	return &inventory.Manifest{Services: map[string]inventory.ServiceConfig{
		"foghorn-eu":  {Enabled: true, Deploy: "foghorn", Clusters: []string{"media-eu"}},
		"foghorn-us":  {Enabled: true, Deploy: "foghorn", Cluster: "media-us"},
		"foghorn-old": {Enabled: false, Deploy: "foghorn", Clusters: []string{"media-eu"}},
		"commodore":   {Enabled: true},
	}}
}

func sampleDVRDiagnosis() *foghornpb.DiagnoseDVRResponse {
	return &foghornpb.DiagnoseDVRResponse{
		Recording: &foghornpb.DVRRecordingDiagnosis{DvrHash: "dvrhash1", Status: "recording", StorageLocation: "local", SyncStatus: "failed", SyncError: "s3 timeout", StreamInternalName: "live+abc"},
		Segments: &foghornpb.DVRSegmentSummary{
			Count: 3, FirstMediaStartMs: 0, LastMediaEndMs: 18000,
			ByStatus: []*foghornpb.DVRSegmentStatusCount{{Status: "uploaded", Count: 2}, {Status: "lost_local", Count: 1}},
			Gaps:     []*foghornpb.DVRSegmentGap{{StartMs: 6000, EndMs: 12000, NextSequence: 3}},
			GapCount: 1,
		},
		Chapters: []*foghornpb.DVRChapterDiagnosis{
			{ChapterId: "ch-1", State: "finalizing", StartMs: 0, EndMs: 3600000, FinalizeAttempts: 2, FinalizeJobId: "chapter-finalize-v2-2-ch-1", FinalizeNodeId: "edge-1", LastFailureReason: "source missing"},
		},
		PendingFinalize: []*foghornpb.DVRPendingFinalize{{ChapterId: "ch-1", State: "finalizing", FinalizeAttempts: 2, FinalizeJobId: "chapter-finalize-v2-2-ch-1"}},
	}
}

func TestRunDVRDiagnoseRoutesToOriginCellEntry(t *testing.T) {
	src := &fakeDVRDiagnosisSource{
		resolved:  &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "tenant-a", OriginClusterId: "media-eu"},
		manifest:  perCellFoghornManifest(),
		diagnosis: sampleDVRDiagnosis(),
	}
	var buf bytes.Buffer
	if err := runDVRDiagnose(context.Background(), &buf, src, "operator-jwt", "dvrhash1", "", false); err != nil {
		t.Fatal(err)
	}
	if len(src.entries) != 1 || src.entries[0] != "foghorn-eu" {
		t.Fatalf("diagnosed entries = %v, want [foghorn-eu]", src.entries)
	}
	if src.diagnoseJWT != "operator-jwt" {
		t.Fatalf("DiagnoseDVR context JWT = %q, want the operator session", src.diagnoseJWT)
	}
	if src.resolveJWT != "" {
		t.Fatalf("ResolveDVRHash carried JWT %q; it runs under the service token", src.resolveJWT)
	}
	out := buf.String()
	for _, want := range []string{
		"DVR dvrhash1 on foghorn-eu",
		"origin_cluster=media-eu",
		`sync_error="s3 timeout"`,
		"by status: uploaded=2 lost_local=1",
		"6000-12000 ms (6000 ms) before sequence 3",
		"ch-1 state=finalizing [0,3600000) attempts=2 job=chapter-finalize-v2-2-ch-1 node=edge-1",
		`last_failure="source missing"`,
		"Finalize queue (1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDVRDiagnoseJSON(t *testing.T) {
	src := &fakeDVRDiagnosisSource{
		resolved:  &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "tenant-a", OriginClusterId: "media-us"},
		manifest:  perCellFoghornManifest(),
		diagnosis: sampleDVRDiagnosis(),
	}
	var buf bytes.Buffer
	if err := runDVRDiagnose(context.Background(), &buf, src, "jwt", "dvrhash1", "", true); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		FoghornService  string `json:"foghorn_service"`
		OriginClusterID string `json:"origin_cluster_id"`
		Diagnosis       struct {
			Segments struct {
				GapCount string `json:"gap_count"`
			} `json:"segments"`
			PendingFinalize []map[string]any `json:"pending_finalize"`
		} `json:"diagnosis"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json: %v\n%s", err, buf.String())
	}
	if decoded.FoghornService != "foghorn-us" || decoded.OriginClusterID != "media-us" || decoded.Diagnosis.Segments.GapCount != "1" || len(decoded.Diagnosis.PendingFinalize) != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestRunDVRDiagnoseServiceOverrideAndRefusals(t *testing.T) {
	base := func() *fakeDVRDiagnosisSource {
		return &fakeDVRDiagnosisSource{
			resolved:  &commodorepb.ResolveDVRHashResponse{Found: true, OriginClusterId: "media-eu"},
			manifest:  perCellFoghornManifest(),
			diagnosis: sampleDVRDiagnosis(),
		}
	}

	src := base()
	if err := runDVRDiagnose(context.Background(), &bytes.Buffer{}, src, "jwt", "dvrhash1", "foghorn-us", false); err != nil {
		t.Fatal(err)
	}
	if src.entries[0] != "foghorn-us" {
		t.Fatalf("--service ignored: %v", src.entries)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*fakeDVRDiagnosisSource)
		jwt     string
		service string
		want    string
	}{
		{name: "no operator session", jwt: "", want: "platform-operator session"},
		{name: "unknown hash", jwt: "jwt", mutate: func(s *fakeDVRDiagnosisSource) { s.resolved = &commodorepb.ResolveDVRHashResponse{} }, want: "no DVR recording"},
		{name: "commodore error", jwt: "jwt", mutate: func(s *fakeDVRDiagnosisSource) { s.resolveErr = errors.New("unavailable") }, want: "resolve DVR hash"},
		{name: "disabled entry override", jwt: "jwt", service: "foghorn-old", want: "not an enabled Foghorn entry"},
		{name: "no entry serves cluster", jwt: "jwt", mutate: func(s *fakeDVRDiagnosisSource) { s.resolved.OriginClusterId = "media-ap" }, want: `cluster "media-ap"`},
		{name: "foghorn error", jwt: "jwt", mutate: func(s *fakeDVRDiagnosisSource) { s.diagnoseErr = errors.New("permission denied") }, want: "diagnose DVR on foghorn-eu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := base()
			if tc.mutate != nil {
				tc.mutate(src)
			}
			err := runDVRDiagnose(context.Background(), &bytes.Buffer{}, src, tc.jwt, "dvrhash1", tc.service, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestFoghornEntryForDVR(t *testing.T) {
	single := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{"foghorn": {Enabled: true}}}
	for _, tc := range []struct {
		name     string
		manifest *inventory.Manifest
		origin   string
		want     string
		wantErr  string
	}{
		{name: "local access has no manifest", manifest: nil, origin: "media-eu", want: ""},
		{name: "single unassigned entry serves any cluster", manifest: single, origin: "media-eu", want: "foghorn"},
		{name: "single unassigned entry without origin", manifest: single, origin: "", want: "foghorn"},
		{name: "clusters list", manifest: perCellFoghornManifest(), origin: "media-eu", want: "foghorn-eu"},
		{name: "cluster shorthand", manifest: perCellFoghornManifest(), origin: "media-us", want: "foghorn-us"},
		{name: "no origin with several cells", manifest: perCellFoghornManifest(), origin: "", wantErr: "no origin cluster"},
		{name: "ambiguous", manifest: &inventory.Manifest{Services: map[string]inventory.ServiceConfig{
			"foghorn-a": {Enabled: true, Deploy: "foghorn", Clusters: []string{"media-eu"}},
			"foghorn-b": {Enabled: true, Deploy: "foghorn", Clusters: []string{"media-eu"}},
		}}, origin: "media-eu", wantErr: "pass --service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := foghornEntryForDVR(tc.manifest, tc.origin, "")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("entry = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestClusterDiagnoseRegistersDVRSubcommand(t *testing.T) {
	cmd, args, err := newClusterDiagnoseCmd().Find([]string{"dvr", "dvrhash1"})
	if err != nil || cmd.Name() != "dvr" || len(args) != 1 || cmd.Flags().Lookup("service") == nil {
		t.Fatalf("dvr subcommand: %v %v %v", cmd, args, err)
	}
	parent, _, err := newClusterDiagnoseCmd().Find([]string{"network"})
	if err != nil || parent.Name() != "diagnose" {
		t.Fatalf("existing components must still reach the parent: %v %v", parent, err)
	}
}
