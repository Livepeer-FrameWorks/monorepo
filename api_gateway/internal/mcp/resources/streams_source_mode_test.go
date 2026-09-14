package resources

import (
	"reflect"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestStreamSourceInfoUsesRedactedViews(t *testing.T) {
	pull := streamPullSourceInfo(&commodorepb.PullSourceView{
		SourceUriRedacted: "rtsp://camera.example/***",
		Enabled:           true,
		Class:             "public",
		AllowedClusterIds: []string{"edge-us"},
	})
	if pull == nil || pull.SourceURIRedacted != "rtsp://camera.example/***" || !reflect.DeepEqual(pull.AllowedClusterIDs, []string{"edge-us"}) {
		t.Fatalf("unexpected pull source info: %+v", pull)
	}

	managed := streamManagedSourceInfo(&commodorepb.ManagedSourceView{
		SourceKind:        "playlist",
		AlwaysOn:          true,
		PlacementCount:    1,
		AllowedClusterIds: []string{"media-eu"},
	})
	if managed == nil || managed.SourceKind != "playlist" || !managed.AlwaysOn || managed.PlacementCount != 1 || !reflect.DeepEqual(managed.AllowedClusterIDs, []string{"media-eu"}) {
		t.Fatalf("unexpected managed source info: %+v", managed)
	}
}

func TestStreamSourceInfoOmitsAbsentViews(t *testing.T) {
	if streamPullSourceInfo(nil) != nil {
		t.Fatal("nil pull source must remain omitted")
	}
	if streamManagedSourceInfo(nil) != nil {
		t.Fatal("nil managed source must remain omitted")
	}
}
