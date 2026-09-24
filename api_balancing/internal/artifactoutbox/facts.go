package artifactoutbox

import (
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

// The public Artifact carries only identifiers a tenant sees through the API.
// artifact_id is the stable key. playback_id stays empty: a playback ID can be
// rotated, and Foghorn has no current value at most emit sites, so receivers
// look it up by artifact_id.

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
