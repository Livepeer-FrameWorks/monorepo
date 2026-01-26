package x402

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"frameworks/pkg/globalid"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
)

const (
	ResourceKindViewer  = "viewer"
	ResourceKindStream  = "stream"
	ResourceKindClip    = "clip"
	ResourceKindDVR     = "dvr"
	ResourceKindVOD     = "vod"
	ResourceKindGraphQL = "graphql"
)

const (
	ErrInvalidPayment         = "invalid_payment"
	ErrAuthRequired           = "auth_required"
	ErrTargetMismatch         = "target_mismatch"
	ErrResourceNotFound       = "resource_not_found"
	ErrInvalidResource        = "invalid_resource"
	ErrResolverUnavailable    = "resolver_unavailable"
	ErrVerificationFailed     = "verification_failed"
	ErrBillingDetailsRequired = "billing_details_required"
	ErrAuthOnly               = "auth_only"
	ErrSettlementFailed       = "settlement_failed"
)

type SettlementError struct {
	Code         string
	Message      string
	ResourceType string
	ResourceID   string
}

func (e *SettlementError) Error() string {
	return e.Message
}

type PurserClient interface {
	VerifyX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error)
	SettleX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error)
}

type CommodoreClient interface {
	ResolvePlaybackID(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error)
	ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error)
	ResolveClipHash(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error)
	ResolveDVRHash(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error)
	ResolveIdentifier(ctx context.Context, identifier string) (*pb.ResolveIdentifierResponse, error)
	ResolveVodID(ctx context.Context, vodID string) (*pb.ResolveVodIDResponse, error)
	ValidateStreamKey(ctx context.Context, streamKey string) (*pb.ValidateStreamKeyResponse, error)
}

type ResourceResolution struct {
	Resource string
	Kind     string
	TenantID string
	Resolved bool
}

type SettlementOptions struct {
	PaymentHeader         string
	Payload               *pb.X402PaymentPayload
	Resource              string
	AuthTenantID          string
	ClientIP              string
	Purser                PurserClient
	Commodore             CommodoreClient
	AllowUnresolvedCreator bool
	Logger                logging.Logger
	Resolution            *ResourceResolution
}

type SettlementResult struct {
	TargetTenantID string
	Resource       string
	ResourceKind   string
	Verify         *pb.VerifyX402PaymentResponse
	Settle         *pb.SettleX402PaymentResponse
	PayerAddress   string
}

func IsAuthOnlyPayment(payload *pb.X402PaymentPayload) bool {
	if payload == nil || payload.Payload == nil || payload.Payload.Authorization == nil {
		return false
	}
	value := strings.TrimSpace(payload.Payload.Authorization.Value)
	if value == "" {
		return false
	}
	amount, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return false
	}
	return amount.Sign() == 0
}

