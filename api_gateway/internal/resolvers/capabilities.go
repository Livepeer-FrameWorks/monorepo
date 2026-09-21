package resolvers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/capabilities"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/platformfeatures"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/99designs/gqlgen/graphql"
)

// retentionReading is the tier cap on customer-set recording retention, as
// Purser reports it. Zero days means the tier sets no cap.
type retentionReading struct {
	days int32
}

// capabilitySections holds one cache per capabilities source. It is created on
// first use so a Resolver assembled without NewResolver still serves the query.
// Only the capabilities query reads these caches; no enforcement path does.
type capabilitySections struct {
	once sync.Once
	// now is the clock the caches age their readings against. Nil means the
	// wall clock.
	now        func() time.Time
	retention  *capabilities.SectionCache[retentionReading]
	processing *capabilities.SectionCache[bool]
	tenant     *capabilities.SectionCache[*quartermasterpb.GetTenantClusterCapabilitiesResponse]
}

func (s *capabilitySections) init() {
	s.once.Do(func() {
		now := s.now
		if now == nil {
			now = time.Now
		}
		s.retention = capabilities.NewSectionCacheWithClock[retentionReading](now)
		s.processing = capabilities.NewSectionCacheWithClock[bool](now)
		s.tenant = capabilities.NewSectionCacheWithClock[*quartermasterpb.GetTenantClusterCapabilitiesResponse](now)
	})
}

// CapabilitiesReading is what the gateway could read about the caller's
// enforced gates. A nil section with its error set means that section's source
// was unavailable and no earlier reading was cached, so no value is invented.
type CapabilitiesReading struct {
	Tenant      *model.TenantCapabilities
	TenantErr   error
	Clusters    []*quartermasterpb.TenantClusterCapability
	ClustersErr error
	// ObservedAt is when the oldest contributing section was read from its
	// source.
	ObservedAt time.Time
}

// ServerInfo reports the platform release and the product features this build
// ships. Static, public, and free of commit, build, cluster, or region detail.
func (r *Resolver) ServerInfo() *model.ServerInfo {
	return &model.ServerInfo{
		Version:  version.Version,
		Features: platformfeatures.Shipped(),
	}
}

// ReadCapabilities gathers the enforced gates for the calling tenant from the
// services that enforce them: the platform operator grant from the caller's own
// identity, the retention cap and the processing-override flag from Purser, and
// domain plus per-cluster media capabilities from Quartermaster. The caller
// must be allowed to read the tenant's media placement, as Quartermaster
// requires; otherwise an Unauthenticated or PermissionDenied status error is
// returned before any section is read. Each source is cached per tenant for
// capabilities.SectionTTL; when one is unavailable the last reading is served
// with its original observation time, and without one the corresponding
// section is left nil with its error set.
func (r *Resolver) ReadCapabilities(ctx context.Context) (*CapabilitiesReading, error) {
	if middleware.IsDemoMode(ctx) {
		synthetic := demo.GenerateCapabilities()
		return &CapabilitiesReading{
			Tenant:     synthetic.Tenant,
			Clusters:   synthetic.Clusters,
			ObservedAt: synthetic.ObservedAt,
		}, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}
	// The sections are cached per tenant, not per caller, so the caller must
	// pass Quartermaster's placement reader rule here: a cached section must
	// never answer a caller the source would refuse.
	if err := placementAuthorization(ctx, false); err != nil {
		return nil, err
	}
	r.capabilityCache.init()

	retention, retentionErr := r.capabilityCache.retention.Get(ctx, tenantID, func(ctx context.Context) (retentionReading, error) {
		status, err := r.Clients.Purser.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			return retentionReading{}, err
		}
		return retentionReading{days: status.GetRecordingRetentionDays()}, nil
	})
	processing, processingErr := r.capabilityCache.processing.Get(ctx, tenantID, func(ctx context.Context) (bool, error) {
		return r.tierProcessingCustomizable(ctx, tenantID)
	})
	tenantCaps, tenantCapsErr := r.capabilityCache.tenant.Get(ctx, tenantID, func(ctx context.Context) (*quartermasterpb.GetTenantClusterCapabilitiesResponse, error) {
		return r.Clients.Quartermaster.GetTenantClusterCapabilities(ctx, tenantID)
	})

	reading := &CapabilitiesReading{}
	var observed []time.Time

	if tenantCapsErr != nil {
		reading.ClustersErr = tenantCapsErr
	} else {
		clusters := tenantCaps.Value.GetClusters()
		if clusters == nil {
			clusters = []*quartermasterpb.TenantClusterCapability{}
		}
		reading.Clusters = clusters
		observed = append(observed, tenantCaps.ObservedAt)
	}

	switch {
	case retentionErr != nil:
		reading.TenantErr = retentionErr
	case processingErr != nil:
		reading.TenantErr = processingErr
	case tenantCapsErr != nil:
		reading.TenantErr = tenantCapsErr
	default:
		reading.Tenant = &model.TenantCapabilities{
			PlatformOperator:       r.RequirePlatformOperator(ctx) == nil,
			RecordingRetention:     retentionCapModel(retention.Value),
			ProcessingCustomizable: processing.Value,
			CustomSubdomain:        tenantCaps.Value.GetCustomSubdomain(),
			CustomDomain:           tenantCaps.Value.GetCustomDomain(),
		}
		observed = append(observed, retention.ObservedAt, processing.ObservedAt)
	}

	reading.ObservedAt = oldestObservation(observed)
	return reading, nil
}

