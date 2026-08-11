package resolvers

import (
	"testing"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func aoStr(s string) *string { return &s }
func aoBool(b bool) *bool    { return &b }

// applyArtifactStorageStateToClip projects a Periscope ArtifactState onto the
// client-facing Clip. The load-bearing rule: when no explicit StorageLocation is
// known but a local FilePath exists, surface "local" rather than leaving the field
// blank (otherwise the UI cannot tell a hot copy from an unknown one). nil inputs
// must be a no-op, never a panic.
func TestApplyArtifactStorageStateToClip(t *testing.T) {
	t.Run("nil state and nil clip are no-ops", func(t *testing.T) {
		applyArtifactStorageStateToClip(nil, &periscopepb.ArtifactState{})
		clip := &sharedpb.ClipInfo{}
		applyArtifactStorageStateToClip(clip, nil)
		if clip.StorageLocation != nil {
			t.Fatalf("nil state mutated clip: %v", clip.StorageLocation)
		}
	})

	t.Run("explicit storage location wins", func(t *testing.T) {
		clip := &sharedpb.ClipInfo{}
		applyArtifactStorageStateToClip(clip, &periscopepb.ArtifactState{
			StorageLocation: aoStr("s3"),
			FilePath:        aoStr("/data/x.mkv"),
			SyncStatus:      aoStr("synced"),
			HasLocalCopy:    aoBool(false),
			IsSynced:        aoBool(true),
		})
		if clip.GetStorageLocation() != "s3" {
			t.Errorf("StorageLocation = %q, want s3", clip.GetStorageLocation())
		}
		if clip.GetSyncStatus() != "synced" || !clip.GetIsSynced() {
			t.Errorf("sync flags not propagated: status=%q synced=%v", clip.GetSyncStatus(), clip.GetIsSynced())
		}
	})

	t.Run("falls back to local when only file path is present", func(t *testing.T) {
		clip := &sharedpb.ClipInfo{}
		applyArtifactStorageStateToClip(clip, &periscopepb.ArtifactState{
			FilePath: aoStr("/data/x.mkv"),
		})
		if clip.GetStorageLocation() != "local" {
			t.Errorf("StorageLocation = %q, want local fallback", clip.GetStorageLocation())
		}
	})

	t.Run("no location and no file path leaves location unset", func(t *testing.T) {
		clip := &sharedpb.ClipInfo{}
		applyArtifactStorageStateToClip(clip, &periscopepb.ArtifactState{
			HasLocalCopy: aoBool(true),
		})
		if clip.StorageLocation != nil {
			t.Errorf("StorageLocation = %v, want nil (unknown)", clip.StorageLocation)
		}
		if !clip.GetHasLocalCopy() {
			t.Errorf("has_local_copy not propagated")
		}
	})
}