func SettleX402Payment(ctx context.Context, opts SettlementOptions) (*SettlementResult, *SettlementError) {
	if opts.Purser == nil {
		return nil, &SettlementError{Code: ErrSettlementFailed, Message: "x402 settlement unavailable"}
	}

	payload := opts.Payload
	if payload == nil {
		if opts.PaymentHeader == "" {
			return nil, &SettlementError{Code: ErrInvalidPayment, Message: "payment is required"}
		}
		var err error
		payload, err = ParsePaymentHeader(opts.PaymentHeader)
		if err != nil {
			return nil, &SettlementError{Code: ErrInvalidPayment, Message: "invalid X-PAYMENT header"}
		}
	}

	if IsAuthOnlyPayment(payload) {
		return nil, &SettlementError{Code: ErrAuthOnly, Message: "auth-only payments cannot be used for settlement"}
	}

	resource := strings.TrimSpace(opts.Resource)
	resolution := opts.Resolution
	if resolution == nil && resource != "" {
		var err *SettlementError
		resolution, err = ResolveResource(ctx, resource, opts.Commodore)
		if err != nil {
			if opts.AllowUnresolvedCreator && opts.AuthTenantID != "" && err.Code == ErrResourceNotFound {
				resolution = &ResourceResolution{
					Resource: resource,
					Kind:     ResourceKindGraphQL,
					TenantID: "",
					Resolved: false,
				}
			} else {
				return nil, err
			}
		}
	}

	kind := ResourceKindGraphQL
	targetTenantID := ""
	if resolution != nil {
		kind = resolution.Kind
	}

	if resource == "" {
		if opts.AuthTenantID == "" {
			return nil, &SettlementError{Code: ErrAuthRequired, Message: "resource is required for non-zero payments when no authenticated tenant is present"}
		}
		targetTenantID = opts.AuthTenantID
	} else if kind == ResourceKindViewer {
		if resolution == nil || !resolution.Resolved || resolution.TenantID == "" {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: "playback resource not found", ResourceType: "Viewer", ResourceID: resource}
		}
		targetTenantID = resolution.TenantID
	} else {
		if opts.AuthTenantID == "" {
			return nil, &SettlementError{Code: ErrAuthRequired, Message: "authentication required for non-viewer payments"}
		}
		if resolution != nil && resolution.Resolved && resolution.TenantID != "" && resolution.TenantID != opts.AuthTenantID {
			return nil, &SettlementError{Code: ErrTargetMismatch, Message: "billable tenant does not match authenticated tenant"}
		}
		if resolution != nil && !resolution.Resolved && !opts.AllowUnresolvedCreator {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: "resource not found", ResourceType: resolutionKindLabel(kind), ResourceID: resource}
		}
		targetTenantID = opts.AuthTenantID
	}

	verifyCtx, cancelVerify := context.WithTimeout(ctx, 10*time.Second)
	defer cancelVerify()

	verifyResp, err := opts.Purser.VerifyX402Payment(verifyCtx, targetTenantID, payload, opts.ClientIP)
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.WithError(err).Warn("x402 payment verification failed")
		}
		return nil, &SettlementError{Code: ErrVerificationFailed, Message: "payment verification failed"}
	}
	if verifyResp == nil || !verifyResp.Valid {
		msg := "payment verification failed"
		if verifyResp != nil && verifyResp.Error != "" {
			msg = verifyResp.Error
		}
		return nil, &SettlementError{Code: ErrVerificationFailed, Message: msg}
	}
	if verifyResp.RequiresBillingDetails {
		return nil, &SettlementError{Code: ErrBillingDetailsRequired, Message: "billing details required for this payment"}
	}
	if verifyResp.IsAuthOnly {
		return nil, &SettlementError{Code: ErrAuthOnly, Message: "auth-only payments cannot be used for settlement"}
	}

	settleCtx, cancelSettle := context.WithTimeout(ctx, 30*time.Second)
	defer cancelSettle()

	settleResp, err := opts.Purser.SettleX402Payment(settleCtx, targetTenantID, payload, opts.ClientIP)
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.WithError(err).Warn("x402 payment settlement failed")
		}
		return nil, &SettlementError{Code: ErrSettlementFailed, Message: "payment settlement failed"}
	}
	if settleResp == nil || !settleResp.Success {
		msg := "payment settlement failed"
		if settleResp != nil && settleResp.Error != "" {
			msg = settleResp.Error
		}
		return nil, &SettlementError{Code: ErrSettlementFailed, Message: msg}
	}
	if settleResp.IsAuthOnly {
		return nil, &SettlementError{Code: ErrAuthOnly, Message: "auth-only payments cannot be used for settlement"}
	}

	payerAddress := ""
	if settleResp.PayerAddress != "" {
		payerAddress = settleResp.PayerAddress
	} else if verifyResp.PayerAddress != "" {
		payerAddress = verifyResp.PayerAddress
	} else if payload.Payload != nil && payload.Payload.Authorization != nil {
		payerAddress = payload.Payload.Authorization.From
	}

	return &SettlementResult{
		TargetTenantID: targetTenantID,
		Resource:       resource,
		ResourceKind:   kind,
		Verify:         verifyResp,
		Settle:         settleResp,
		PayerAddress:   payerAddress,
	}, nil
}

