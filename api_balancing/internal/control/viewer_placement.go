package control

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// ViewerPlacementRequest names exactly one signed object: StreamID for a live
// stream in any ingest mode, ArtifactHash for stored media. InternalName is the
// object's own routing name without a Mist runtime prefix. Stored media is named
// by hash because the artifact's authority identifier is a control-plane row id
// the media cell never receives; the resolver binds hash, internal name and
// playback id together instead of trusting any one of them alone.
type ViewerPlacementRequest struct {
	TenantID, StreamID, ArtifactHash, InternalName, PlaybackID, Protocol string
	ArtifactID                                                           string
	Location                                                             *placement.Coordinates
}

type ViewerPlacementPreparer interface {
	PrepareViewer(context.Context, ViewerPlacementRequest) (balancer.PlacementPreparationResult, error)
}

// ViewerPlacementDestination is one destination the serve policy currently
// permits. It is a policy answer only: it reserves nothing and starts no media.
type ViewerPlacementDestination struct {
	ClusterID, NodeID string
}

// ViewerPlacementPermission carries the destinations a policy evaluation
// permits, for lanes that rank candidates on their own evidence (stored media
// ranks warm copies). The consuming lane must still let final admission decide.
type ViewerPlacementPermission struct {
	ObjectID, SourceGeneration string
	Destinations               []ViewerPlacementDestination
	ExpiresAt                  time.Time
}

// Permits reports whether a destination survived policy evaluation.
func (permission ViewerPlacementPermission) Permits(clusterID, nodeID string) bool {
	for _, destination := range permission.Destinations {
		if destination.ClusterID == clusterID && destination.NodeID == nodeID {
			return true
		}
	}
	return false
}

// ViewerPlacementPermitter evaluates serve policy without preparing a
// destination, so a lane with its own physical ranking can drop the
// destinations policy refuses instead of handing out an endpoint that final
// admission will reject.
type ViewerPlacementPermitter interface {
	PermitViewer(context.Context, ViewerPlacementRequest) (ViewerPlacementPermission, error)
}

var ErrInvalidViewerProtocol = errors.New("invalid viewer placement protocol")

// ViewerPlacementLiveObjectID maps a live viewer request to its signed authority
// id. A request that also names stored media is refused rather than defaulted, so
// a live stream can never be evaluated under an artifact's policy.
func ViewerPlacementLiveObjectID(request ViewerPlacementRequest) (string, error) {
	if request.StreamID == "" || request.ArtifactHash != "" {
		return "", errors.New("live viewer placement requires exactly one signed stream identity")
	}
	return sharedauthority.LiveStreamAuthorityID(request.StreamID), nil
}

// ViewerPlacementStoredMedia reports whether a request names stored media rather
// than a live stream, which the resolver must then bind by internal name.
func ViewerPlacementStoredMedia(request ViewerPlacementRequest) (bool, error) {
	switch {
	case request.StreamID != "" && request.ArtifactHash == "":
		return false, nil
	case request.ArtifactHash != "" && request.StreamID == "":
		return true, nil
	default:
		return false, errors.New("viewer placement requires exactly one signed object identity")
	}
}

// ResolvePreparedLiveViewerEndpoint selects and prepares one serving node. An
// omitted protocol requires any browser-playable Mist output; the selected
// MistServer's complete advertised output catalog remains authoritative.
func ResolvePreparedLiveViewerEndpoint(ctx context.Context, preparer ViewerPlacementPreparer, request ViewerPlacementRequest, activeIngestClusterID string) (*sharedpb.ViewerEndpointResponse, error) {
	objectID, err := ViewerPlacementLiveObjectID(request)
	if err != nil || request.ArtifactID != "" || strings.HasPrefix(request.InternalName, "dvr+") {
		return nil, errors.New("live viewer requires its own signed stream identity")
	}
	return resolvePreparedViewerEndpoint(ctx, preparer, request, objectID, activeIngestClusterID)
}

// ResolvePreparedDVRViewerEndpoint keeps the recording's authority and playback
// identity distinct from the parent stream used to locate its source.
func ResolvePreparedDVRViewerEndpoint(ctx context.Context, preparer ViewerPlacementPreparer, resolution *ContentResolution, protocol string, location *placement.Coordinates) (*sharedpb.ViewerEndpointResponse, error) {
	if resolution == nil || resolution.ContentType != "dvr" || resolution.ArtifactID == "" || resolution.ArtifactHash == "" {
		return nil, errors.New("DVR viewer requires signed recording identity")
	}
	request := ViewerPlacementRequest{TenantID: resolution.TenantId, ArtifactID: resolution.ArtifactID, ArtifactHash: resolution.ArtifactHash,
		InternalName: resolution.InternalName, PlaybackID: resolution.ContentId, Protocol: protocol, Location: location}
	resp, err := resolvePreparedViewerEndpoint(ctx, preparer, request, sharedauthority.ArtifactAuthorityID(resolution.ArtifactID), resolution.OriginClusterID)
	if err != nil {
		return nil, err
	}
	resp.Metadata.ContentType, resp.Metadata.Status, resp.Metadata.DvrStatus = "dvr", "recording", "recording"
	resp.Metadata.StreamId = &resolution.StreamId
	resp.Metadata.ThumbnailAssets = buildThumbnailAssets(resolveThumbnailChandlerBase(resolution.OriginClusterID), resolution.StreamId)
	return resp, nil
}

