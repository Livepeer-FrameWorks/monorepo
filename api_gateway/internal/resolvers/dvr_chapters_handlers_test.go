package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

// DoRetrieveDVRChapter builds the request from the chapter window, maps state,
// prefers actual media bounds when present, and sets PlayableNow from state.
func TestDoRetrieveDVRChapter(t *testing.T) {
	var got *foghorncontrolpb.RetrieveDVRChapterRequest
	c := &clientstest.FakeCommodore{
		RetrieveDVRChapterFn: func(_ context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error) {
			got = req
			return &foghorncontrolpb.RetrieveDVRChapterResponse{
				ChapterId:          "ch1",
				State:              "finalized",
				IsCurrent:          false,
				SegmentCount:       3,
				StartMs:            1000,
				EndMs:              2000,
				ActualMediaStartMs: 1100,
				ActualMediaEndMs:   1900,
				PlaybackId:         "pb-ch1",
			}, nil
		},
	}
	out, err := commoW2(c).DoRetrieveDVRChapter(clientstest.AuthedCtx("t1"), "dvr9", model.DVRChapterModeFixedInterval, 60, 1000, 2000)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.DvrArtifactId != "dvr9" || got.Mode != "fixed_interval" || got.IntervalSeconds != 60 || got.StartMs != 1000 || got.EndMs != 2000 {
		t.Fatalf("request built wrong: %+v", got)
	}
	// Actual media bounds override the requested window.
	if out.WallClockStartUnixMs != 1100 || out.WallClockEndUnixMs != 1900 {
		t.Fatalf("actual media bounds not applied: %+v", out)
	}
	if out.State != model.DVRChapterStateFinalized || !out.PlayableNow {
		t.Fatalf("state/playable mapped wrong: %+v", out)
	}
	if out.PlaybackID == nil || *out.PlaybackID != "pb-ch1" || out.SegmentCount != 3 {
		t.Fatalf("output mapped wrong: %+v", out)
	}

	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoRetrieveDVRChapter(context.Background(), "d", model.DVRChapterModeNone, 0, 0, 0); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		RetrieveDVRChapterFn: func(context.Context, *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoRetrieveDVRChapter(clientstest.AuthedCtx("t1"), "d", model.DVRChapterModeNone, 0, 0, 0); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoListDVRChapters builds the paginated request and maps each ChapterRef,
// populating optional intervalSeconds/playbackId and nextPageToken.
func TestDoListDVRChapters(t *testing.T) {
	var got *foghorncontrolpb.ListDVRChaptersRequest
	c := &clientstest.FakeCommodore{
		ListDVRChaptersFn: func(_ context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
			got = req
			return &foghorncontrolpb.ListDVRChaptersResponse{
				Chapters: []*foghorncontrolpb.ChapterRef{
					{
						ChapterId:       "c1",
						Mode:            "fixed_interval",
						IntervalSeconds: 30,
						StartMs:         0,
						EndMs:           30000,
						State:           "open",
						SegmentCount:    1,
						PlaybackId:      "pb-c1",
					},
				},
				NextPageToken: "next-tok",
			}, nil
		},
	}
	out, err := commoW2(c).DoListDVRChapters(clientstest.AuthedCtx("t1"), "dvr9", model.DVRChapterModeFixedInterval, 30, 0, 60000, 50, "tok0")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.DvrArtifactId != "dvr9" || got.Mode != "fixed_interval" || got.IntervalSeconds != 30 ||
		got.RangeStartMs != 0 || got.RangeEndMs != 60000 || got.PageSize != 50 || got.PageToken != "tok0" {
		t.Fatalf("request built wrong: %+v", got)
	}
	if len(out.Chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(out.Chapters))
	}
	ref := out.Chapters[0]
	if ref.ChapterID != "c1" || ref.Mode != model.DVRChapterModeFixedInterval || ref.State != model.DVRChapterStateOpen {
		t.Fatalf("chapter mapped wrong: %+v", ref)
	}
	if ref.IntervalSeconds == nil || *ref.IntervalSeconds != 30 || ref.PlaybackID == nil || *ref.PlaybackID != "pb-c1" {
		t.Fatalf("optional fields mapped wrong: %+v", ref)
	}
	if out.NextPageToken == nil || *out.NextPageToken != "next-tok" {
		t.Fatalf("nextPageToken mapped wrong: %+v", out.NextPageToken)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		ListDVRChaptersFn: func(context.Context, *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoListDVRChapters(clientstest.AuthedCtx("t1"), "d", model.DVRChapterModeNone, 0, 0, 0, 10, ""); err == nil {
		t.Fatal("backend error should propagate")
	}
}