func ResolveResource(ctx context.Context, resource string, commodore CommodoreClient) (*ResourceResolution, *SettlementError) {
	raw := strings.TrimSpace(resource)
	if raw == "" {
		return nil, &SettlementError{Code: ErrInvalidResource, Message: "resource is required"}
	}

	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "mcp://") {
		raw = "graphql://" + strings.TrimSpace(raw[len("mcp://"):])
		lower = strings.ToLower(raw)
	}

	if strings.HasPrefix(lower, "graphql://") {
		return &ResourceResolution{
			Resource: "graphql://" + strings.TrimSpace(raw[len("graphql://"):]),
			Kind:     ResourceKindGraphQL,
			TenantID: "",
			Resolved: false,
		}, nil
	}

	if strings.HasPrefix(lower, "playback:") {
		raw = strings.TrimSpace(raw[len("playback:"):])
		lower = "viewer://"
	} else if strings.HasPrefix(lower, "viewer://") {
		raw = strings.TrimSpace(raw[len("viewer://"):])
		lower = "viewer://"
	} else if strings.HasPrefix(lower, "stream://") {
		raw = strings.TrimSpace(raw[len("stream://"):])
		lower = "stream://"
	} else if strings.HasPrefix(lower, "clip://") {
		raw = strings.TrimSpace(raw[len("clip://"):])
		lower = "clip://"
	} else if strings.HasPrefix(lower, "dvr://") {
		raw = strings.TrimSpace(raw[len("dvr://"):])
		lower = "dvr://"
	} else if strings.HasPrefix(lower, "vod://") {
		raw = strings.TrimSpace(raw[len("vod://"):])
		lower = "vod://"
	} else if strings.HasPrefix(lower, "ingest:") {
		key := strings.TrimSpace(raw[len("ingest:"):])
		if key == "" {
			return nil, &SettlementError{Code: ErrInvalidResource, Message: "ingest resource missing stream key"}
		}
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "ingest resolver unavailable"}
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := commodore.ValidateStreamKey(ctx, key)
		if err != nil {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("invalid stream key: %v", err), ResourceType: "Stream", ResourceID: key}
		}
		if resp == nil || !resp.Valid {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: "invalid stream key", ResourceType: "Stream", ResourceID: key}
		}
		return &ResourceResolution{
			Resource: "stream://" + strings.TrimSpace(resp.StreamId),
			Kind:     ResourceKindStream,
			TenantID: resp.TenantId,
			Resolved: resp.TenantId != "",
		}, nil
	} else if strings.HasPrefix(lower, "stream:") {
		raw = strings.TrimSpace(raw[len("stream:"):])
		lower = "stream://"
	} else if strings.HasPrefix(lower, "streams://") {
		raw = strings.TrimSpace(raw[len("streams://"):])
		lower = "stream://"
	} else if strings.HasPrefix(lower, "vod:") {
		raw = strings.TrimSpace(raw[len("vod:"):])
		lower = "vod://"
	}

	raw = strings.TrimSuffix(raw, "/health")

	switch {
	case strings.HasPrefix(lower, "viewer://"):
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "playback resolver unavailable"}
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := commodore.ResolveArtifactPlaybackID(ctx, raw); err == nil && resp != nil && resp.Found && resp.TenantId != "" {
			return &ResourceResolution{
				Resource: "viewer://" + raw,
				Kind:     ResourceKindViewer,
				TenantID: resp.TenantId,
				Resolved: true,
			}, nil
		}
		if resp, err := commodore.ResolvePlaybackID(ctx, raw); err == nil && resp != nil && resp.TenantId != "" {
			return &ResourceResolution{
				Resource: "viewer://" + raw,
				Kind:     ResourceKindViewer,
				TenantID: resp.TenantId,
				Resolved: true,
			}, nil
		}
		return nil, &SettlementError{Code: ErrResourceNotFound, Message: "playback resource not found", ResourceType: "Viewer", ResourceID: raw}

	case strings.HasPrefix(lower, "clip://"):
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "clip resolver unavailable"}
		}
		if typ, id, ok := globalid.Decode(raw); ok {
			if typ != globalid.TypeClip {
				return nil, &SettlementError{Code: ErrInvalidResource, Message: fmt.Sprintf("unsupported relay ID type: %s", typ)}
			}
			if _, err := uuid.Parse(id); err == nil {
				return nil, &SettlementError{Code: ErrInvalidResource, Message: "clip relay IDs are not supported for payment; use clip_hash"}
			}
			raw = id
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := commodore.ResolveClipHash(ctx, raw)
		if err != nil {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve clip hash: %v", err), ResourceType: "Clip", ResourceID: raw}
		}
		if resp != nil && resp.TenantId != "" {
			return &ResourceResolution{
				Resource: "clip://" + raw,
				Kind:     ResourceKindClip,
				TenantID: resp.TenantId,
				Resolved: true,
			}, nil
		}
		return nil, &SettlementError{Code: ErrResourceNotFound, Message: "clip resource not found", ResourceType: "Clip", ResourceID: raw}

	case strings.HasPrefix(lower, "dvr://"):
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "dvr resolver unavailable"}
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := commodore.ResolveDVRHash(ctx, raw)
		if err != nil {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve DVR hash: %v", err), ResourceType: "DVR", ResourceID: raw}
		}
		if resp != nil && resp.TenantId != "" {
			return &ResourceResolution{
				Resource: "dvr://" + raw,
				Kind:     ResourceKindDVR,
				TenantID: resp.TenantId,
				Resolved: true,
			}, nil
		}
		return nil, &SettlementError{Code: ErrResourceNotFound, Message: "DVR resource not found", ResourceType: "DVR", ResourceID: raw}

	case strings.HasPrefix(lower, "stream://"):
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "stream resolver unavailable"}
		}
		if typ, id, ok := globalid.Decode(raw); ok {
			if typ != globalid.TypeStream {
				return nil, &SettlementError{Code: ErrInvalidResource, Message: fmt.Sprintf("unsupported relay ID type: %s", typ)}
			}
			raw = id
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := commodore.ResolveIdentifier(ctx, raw)
		if err != nil {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve stream: %v", err), ResourceType: "Stream", ResourceID: raw}
		}
		if resp == nil || !resp.Found {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: "stream resource not found", ResourceType: "Stream", ResourceID: raw}
		}
		if resp.IdentifierType != "stream_id" {
			return nil, &SettlementError{Code: ErrInvalidResource, Message: "invalid stream identifier"}
		}
		return &ResourceResolution{
			Resource: "stream://" + raw,
			Kind:     ResourceKindStream,
			TenantID: resp.TenantId,
			Resolved: resp.TenantId != "",
		}, nil

	case strings.HasPrefix(lower, "vod://"):
		if commodore == nil {
			return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "VOD resolver unavailable"}
		}
		if typ, id, ok := globalid.Decode(raw); ok {
			if typ != globalid.TypeVodAsset {
				return nil, &SettlementError{Code: ErrInvalidResource, Message: fmt.Sprintf("unsupported relay ID type: %s", typ)}
			}
			raw = id
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := uuid.Parse(raw); err == nil {
			vodResp, err := commodore.ResolveVodID(ctx, raw)
			if err != nil {
				return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve VOD ID: %v", err), ResourceType: "VOD", ResourceID: raw}
			}
			if vodResp == nil || !vodResp.Found {
				return nil, &SettlementError{Code: ErrResourceNotFound, Message: "VOD asset not found", ResourceType: "VOD", ResourceID: raw}
			}
			return &ResourceResolution{
				Resource: "vod://" + raw,
				Kind:     ResourceKindVOD,
				TenantID: vodResp.TenantId,
				Resolved: vodResp.TenantId != "",
			}, nil
		}
		vodResp, err := commodore.ResolveIdentifier(ctx, raw)
		if err != nil {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve VOD hash: %v", err), ResourceType: "VOD", ResourceID: raw}
		}
		if vodResp == nil || !vodResp.Found {
			return nil, &SettlementError{Code: ErrResourceNotFound, Message: "VOD asset not found", ResourceType: "VOD", ResourceID: raw}
		}
		return &ResourceResolution{
			Resource: "vod://" + raw,
			Kind:     ResourceKindVOD,
			TenantID: vodResp.TenantId,
			Resolved: vodResp.TenantId != "",
		}, nil
	}

	if typ, id, ok := globalid.Decode(raw); ok {
		switch typ {
		case globalid.TypeStream:
			return ResolveResource(ctx, "stream://"+id, commodore)
		case globalid.TypeVodAsset:
			return ResolveResource(ctx, "vod://"+id, commodore)
		default:
			return nil, &SettlementError{Code: ErrInvalidResource, Message: fmt.Sprintf("unsupported relay ID type: %s", typ)}
		}
	}

	if commodore == nil {
		return nil, &SettlementError{Code: ErrResolverUnavailable, Message: "resource resolver unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := commodore.ResolveIdentifier(ctx, raw)
	if err != nil {
		return nil, &SettlementError{Code: ErrResourceNotFound, Message: fmt.Sprintf("failed to resolve resource: %v", err), ResourceType: "Resource", ResourceID: raw}
	}
	if resp != nil && resp.Found {
		if resp.IdentifierType == "stream" || strings.Contains(resp.IdentifierType, "internal_name") {
			return nil, &SettlementError{Code: ErrInvalidResource, Message: "internal routing identifiers are not accepted; use stream_id or artifact_hash"}
		}
		return &ResourceResolution{
			Resource: raw,
			Kind:     resp.IdentifierType,
			TenantID: resp.TenantId,
			Resolved: resp.TenantId != "",
		}, nil
	}

	streamResp, err := commodore.ValidateStreamKey(ctx, raw)
	if err == nil && streamResp != nil && streamResp.Valid {
		return &ResourceResolution{
			Resource: "stream://" + strings.TrimSpace(streamResp.StreamId),
			Kind:     ResourceKindStream,
			TenantID: streamResp.TenantId,
			Resolved: streamResp.TenantId != "",
		}, nil
	}

	return nil, &SettlementError{Code: ErrResourceNotFound, Message: "resource not found", ResourceType: "Resource", ResourceID: raw}
}

func resolutionKindLabel(kind string) string {
	switch kind {
	case ResourceKindViewer:
		return "Viewer"
	case ResourceKindStream:
		return "Stream"
	case ResourceKindClip:
		return "Clip"
	case ResourceKindDVR:
		return "DVR"
	case ResourceKindVOD:
		return "VOD"
	default:
		return "Resource"
	}
}
