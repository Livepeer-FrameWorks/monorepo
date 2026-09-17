package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"frameworks/api_control/internal/placementpolicy"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *CommodoreServer) GetMediaPlacementOptions(ctx context.Context, req *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
	scope, err := mediaPlacementScope(ctx, req.GetScope(), false)
	if err != nil {
		return nil, err
	}
	if validationErr := validatePlacementOptionsRequest(req); validationErr != nil {
		return nil, validationErr
	}
	if s.db == nil || s.authorityTenantSource == nil {
		return nil, status.Error(codes.Unavailable, "placement options owner is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Stream ownership is checked before requesting any infrastructure metadata.
	if _, readErr := placementpolicy.NewStore(s.db).Read(ctx, scope); readErr != nil {
		return nil, mediaPlacementError(readErr)
	}
	entitlement, err := s.authorityTenantSource.GetTenantEntitlement(ctx, scope.TenantID)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement entitlement is unavailable")
	}
	var ownedNodes map[string]string
	if kind := req.GetFilter().GetKind(); kind == placementpb.OptionKind_OPTION_KIND_UNSPECIFIED || kind == placementpb.OptionKind_OPTION_KIND_NODE {
		if ownedNodes, err = s.ownedPlacementNodes(ctx, scope.TenantID, entitlement); err != nil {
			return nil, err
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	return projectPlacementOptions(scope, req, entitlement, ownedNodes, time.Now())
}

func validatePlacementOptionsRequest(req *placementpb.GetOptionsRequest) error {
	if req == nil || req.GetScope() == nil || proto.Size(req) > 8192 || req.GetFirst() < 0 || req.GetFirst() > 100 || len(req.GetAfter()) > 2048 || len(req.GetFilter().GetQuery()) > 128 || len(req.GetFilter().GetClasses()) > 3 ||
		len(req.ProtoReflect().GetUnknown()) != 0 || len(req.GetScope().ProtoReflect().GetUnknown()) != 0 {
		return status.Error(codes.InvalidArgument, "invalid placement options bounds")
	}
	filter := req.GetFilter()
	if filter != nil && len(filter.ProtoReflect().GetUnknown()) != 0 || filter.GetKind() < placementpb.OptionKind_OPTION_KIND_UNSPECIFIED || filter.GetKind() > placementpb.OptionKind_OPTION_KIND_NODE {
		return status.Error(codes.InvalidArgument, "unsupported placement options filter")
	}
	if clusterID := filter.GetClusterId(); clusterID != "" && !placementOptionID(clusterID) {
		return status.Error(codes.InvalidArgument, "invalid placement options cluster filter")
	}
	for _, class := range filter.GetClasses() {
		if class < placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL || class > placementpb.ClusterClass_CLUSTER_CLASS_THIRD_PARTY_MARKETPLACE {
			return status.Error(codes.InvalidArgument, "unsupported placement class filter")
		}
	}
	return nil
}

type placementOptionsCursor struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Catalog string `json:"catalog"`
	After   string `json:"after"`
}

// ownedNodes maps node ID to cluster ID for clusters the consuming tenant owns.
// Node options are offered only for clusters private to that tenant, so
// platform and marketplace node identities never appear.
func projectPlacementOptions(scope placementpolicy.Scope, req *placementpb.GetOptionsRequest, entitlement *quartermasterpb.GetTenantEntitlementResponse, ownedNodes map[string]string, now time.Time) (*placementpb.Options, error) {
	if err := validatePlacementOptionsRequest(req); err != nil {
		return nil, err
	}
	if entitlement == nil || len(entitlement.GetAllowedClusterIds()) > 4096 || len(entitlement.GetEffectiveAccess()) > 4096 {
		return nil, status.Error(codes.Unavailable, "placement entitlement is incomplete")
	}
	allowed := make(map[string]bool, len(entitlement.GetAllowedClusterIds()))
	for _, id := range entitlement.GetAllowedClusterIds() {
		if !placementOptionID(id) || allowed[id] {
			return nil, status.Error(codes.Unavailable, "placement entitlement is inconsistent")
		}
		allowed[id] = true
	}
	filter := proto.CloneOf(req.GetFilter())
	if filter == nil {
		filter = &placementpb.OptionsFilter{}
	}
	filter.Query = strings.ToLower(strings.TrimSpace(filter.GetQuery()))
	slices.Sort(filter.Classes)
	filter.Classes = slices.Compact(filter.Classes)
	options := map[string]*placementpb.Option{}
	seen := make(map[string]bool, len(allowed))
	for _, peer := range entitlement.GetEffectiveAccess() {
		if peer == nil || !allowed[peer.GetClusterId()] || seen[peer.GetClusterId()] {
			return nil, status.Error(codes.Unavailable, "placement entitlement census differs")
		}
		seen[peer.GetClusterId()] = true
		if !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" || peer.GetAccessSource() < clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER || peer.GetAccessSource() > clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE {
			return nil, status.Error(codes.Unavailable, "placement entitlement permission is inconsistent")
		}
		if expiry := peer.GetAccessExpiresAt(); expiry != nil && (!expiry.IsValid() || !now.Before(expiry.AsTime())) {
			return nil, status.Error(codes.Unavailable, "placement entitlement expired")
		}
		if peer.GetClusterType() != "edge" {
			continue
		}
		// The census above still covers every entitled cluster; the cluster
		// filter only narrows which clusters contribute options.
		if filter.GetClusterId() != "" && peer.GetClusterId() != filter.GetClusterId() {
			continue
		}
		class, err := placementOptionClass(scope.TenantID, peer)
		if err != nil {
			return nil, err
		}
		if len(filter.GetClasses()) != 0 && !slices.Contains(filter.GetClasses(), class) {
			continue
		}
		if peer.GetOwnerTenantId() != "" && !placementOptionID(peer.GetOwnerTenantId()) || peer.GetRegionId() != "" && !placementOptionID(peer.GetRegionId()) || len(peer.GetClusterName()) > 255 {
			return nil, status.Error(codes.Unavailable, "placement option identity is invalid")
		}
		eligible, reason := true, ""
		consent := peer.GetMediaConsent()
		switch {
		case consent == nil || consent.GetRevision() > math.MaxInt64:
			eligible, reason = false, "owner_consent_unknown"
		case !consent.GetAllowIngest() && !consent.GetAllowServe():
			eligible, reason = false, "owner_denies_media"
		}
		name := peer.GetClusterName()
		if name == "" {
			name = peer.GetClusterId()
		}
		addPlacementOption(options, &placementpb.Option{Id: peer.GetClusterId(), Name: name, Kind: placementpb.OptionKind_OPTION_KIND_CLUSTER, ClusterClass: class, Region: peer.GetRegionId(), OwnerId: peer.GetOwnerTenantId(), Eligible: eligible, Reason: reason})
		if owner := peer.GetOwnerTenantId(); owner != "" {
			label := "Operator " + owner
			if owner == scope.TenantID {
				label = "Your clusters"
			}
			addPlacementOption(options, &placementpb.Option{Id: owner, Name: label, Kind: placementpb.OptionKind_OPTION_KIND_OPERATOR, ClusterClass: class, OwnerId: owner, Eligible: eligible, Reason: reason})
		}
		if region := peer.GetRegionId(); region != "" {
			addPlacementOption(options, &placementpb.Option{Id: region, Name: region, Kind: placementpb.OptionKind_OPTION_KIND_REGION, ClusterClass: class, Region: region, Eligible: eligible, Reason: reason})
		}
		if class != placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE {
			continue
		}
		for nodeID, clusterID := range ownedNodes {
			if clusterID != peer.GetClusterId() {
				continue
			}
			if !placementOptionID(nodeID) {
				return nil, status.Error(codes.Unavailable, "placement option identity is invalid")
			}
			addPlacementOption(options, &placementpb.Option{Id: nodeID, Name: nodeID, Kind: placementpb.OptionKind_OPTION_KIND_NODE, ClusterClass: class, Region: peer.GetRegionId(), OwnerId: peer.GetOwnerTenantId(), ClusterId: clusterID, Eligible: eligible, Reason: reason})
		}
	}
	if len(seen) != len(allowed) {
		return nil, status.Error(codes.Unavailable, "placement entitlement census is incomplete")
	}
	nodes := make([]*placementpb.Option, 0, len(options))
	for _, option := range options {
		if filter.GetKind() != placementpb.OptionKind_OPTION_KIND_UNSPECIFIED && option.GetKind() != filter.GetKind() {
			continue
		}
		text := strings.ToLower(option.GetId() + " " + option.GetName() + " " + option.GetRegion() + " " + option.GetOwnerId() + " " + option.GetClusterId())
		if filter.GetQuery() != "" && !strings.Contains(text, filter.GetQuery()) {
			continue
		}
		nodes = append(nodes, option)
	}
	slices.SortFunc(nodes, func(a, b *placementpb.Option) int {
		return strings.Compare(placementOptionKey(a), placementOptionKey(b))
	})
	return paginatePlacementOptions(scope, req, filter, nodes)
}

func placementOptionID(value string) bool {
	return value != "" && len(value) <= 100 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func placementOptionClass(tenantID string, peer *clusterpb.TenantClusterPeer) (placementpb.ClusterClass, error) {
	switch peer.GetClusterClass() {
	case "platform_official":
		return placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL, nil
	case "tenant_private", "third_party_marketplace":
		if peer.GetOwnerTenantId() == tenantID {
			return placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE, nil
		}
		if peer.GetOwnerTenantId() != "" {
			return placementpb.ClusterClass_CLUSTER_CLASS_THIRD_PARTY_MARKETPLACE, nil
		}
	}
	return 0, status.Error(codes.Unavailable, "placement ownership classification is unknown")
}

func addPlacementOption(options map[string]*placementpb.Option, next *placementpb.Option) {
	key := placementOptionKey(next)
	previous := options[key]
	if previous == nil {
		options[key] = next
		return
	}
	if previous.GetClusterClass() != next.GetClusterClass() {
		previous.ClusterClass = placementpb.ClusterClass_CLUSTER_CLASS_UNSPECIFIED
	}
	previous.Eligible = previous.GetEligible() || next.GetEligible()
	if previous.GetEligible() {
		previous.Reason = ""
	} else if previous.GetReason() != next.GetReason() {
		previous.Reason = "no_eligible_media_target"
	}
}

func placementOptionKey(option *placementpb.Option) string {
	return option.GetKind().String() + ":" + option.GetId()
}

func paginatePlacementOptions(scope placementpolicy.Scope, req *placementpb.GetOptionsRequest, filter *placementpb.OptionsFilter, nodes []*placementpb.Option) (*placementpb.Options, error) {
	bound, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&placementpb.GetOptionsRequest{Scope: req.GetScope(), Filter: filter})
	if err != nil {
		return nil, status.Error(codes.Internal, "placement filter encoding failed")
	}
	scopeHash := sha256.Sum256(append([]byte("placement-options/v1\x00"+scope.TenantID+"\x00"+scope.Kind+"\x00"+scope.ID+"\x00"), bound...))
	catalog, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&placementpb.Options{Nodes: nodes})
	if err != nil {
		return nil, status.Error(codes.Internal, "placement catalogue encoding failed")
	}
	catalogHash := sha256.Sum256(catalog)
	binding := placementOptionsCursor{Version: 1, Scope: hex.EncodeToString(scopeHash[:]), Catalog: hex.EncodeToString(catalogHash[:])}
	start := 0
	if req.GetAfter() != "" {
		encoded, decodeErr := base64.RawURLEncoding.DecodeString(req.GetAfter())
		if decodeErr != nil || base64.RawURLEncoding.EncodeToString(encoded) != req.GetAfter() {
			return nil, status.Error(codes.InvalidArgument, "invalid placement options cursor")
		}
		var cursor placementOptionsCursor
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || cursor.Version != 1 || cursor.Scope != binding.Scope {
			return nil, status.Error(codes.InvalidArgument, "placement options cursor does not match scope or filter")
		}
		if cursor.Catalog != binding.Catalog {
			return nil, status.Error(codes.Aborted, "placement catalogue changed; restart pagination")
		}
		index := slices.IndexFunc(nodes, func(option *placementpb.Option) bool { return placementOptionKey(option) == cursor.After })
		if index < 0 {
			return nil, status.Error(codes.InvalidArgument, "placement options cursor position is invalid")
		}
		start = index + 1
	}
	first := int(req.GetFirst())
	if first == 0 {
		first = 50
	}
	end := min(start+first, len(nodes))
	out := &placementpb.Options{Scope: proto.CloneOf(req.GetScope()), Nodes: nodes[start:end], HasPreviousPage: start > 0, HasNextPage: end < len(nodes)}
	encode := func(option *placementpb.Option) (string, error) {
		cursor := binding
		cursor.After = placementOptionKey(option)
		body, marshalErr := json.Marshal(cursor)
		return base64.RawURLEncoding.EncodeToString(body), marshalErr
	}
	if len(out.Nodes) != 0 {
		out.StartCursor, err = encode(out.Nodes[0])
		if err == nil {
			out.EndCursor, err = encode(out.Nodes[len(out.Nodes)-1])
		}
	}
	return out, err
}
