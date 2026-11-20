package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

const (
	standardLadderJSON = `[{"name":"720p","codec":"H264","bitrate":3200000,"resolution":"x720","profile":"high","track_inhibit":"video=<1280x720"},` +
		`{"name":"1080p","codec":"H264","bitrate":6500000,"resolution":"x1080","profile":"high","track_inhibit":"video=<1920x1080"}]`
	uhdLadderJSON = `[{"name":"720p","codec":"H264","bitrate":3200000,"resolution":"x720","profile":"high","track_inhibit":"video=<1280x720"},` +
		`{"name":"1080p","codec":"H264","bitrate":6500000,"resolution":"x1080","profile":"high","track_inhibit":"video=<1920x1080"},` +
		`{"name":"2160p","codec":"H264","bitrate":15000000,"resolution":"x2160","profile":"high","track_inhibit":"video=<3840x2160"}]`
	tierLiveProcesses = `[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"audio=all&video=none&subtitle=none&meta=none"},` +
		`{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","track_inhibit":"video=<640x360","target_profiles":` + standardLadderJSON + `},` +
		`{"process":"Thumbs","track_select":"video=lowres"}]`
)

// ladderTier is a tier whose processes carry its default (standard) ladder;
// uhd adds it to the ladders the tier allows.
func ladderTier(uhd bool) *purserpb.BillingTier {
	tier := &purserpb.BillingTier{
		TierName: "production", ProcessesLive: tierLiveProcesses, ProcessesVod: tierLiveProcesses,
		DefaultTranscodeLadder: "standard",
		TranscodeLadders:       []*purserpb.TranscodeLadder{{Name: "standard", DisplayName: "Standard", RenditionsJson: standardLadderJSON}},
	}
	if uhd {
		tier.TranscodeLadders = append(tier.TranscodeLadders, &purserpb.TranscodeLadder{Name: "uhd", DisplayName: "UHD", RenditionsJson: uhdLadderJSON})
	}
	return tier
}

func ladderPurser(t *testing.T, tier *purserpb.BillingTier) *purserEntitlementFake {
	t.Helper()
	return &purserEntitlementFake{
		subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-1"}}, nil
		},
		tier: func(context.Context, *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error) {
			return tier, nil
		},
	}
}

func renditionNames(t *testing.T, processesJSON string) []string {
	t.Helper()
	if !mist.HasTranscodeProcesses(processesJSON) {
		return nil
	}
	spec, err := mist.LivepeerJobSpecFromProcessesJSON(mist.SetLivepeerWorkload(processesJSON, mist.WorkloadLive, 0, 0))
	if err != nil {
		t.Fatalf("transcode spec of %s: %v", processesJSON, err)
	}
	names := make([]string, 0, len(spec.Profiles))
	for _, r := range spec.Profiles {
		names = append(names, r["name"].(string))
	}
	return names
}

// expectTenantLadder mocks the tenant's ladder read; "" is a tenant without one.
func expectTenantLadder(mock sqlmock.Sqlmock, ladder string) {
	query := mock.ExpectQuery("GetTenantTranscodeLadder").WithArgs(testTenantID)
	if ladder == "" {
		query.WillReturnError(sql.ErrNoRows)
		return
	}
	query.WillReturnRows(sqlmock.NewRows([]string{"transcode_ladder"}).AddRow(ladder))
}