func resolvePreparedViewerEndpoint(ctx context.Context, preparer ViewerPlacementPreparer, request ViewerPlacementRequest, objectID, activeIngestClusterID string) (*sharedpb.ViewerEndpointResponse, error) {
	if preparer == nil {
		return nil, errors.New("viewer placement is required but unavailable")
	}
	// Runtime prefixes are not part of the signed object's routing name.
	request.InternalName = mist.ExtractInternalName(request.InternalName)
	if request.InternalName == "" || strings.Contains(request.InternalName, "+") || request.PlaybackID == "" || request.TenantID == "" {
		return nil, errors.New("viewer placement requires exact live stream identity")
	}
	if request.Protocol != "" {
		protocol := mist.PlaybackProtocol(request.Protocol)
		if protocol == "" {
			return nil, ErrInvalidViewerProtocol
		}
		request.Protocol = protocol
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	protocols := []string{mist.AutoPlaybackProtocol}
	if request.Protocol != "" {
		protocols = []string{request.Protocol}
	}
	for _, protocol := range protocols {
		request.Protocol = protocol
		prepared, err := preparer.PrepareViewer(ctx, request)
		if errors.Is(err, balancer.ErrPlacementUnavailable) && len(protocols) > 1 {
			continue
		}
		if err != nil {
			return nil, err
		}
		issued, issueErr := placement.PreparationIssuedAt(prepared.AttemptID)
		u, urlErr := url.Parse(prepared.Endpoint)
		now := time.Now()
		if issueErr != nil || urlErr != nil || u == nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
			prepared.Outcome != balancer.PlacementAccepted || prepared.TenantID != request.TenantID || prepared.ObjectID != objectID ||
			prepared.NodeID == "" || prepared.ClusterID == "" || prepared.SourceGeneration == "" || prepared.Protocol != protocol || !validPreparedViewerScheme(protocol, u.Scheme) ||
			!now.Before(prepared.ExpiresAt) || prepared.ExpiresAt.After(issued.Add(placement.PreparationLifetime)) || issued.After(now.Add(placement.PreparationClockSkew)) {
			return nil, errors.New("viewer preparation does not match the requested destination")
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		base, baseErr := url.Parse(prepared.PublicBaseURL)
		if baseErr != nil || base == nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
			return nil, errors.New("viewer preparation has no valid public base URL")
		}
		var rawOutputs map[string]any
		if len(prepared.OutputsJSON) > 1<<20 || json.Unmarshal([]byte(prepared.OutputsJSON), &rawOutputs) != nil || len(rawOutputs) == 0 {
			return nil, errors.New("viewer preparation has no valid Mist output advertisement")
		}
		outputs := BuildAdvertisedPlaybackOutputs(prepared.PublicBaseURL, rawOutputs, request.PlaybackID, true)
		if !bindPreparedEndpoint(prepared.Endpoint, protocol, outputs) {
			return nil, errors.New("viewer preparation endpoint is outside its Mist output advertisement")
		}
		protocolHints := make([]string, 0, len(outputs))
		for name := range outputs {
			if name != "MIST_HTML" && name != "PLAYER_JS" {
				protocolHints = append(protocolHints, name)
			}
		}
		sort.Strings(protocolHints)
		endpoint := &sharedpb.ViewerEndpoint{NodeId: prepared.NodeID, ClusterId: prepared.ClusterID, BaseUrl: prepared.PublicBaseURL, Protocol: protocol, Url: prepared.Endpoint,
			Outputs: outputs}
		metadata := &sharedpb.PlaybackMetadata{ContentId: request.PlaybackID, ContentType: "live", TenantId: request.TenantID, StreamId: &request.StreamID,
			Status: "live", IsLive: true, ProtocolHints: protocolHints, ThumbnailAssets: buildThumbnailAssets(resolveThumbnailChandlerBase(activeIngestClusterID), request.StreamID)}
		return &sharedpb.ViewerEndpointResponse{Primary: endpoint, Metadata: metadata}, nil
	}
	return nil, balancer.ErrPlacementUnavailable
}

func validPreparedViewerScheme(protocol, scheme string) bool {
	switch protocol {
	case mist.AutoPlaybackProtocol:
		return scheme == "http" || scheme == "https" || scheme == "ws" || scheme == "wss"
	case "webrtc", "wsmp4", "mews_webm", "h264_ws", "raw_ws", "json_ws":
		return scheme == "ws" || scheme == "wss"
	case "rtmp":
		return scheme == "rtmp" || scheme == "rtmps"
	case "rtsp", "srt", "dtsc":
		return scheme == protocol
	default:
		return scheme == "http" || scheme == "https"
	}
}

func bindPreparedEndpoint(endpoint, protocol string, outputs map[string]*sharedpb.OutputEndpoint) bool {
	preparedURL, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	preparedURL.RawQuery, preparedURL.ForceQuery = "", false
	for name, output := range outputs {
		if name == "PLAYER_JS" || (name == "MIST_HTML" && protocol != "mist_html") || output == nil {
			continue
		}
		advertisedURL, parseErr := url.Parse(output.Url)
		if parseErr != nil {
			continue
		}
		advertisedURL.RawQuery, advertisedURL.ForceQuery = "", false
		if advertisedURL.String() == preparedURL.String() {
			output.Url = endpoint
			return true
		}
	}
	return false
}

func ViewerPlacementLocation(lat, lon float64) *placement.Coordinates {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return nil
	}
	return &placement.Coordinates{Latitude: lat, Longitude: lon}
}
