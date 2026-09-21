package artifactoutbox

import (
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

// The public Artifact carries only identifiers a tenant sees through the API.
// Foghorn holds no playback IDs for clips, recordings, or uploads (Commodore
// owns them), so playback_id stays empty on every Foghorn artifact event.

// ClipArtifact identifies a clip by its hash and source stream.
func ClipArtifact(hash, streamID string) *publicv1.Artifact {
	return &publicv1.Artifact{ArtifactId: hash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID}
}

// RecordingArtifact identifies a DVR recording by its hash and source stream.
func RecordingArtifact(hash, streamID string) *publicv1.Artifact {
	return &publicv1.Artifact{ArtifactId: hash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_RECORDING, StreamId: streamID}
}

// UploadArtifact identifies an upload by its hash; uploads have no stream.
func UploadArtifact(hash string) *publicv1.Artifact {
	return &publicv1.Artifact{ArtifactId: hash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_UPLOAD}
}