// The stream's ladder wins over the tenant's, the tenant's over the tier
// default; a ladder the tier no longer allows is skipped, never served; a full
// stream override stays verbatim; and live_video_abr OFF still strips the
// renditions of whichever ladder applies.
func TestResolveProcessesAppliesTheTranscodeLadder(t *testing.T) {
	const operatorPolicy = `[{"process":"Thumbs","track_select":"video=maxbps"}]`
	for name, test := range map[string]struct {
		uhdAllowed     bool
		streamOverride string
		abr            string
		streamLadder   string
		tenantLadder   string
		lifecycle      string
		streamID       string
		want           []string
	}{
		"tier default":                     {uhdAllowed: true, abr: "INHERIT", lifecycle: "live", streamID: "s1", want: []string{"720p", "1080p"}},
		"tenant ladder":                    {uhdAllowed: true, abr: "INHERIT", tenantLadder: "uhd", lifecycle: "live", streamID: "s1", want: []string{"720p", "1080p", "2160p"}},
		"stream ladder over tenant ladder": {uhdAllowed: true, abr: "INHERIT", streamLadder: "uhd", tenantLadder: "standard", lifecycle: "live", streamID: "s1", want: []string{"720p", "1080p", "2160p"}},
		"stream ladder after downgrade":    {abr: "INHERIT", streamLadder: "uhd", lifecycle: "live", streamID: "s1", want: []string{"720p", "1080p"}},
		"tenant ladder after downgrade":    {abr: "INHERIT", tenantLadder: "uhd", lifecycle: "live", streamID: "s1", want: []string{"720p", "1080p"}},
		"abr off strips the ladder":        {uhdAllowed: true, abr: "OFF", streamLadder: "uhd", lifecycle: "live", streamID: "s1", want: nil},
		"vod takes the tenant ladder":      {uhdAllowed: true, tenantLadder: "uhd", lifecycle: "vod", want: []string{"720p", "1080p", "2160p"}},
		"operator stream policy verbatim":  {uhdAllowed: true, streamOverride: operatorPolicy, abr: "INHERIT", streamLadder: "uhd", lifecycle: "live", streamID: "s1", want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(test.uhdAllowed)))
			if test.streamID != "" {
				override := mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs(test.streamID, testTenantID)
				if test.streamOverride == "" {
					override.WillReturnError(sql.ErrNoRows)
				} else {
					override.WillReturnRows(sqlmock.NewRows([]string{"processes_live", "processes_dvr", "processes_clip", "processes_dvr_finalize", "processes_vod"}).
						AddRow([]byte(test.streamOverride), []byte(`null`), []byte(`null`), []byte(`null`), []byte(`null`)))
				}
				mock.ExpectQuery("GetStreamProcessingChoices").WithArgs(test.streamID, testTenantID).
					WillReturnRows(sqlmock.NewRows([]string{"live_video_abr", "transcode_ladder"}).AddRow(test.abr, test.streamLadder))
			}
			_, explicitAllowed := allowedTranscodeLadder(ladderTier(test.uhdAllowed), test.streamLadder)
			if test.streamOverride == "" && !explicitAllowed && test.abr != "OFF" {
				expectTenantLadder(mock, test.tenantLadder)
			}

			got, err := server.resolveProcessesJSONStrict(context.Background(), testTenantID, test.streamID, "", test.lifecycle)
			if err != nil {
				t.Fatal(err)
			}
			if names := renditionNames(t, got); strings.Join(names, ",") != strings.Join(test.want, ",") {
				t.Fatalf("renditions %v, want %v (config %s)", names, test.want, got)
			}
			if test.streamOverride != "" && got != mist.NormalizeProcessConfigSelectors(test.streamOverride) {
				t.Fatalf("operator policy changed: %s", got)
			}
			if test.abr != "OFF" && test.streamOverride == "" && !strings.Contains(got, `"x-LSP-name"`) && !strings.Contains(got, `"Thumbs"`) {
				t.Fatalf("the tier's other processes were dropped: %s", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A ladder the tenant's tier does not allow is refused on every write path
// before anything is stored.
func TestTranscodeLadderWritesRefuseALadderTheTierDoesNotAllow(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(false)))
	ctx := ctxAs("u1", testTenantID, "owner")

	_, err := server.CreateStream(ctx, &commodorepb.CreateStreamRequest{Title: "4K", TranscodeLadder: "uhd"})
	wantCode(t, err, codes.InvalidArgument)
	if !strings.Contains(err.Error(), `"uhd" is not available on your plan; choose one of: standard`) {
		t.Fatalf("refusal does not name the allowed ladders: %v", err)
	}
	_, err = server.SetTenantTranscodeLadder(ctx, &commodorepb.SetTenantTranscodeLadderRequest{TranscodeLadder: "UHD"})
	wantCode(t, err, codes.InvalidArgument)
	_, err = server.CreateStream(ctx, &commodorepb.CreateStreamRequest{Title: "typo", TranscodeLadder: "ultra"})
	wantCode(t, err, codes.InvalidArgument)

	mock.ExpectQuery("ingest_mode, is_recording_enabled").WithArgs("s1", "u1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).AddRow("live+abc", "push", false))
	_, err = server.UpdateStream(ctx, &commodorepb.UpdateStreamRequest{StreamId: "s1", TranscodeLadder: proto.String("uhd")})
	wantCode(t, err, codes.InvalidArgument)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetTenantTranscodeLadderStoresAnAllowedLadder(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	ctx := ctxAs("u1", testTenantID, "owner")

	mock.ExpectExec("SetTenantTranscodeLadder").WithArgs(testTenantID, "uhd").WillReturnResult(sqlmock.NewResult(0, 1))
	expectTenantLadder(mock, "uhd")
	policy, err := server.SetTenantTranscodeLadder(ctx, &commodorepb.SetTenantTranscodeLadderRequest{TranscodeLadder: " UHD "})
	if err != nil {
		t.Fatal(err)
	}
	if policy.GetTenantLadder() != "uhd" || policy.GetEffectiveLadder() != "uhd" || policy.GetTierDefault() != "standard" || len(policy.GetLadders()) != 2 {
		t.Fatalf("policy = %+v", policy)
	}
	if got := strings.Join(policy.GetLadders()[1].GetRenditionNames(), ","); got != "720p,1080p,2160p" {
		t.Fatalf("uhd renditions %s", got)
	}

	// Empty returns the tenant to the tier default.
	mock.ExpectExec("SetTenantTranscodeLadder").WithArgs(testTenantID, nil).WillReturnResult(sqlmock.NewResult(0, 1))
	expectTenantLadder(mock, "")
	if policy, err = server.SetTenantTranscodeLadder(ctx, &commodorepb.SetTenantTranscodeLadderRequest{}); err != nil {
		t.Fatal(err)
	}
	if policy.GetTenantLadder() != "" || policy.GetEffectiveLadder() != "standard" {
		t.Fatalf("cleared policy = %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVODCompletionResolvesTheSavedUploadLadder(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	mock.ExpectQuery("GetVODTranscodeLadder").WithArgs(testTenantID, "upload").WillReturnRows(sqlmock.NewRows([]string{"transcode_ladder"}).AddRow("uhd"))
	got, err := server.resolveVODProcessesJSON(context.Background(), testTenantID, "cell", "upload")
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(renditionNames(t, got), ","); names != "720p,1080p,2160p" {
		t.Fatalf("upload choice lost to tenant default: %s", names)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVODDisallowedLadderRefusedBeforeCreation(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(false)))
	ctx := ctxAs("u1", testTenantID, "owner")
	_, err := server.CreateVodUpload(ctx, &sharedpb.CreateVodUploadRequest{Filename: "phone.mov", SizeBytes: 100, TranscodeLadder: "uhd"})
	wantCode(t, err, codes.InvalidArgument)
	_, err = server.ImportVodAsset(ctx, &sharedpb.ImportVodAssetRequest{SourceUrl: "https://example.com/video.mp4", TranscodeLadder: "uhd"})
	wantCode(t, err, codes.InvalidArgument)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamProcessingOverrideSurvivesOtherNullLifecycles(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	const policy = `[{"process":"Thumbs","track_select":"video=maxbps"}]`
	mock.ExpectQuery("GetStreamProcessingOverrides[\\s\\S]*COALESCE").WithArgs("stream-one", testTenantID).WillReturnRows(
		sqlmock.NewRows([]string{"processes_live", "processes_dvr", "processes_clip", "processes_dvr_finalize", "processes_vod"}).AddRow(policy, "", "", "", ""))
	if got := server.getStreamProcessingOverride(context.Background(), testTenantID, "stream-one", "live"); got != policy {
		t.Fatalf("explicit live override was discarded when unrelated lifecycle columns were SQL NULL: got %q, want %s", got, policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveLadderLookupFailurePreservesProcessing(t *testing.T) {
	for _, failure := range []string{"stream", "tenant"} {
		for _, abr := range []string{"INHERIT", "OFF"} {
			t.Run(failure+"/"+abr, func(t *testing.T) {
				server, mock, done := newMockServer(t)
				defer done()
				server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
				mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("s1", testTenantID).WillReturnError(sql.ErrNoRows)
				choices := mock.ExpectQuery("GetStreamProcessingChoices").WithArgs("s1", testTenantID)
				if failure == "stream" {
					choices.WillReturnError(sql.ErrConnDone)
				} else {
					choices.WillReturnRows(sqlmock.NewRows([]string{"live_video_abr", "transcode_ladder"}).AddRow(abr, ""))
					if abr != "OFF" {
						mock.ExpectQuery("GetTenantTranscodeLadder").WithArgs(testTenantID).WillReturnError(sql.ErrConnDone)
					}
				}
				got := server.resolveProcessesJSON(context.Background(), testTenantID, "s1", "", "live")
				if !strings.Contains(got, `"Thumbs"`) || !strings.Contains(got, `"opus"`) {
					t.Fatalf("lookup failure discarded audio or thumbnails: %s", got)
				}
				wantVideo := failure != "stream" && abr != "OFF"
				if mist.HasTranscodeProcesses(got) != wantVideo {
					t.Fatalf("lookup failure lost tier ladder or enabled video without known ABR: %s", got)
				}
				if wantVideo && strings.Join(renditionNames(t, got), ",") != "720p,1080p" {
					t.Fatalf("lookup failure must use the entitled tier default: %s", got)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