// DoGetCapabilities shapes a reading for GraphQL. A section whose source failed
// without a cached reading resolves to null and adds a field error, so a client
// can tell "unknown" from "not allowed".
func (r *Resolver) DoGetCapabilities(ctx context.Context) (*model.Capabilities, error) {
	reading, err := r.ReadCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	if reading.TenantErr != nil {
		r.Logger.WithError(reading.TenantErr).Warn("Tenant capabilities unavailable")
		graphql.AddError(ctx, fmt.Errorf("tenant capabilities unavailable: %w", reading.TenantErr))
	}
	if reading.ClustersErr != nil {
		r.Logger.WithError(reading.ClustersErr).Warn("Cluster capabilities unavailable")
		graphql.AddError(ctx, fmt.Errorf("cluster capabilities unavailable: %w", reading.ClustersErr))
	}
	return &model.Capabilities{
		Tenant:     reading.Tenant,
		Clusters:   reading.Clusters,
		ObservedAt: reading.ObservedAt,
	}, nil
}

// tierProcessingCustomizable reads the flag through the same path Commodore
// enforces with: the tenant's subscription, then that subscription's tier
// features. The subscription's own custom features are deliberately not
// consulted — Commodore reads only the tier.
func (r *Resolver) tierProcessingCustomizable(ctx context.Context, tenantID string) (bool, error) {
	subscription, err := r.Clients.Purser.GetSubscription(ctx, tenantID)
	if err != nil {
		return false, err
	}
	tierID := subscription.GetSubscription().GetTierId()
	if tierID == "" {
		return false, nil
	}
	tier, err := r.Clients.Purser.GetBillingTier(ctx, tierID)
	if err != nil {
		return false, err
	}
	return tier.GetFeatures().GetProcessingCustomizable(), nil
}

func retentionCapModel(reading retentionReading) *model.RecordingRetentionCap {
	if reading.days <= 0 {
		return &model.RecordingRetentionCap{Capped: false}
	}
	maxDays := int(reading.days)
	return &model.RecordingRetentionCap{Capped: true, MaxDays: &maxDays}
}

// oldestObservation returns the earliest source reading time, which is how
// stale the whole response can be. With no section readable the response
// carries the current time alongside its errors.
func oldestObservation(times []time.Time) time.Time {
	oldest := time.Time{}
	for _, t := range times {
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return time.Now().UTC()
	}
	return oldest
}
