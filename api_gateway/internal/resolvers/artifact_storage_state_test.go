package resolvers

import (
	"testing"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func aoStr(s string) *string { return &s }
func aoBool(b bool) *bool    { return &b }

// applyArtifactStorageState* projects a Periscope ArtifactState onto the
// client-facing Clip/DVR. The load-bearing rule: when no explicit
// StorageLocation is known but a local FilePath exists, surface "local" rather
// than leaving the field blank (otherwise the UI cannot tell a hot copy from an
// unknown one). nil inputs must be a no-op, never a panic.
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
			IsHot:           aoBool(false),
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
			IsHot: aoBool(true),
		})
		if clip.StorageLocation != nil {
			t.Errorf("StorageLocation = %v, want nil (unknown)", clip.StorageLocation)
		}
		if !clip.GetIsHot() {
			t.Errorf("IsHot not propagated")
		}
	})
}

func TestApplyArtifactStorageStateToDVR(t *testing.T) {
	t.Run("s3 url and local fallback both applied", func(t *testing.T) {
		dvr := &sharedpb.DVRInfo{}
		applyArtifactStorageStateToDVR(dvr, &periscopepb.ArtifactState{
			S3Url:    aoStr("https://s3/x"),
			FilePath: aoStr("/data/x.mkv"),
			IsFrozen: aoBool(true),
		})
		if dvr.GetS3Url() != "https://s3/x" {
			t.Errorf("S3Url = %q, want propagated", dvr.GetS3Url())
		}
		if dvr.GetStorageLocation() != "local" {
			t.Errorf("StorageLocation = %q, want local fallback", dvr.GetStorageLocation())
		}
		if !dvr.GetIsFrozen() {
			t.Errorf("IsFrozen not propagated")
		}
	})

	t.Run("nil inputs are no-ops", func(t *testing.T) {
		applyArtifactStorageStateToDVR(nil, &periscopepb.ArtifactState{})
		dvr := &sharedpb.DVRInfo{}
		applyArtifactStorageStateToDVR(dvr, nil)
		if dvr.StorageLocation != nil || dvr.S3Url != nil {
			t.Fatalf("nil state mutated dvr")
		}
	})
}
