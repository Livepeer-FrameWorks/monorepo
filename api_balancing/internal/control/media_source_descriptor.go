package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	localauthority "frameworks/api_balancing/internal/mediaauthority"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"google.golang.org/protobuf/proto"
)

// MediaSourceDescriptor binds a configured input or artifact to its signed
// identity. Generation identifies the media configuration, not a push session.
// SourceURI must stay inside the source's control cell.
type MediaSourceDescriptor struct {
	TenantID, ObjectID, InternalName, RuntimeName, PlaybackID string
	StreamID, Kind, Generation, OriginClusterID               string
	ArtifactHash, SourceURI                                   string
	AllowedClusters                                           []string
	PrivateSource                                             bool
	NativeAlwaysOn                                            bool
	Revision                                                  int64
}

type MediaSourceSecretReader interface {
	OpenLiveStreamSecret(localauthority.MediaObjectSnapshot) (*mediapb.LiveStreamSecret, error)
}

func DescribeMediaSource(pair localauthority.PlacementPair, secrets MediaSourceSecretReader) (MediaSourceDescriptor, error) {
	object := pair.Object.Authority
	if object == nil || pair.Tenant.Authority == nil || object.GetTenantId() != pair.Tenant.Authority.GetTenantId() || pair.Object.Version <= 0 {
		return MediaSourceDescriptor{}, errors.New("source authority identity is unavailable")
	}
	d := MediaSourceDescriptor{TenantID: object.GetTenantId(), ObjectID: pair.Object.AuthorityID,
		InternalName: object.GetInternalName(), PlaybackID: object.GetPlaybackId(), OriginClusterID: object.GetOriginClusterId(), Revision: pair.Object.Version}
	var configuration []byte
	switch object.GetObjectKind() {
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		live := object.GetLiveStream()
		d.Kind, d.StreamID = live.GetIngestMode(), live.GetStreamId()
		if (d.Kind != "pull" && d.Kind != "mist_native") || secrets == nil || !pair.Tenant.SourceReady || !pair.Object.SourceReady {
			return MediaSourceDescriptor{}, errors.New("configured source authority is not ready")
		}
		secret, err := secrets.OpenLiveStreamSecret(pair.Object)
		if err != nil {
			return MediaSourceDescriptor{}, err
		}
		if secret.GetTenantId() != d.TenantID || secret.GetAuthorityId() != d.ObjectID {
			return MediaSourceDescriptor{}, errors.New("source secret identity differs")
		}
		if d.Kind == "pull" {
			if !secret.GetSourceEnabled() || secret.GetSourceUri() == "" {
				return MediaSourceDescriptor{}, errors.New("pull source is disabled")
			}
			d.SourceURI = secret.GetSourceUri()
			d.AllowedClusters = slices.Clone(secret.GetAllowedClusterIds())
			class, classErr := pullsource.Classify(d.SourceURI)
			if classErr != nil || class == pullsource.ClassBlocked {
				return MediaSourceDescriptor{}, errors.New("pull source URI is not permitted")
			}
			d.PrivateSource = class == pullsource.ClassPrivate
			d.RuntimeName = RuntimeNameFor(IngestPull, d.InternalName)
		} else {
			if secret.GetNativeSourceSpec() == "" || len(secret.GetNativeAllowedClusterIds()) != 1 || secret.GetNativePlacementCount() != 1 {
				return MediaSourceDescriptor{}, errors.New("native source election is invalid")
			}
			d.SourceURI = secret.GetNativeSourceSpec()
			d.AllowedClusters = slices.Clone(secret.GetNativeAllowedClusterIds())
			d.NativeAlwaysOn = secret.GetNativeAlwaysOn()
			d.RuntimeName = RuntimeNameFor(IngestMistNative, d.InternalName)
		}
		// Restream targets and sealing-key rotation do not replace this input.
		input := &mediapb.LiveStreamSecret{SourceUri: secret.GetSourceUri(), SourceEnabled: secret.GetSourceEnabled(),
			AllowedClusterIds: slices.Clone(secret.GetAllowedClusterIds()), NativeSourceSpec: secret.GetNativeSourceSpec(),
			NativeSourceKind: secret.GetNativeSourceKind(), NativeAllowedClusterIds: slices.Clone(secret.GetNativeAllowedClusterIds()),
			NativePlacementCount: secret.GetNativePlacementCount(), NativeAlwaysOn: secret.GetNativeAlwaysOn()}
		slices.Sort(input.AllowedClusterIds)
		slices.Sort(input.NativeAllowedClusterIds)
		configuration, err = proto.MarshalOptions{Deterministic: true}.Marshal(input)
		if err != nil {
			return MediaSourceDescriptor{}, err
		}
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		artifact := object.GetArtifact()
		d.Kind, d.ArtifactHash, d.StreamID = "artifact", artifact.GetArtifactHash(), artifact.GetParentStreamId()
		if d.ArtifactHash == "" || artifact.GetArtifactId() == "" {
			return MediaSourceDescriptor{}, errors.New("artifact source identity is unavailable")
		}
		d.RuntimeName = "vod+" + d.InternalName
		if artifact.GetArtifactKind() == mediapb.ArtifactKind_ARTIFACT_KIND_DVR {
			d.Kind = "dvr"
			d.RuntimeName = "dvr+" + mist.ExtractInternalName(d.InternalName)
		}
		configuration = []byte(d.Kind + "\x00" + d.ArtifactHash)
	default:
		return MediaSourceDescriptor{}, errors.New("unsupported media source kind")
	}
	if d.ObjectID == "" || d.InternalName == "" || strings.Contains(d.InternalName, "+") {
		return MediaSourceDescriptor{}, errors.New("source routing identity is invalid")
	}
	digest := sha256.Sum256(append([]byte(d.TenantID+"\x00"+d.ObjectID+"\x00"), configuration...))
	d.Generation = d.Kind + ":" + hex.EncodeToString(digest[:])
	return d, nil
}
