package resolvers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/graph/markers"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Platform operator admin surface (Query.platform). The gateway is the policy
// enforcement point: every resolver here gates on the platform operator and
// audit-logs the read, then calls downstream under the SERVICE token via
// platformServiceCtx (anonymous, for the IsServiceCall-gated cross-tenant
// RPCs) or platformTenantCtx (asserts the TARGET tenant, reusing the
// tenant-scoped resolver surface as-is).

const platformDefaultTenantLimit = 100
const platformMaxTenantLimit = 500

// RequirePlatformOperator gates platform admin on the platform_operator
// grant via the authz PDP. Pure check; auditing happens in platformGate so
// every surface records both outcomes.
func (r *Resolver) RequirePlatformOperator(ctx context.Context) error {
	id := authz.Identity{
		UserID:           ctxkeys.GetUserID(ctx),
		TenantID:         ctxkeys.GetTenantID(ctx),
		Role:             ctxkeys.GetRole(ctx),
		PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}
	if user := middleware.GetUserFromContext(ctx); user != nil {
		id.UserID = user.UserID
		id.TenantID = user.TenantID
		id.Role = user.Role
		id.PlatformOperator = user.PlatformOperator
	}
	if !authz.Default.Can(ctx, id, authz.ActionAccessPlatformAdmin, authz.Resource{}).Allow {
		return fmt.Errorf("platform operator access required")
	}
	return nil
}

// platformGate is the single entry check for every platform admin read: demo mode
// is handled by the callers BEFORE this (the demo sweep runs with empty
// clients), then the operator check runs and the outcome — allowed or
// denied — is audit-logged with the surface and target tenant.
func (r *Resolver) platformGate(ctx context.Context, surface, targetTenant string) error {
	err := r.RequirePlatformOperator(ctx)
	r.auditPlatformRead(ctx, surface, targetTenant, err == nil)
	return err
}

// auditPlatformRead records who read what through platform admin, as
// structured log fields. Cross-tenant admin reads are support tooling
// touching customer data: the actor, target tenant, and outcome must be
// recorded — and since downstream calls run under the service token (actor
// erased), the gateway is the only layer that still knows who asked.
func (r *Resolver) auditPlatformRead(ctx context.Context, surface, targetTenant string, allowed bool) {
	operatorID := ctxkeys.GetUserID(ctx)
	operatorTenant := ctxkeys.GetTenantID(ctx)
	operatorEmail := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		operatorID = user.UserID
		operatorTenant = user.TenantID
		operatorEmail = user.Email
	}
	if targetTenant == "" {
		targetTenant = "*"
	}
	outcome := "allowed"
	if !allowed {
		outcome = "denied"
	}
	r.Logger.WithFields(logging.Fields{
		"audit":              "platform_admin_read",
		"surface":            surface,
		"target_tenant_id":   targetTenant,
		"operator_user_id":   operatorID,
		"operator_tenant_id": operatorTenant,
		"operator_email":     operatorEmail,
		"outcome":            outcome,
	}).Info("Platform admin read")
}

// strippedIdentityCtx shadows the caller's identity so pkg/clients
// interceptors fall back to the service token. It must be a Value()-shadowing
// wrapper (not a fresh context) so deadlines, cancellation, tracing, and
// loaders survive. tenantOverride, when set, asserts the TARGET tenant —
// downstream auth middleware trusts tenant metadata on service-token calls.
type strippedIdentityCtx struct {
	context.Context
	tenantOverride string
}

func (c strippedIdentityCtx) Value(key any) any {
	switch key {
	case ctxkeys.KeyTenantID:
		if c.tenantOverride != "" {
			return c.tenantOverride
		}
		return nil
	// Only the keys the gRPC client interceptors propagate as caller
	// identity are stripped. KeyAuthType and KeyPermissions stay: they are
	// gateway-local inputs to middleware.RequirePermission, which the reused
	// tenant-scoped resolvers (e.g. DoGetPlatformOverview) run before their
	// backend call — stripping them turns an authorized operator into
	// "unauthenticated" at the gateway layer.
	case ctxkeys.KeyUserID, ctxkeys.KeyEmail, ctxkeys.KeyRole, ctxkeys.KeyJWTToken,
		ctxkeys.KeyAPIToken, ctxkeys.KeyAPITokenHash, ctxkeys.KeyUser,
		ctxkeys.KeySessionToken, ctxkeys.KeyWalletAddr, ctxkeys.KeyPlatformOperator:
		return nil
	}
	return c.Context.Value(key)
}

// platformServiceCtx is the fully-anonymous service-call context for
// cross-tenant RPCs (IsServiceCall must be true downstream).
func platformServiceCtx(ctx context.Context) context.Context {
	return strippedIdentityCtx{Context: ctx}
}

// platformTenantCtx impersonates the target tenant at the data layer:
// service token + asserted tenant. Existing tenant-scoped Do* resolvers run
// unchanged against the target tenant.
func platformTenantCtx(ctx context.Context, tenantID string) context.Context {
	return strippedIdentityCtx{Context: ctx, tenantOverride: tenantID}
}

func (r *Resolver) DoPlatformTenants(ctx context.Context, timeRange *model.TimeRangeInput, limit *int) (*model.PlatformTenantIndex, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GeneratePlatformTenantIndex(), nil
	}
	if err := r.platformGate(ctx, "platform.tenants", ""); err != nil {
		return nil, err
	}

	max := platformDefaultTenantLimit
	if limit != nil && *limit > 0 {
		max = min(*limit, platformMaxTenantLimit)
	}
	sctx := platformServiceCtx(ctx)

	// Activity is the ranking spine (hard error); identity covers dormant
	// tenants (failure degrades to ID-only rows).
	var (
		wg           sync.WaitGroup
		activityResp *periscopepb.ListTenantActivityResponse
		activityErr  error
		tenantsByID  map[string]*quartermasterpb.Tenant
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		activityResp, activityErr = r.Clients.Periscope.ListTenantActivity(sctx, toTimeRangeOpts(timeRange), nil, int32(max))
	}()
	go func() {
		defer wg.Done()
		resp, err := r.Clients.Quartermaster.ListTenants(sctx, &commonpb.CursorPaginationRequest{First: int32(500)})
		if err != nil {
			r.Logger.WithError(err).Warn("Platform tenants: tenant identity lookup failed; rows degrade to IDs")
			return
		}
		tenantsByID = make(map[string]*quartermasterpb.Tenant, len(resp.Tenants))
		for _, t := range resp.Tenants {
			tenantsByID[t.Id] = t
		}
	}()
	wg.Wait()
	if activityErr != nil {
		r.Logger.WithError(activityErr).Error("Platform tenants: activity rollup failed")
		return nil, fmt.Errorf("failed to load tenant activity: %w", activityErr)
	}

	// Billing is filtered to exactly the tenant set being rendered (activity
	// rows ∪ listed tenants), never "all subscriptions": the snapshot RPC
	// caps its row count, and an uncorrelated cap would silently null out
	// billing for rendered rows once the platform outgrows it. Failure
	// degrades to null billing.
	rowIDs := make([]string, 0, len(activityResp.Tenants)+len(tenantsByID))
	inRows := make(map[string]bool, len(activityResp.Tenants)+len(tenantsByID))
	for _, a := range activityResp.Tenants {
		if !inRows[a.TenantId] {
			inRows[a.TenantId] = true
			rowIDs = append(rowIDs, a.TenantId)
		}
	}
	for id := range tenantsByID {
		if !inRows[id] {
			inRows[id] = true
			rowIDs = append(rowIDs, id)
		}
	}
	billingByID := map[string]*purserpb.TenantBillingSnapshot{}
	if len(rowIDs) > 0 {
		resp, err := r.Clients.Purser.ListTenantBillingSnapshots(sctx, rowIDs, int32(len(rowIDs)))
		if err != nil {
			r.Logger.WithError(err).Warn("Platform tenants: billing snapshots failed; rows degrade to null billing")
		} else {
			for _, s := range resp.Snapshots {
				billingByID[s.TenantId] = s
			}
		}
	}

	rows := make([]*model.PlatformTenantRow, 0, len(activityResp.Tenants))
	seen := make(map[string]bool, len(activityResp.Tenants))
	for _, a := range activityResp.Tenants {
		seen[a.TenantId] = true
		rows = append(rows, &model.PlatformTenantRow{
			TenantID: a.TenantId,
			Tenant:   tenantsByID[a.TenantId],
			Activity: activitySummaryFromPB(a),
			Billing:  billingByID[a.TenantId],
		})
	}
	// Tenants with no activity in range still belong on the admin view;
	// appended after the activity-ranked rows, inside the same limit.
	for _, t := range tenantsByID {
		if seen[t.Id] || len(rows) >= max {
			continue
		}
		rows = append(rows, &model.PlatformTenantRow{
			TenantID: t.Id,
			Tenant:   t,
			Activity: activitySummaryFromPB(nil),
			Billing:  billingByID[t.Id],
		})
	}

	return &model.PlatformTenantIndex{Rows: rows, GeneratedAt: time.Now()}, nil
}

func (r *Resolver) DoPlatformTenant(ctx context.Context, id string) (*markers.TenantAdminDetail, error) {
	if middleware.IsDemoMode(ctx) {
		return &markers.TenantAdminDetail{TenantID: demo.DemoTenantID}, nil
	}
	if err := r.platformGate(ctx, "platform.tenant", id); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("tenant id is required")
	}
	return &markers.TenantAdminDetail{TenantID: id}, nil
}

// The lazy sub-resolvers below are only reachable through the gated
// Platform fields, but each re-gates (and therefore re-audits) anyway:
// defense in depth is cheap and the audit trail then names every surface
// actually read, not just the entry point.

func (r *Resolver) DoPlatformTenantIdentity(ctx context.Context, tenantID string) (*quartermasterpb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTenant(), nil
	}
	if err := r.platformGate(ctx, "platform.tenant.identity", tenantID); err != nil {
		return nil, err
	}
	resp, err := r.Clients.Quartermaster.GetTenant(platformServiceCtx(ctx), tenantID)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Platform tenant: identity lookup failed")
		return nil, nil // partial data: identity is nullable
	}
	return resp.GetTenant(), nil
}

func (r *Resolver) DoPlatformTenantActivity(ctx context.Context, tenantID string, timeRange *model.TimeRangeInput) (*model.TenantActivitySummary, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTenantActivitySummary(), nil
	}
	if err := r.platformGate(ctx, "platform.tenant.activity", tenantID); err != nil {
		return nil, err
	}
	resp, err := r.Clients.Periscope.ListTenantActivity(platformServiceCtx(ctx), toTimeRangeOpts(timeRange), []string{tenantID}, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to load tenant activity: %w", err)
	}
	for _, a := range resp.Tenants {
		if a.TenantId == tenantID {
			return activitySummaryFromPB(a), nil
		}
	}
	return activitySummaryFromPB(nil), nil
}

// DoPlatformTenantOverview reuses the tenant-facing analytics overview by
// impersonating the target tenant at the data layer.
func (r *Resolver) DoPlatformTenantOverview(ctx context.Context, tenantID string, timeRange *model.TimeRangeInput) (*periscopepb.GetPlatformOverviewResponse, error) {
	if !middleware.IsDemoMode(ctx) {
		if err := r.platformGate(ctx, "platform.tenant.overview", tenantID); err != nil {
			return nil, err
		}
	}
	// Demo mode short-circuits inside DoGetPlatformOverview.
	return r.DoGetPlatformOverview(platformTenantCtx(ctx, tenantID), timeRange)
}

func (r *Resolver) DoPlatformTenantBillingSnapshot(ctx context.Context, tenantID string) (*purserpb.TenantBillingSnapshot, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTenantBillingSnapshot(demo.DemoTenantID), nil
	}
	if err := r.platformGate(ctx, "platform.tenant.billing.snapshot", tenantID); err != nil {
		return nil, err
	}
	resp, err := r.Clients.Purser.ListTenantBillingSnapshots(platformServiceCtx(ctx), []string{tenantID}, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to load billing snapshot: %w", err)
	}
	if len(resp.Snapshots) == 0 {
		return nil, nil
	}
	return resp.Snapshots[0], nil
}

// Billing tab passthroughs: the existing tenant-scoped resolvers run
// unchanged against the impersonated target tenant. Their demo branches
// also keep the demo sweep green.

func (r *Resolver) DoPlatformTenantInvoices(ctx context.Context, tenantID string, first *int, after *string, last *int, before *string) (*model.InvoicesConnection, error) {
	if !middleware.IsDemoMode(ctx) {
		if err := r.platformGate(ctx, "platform.tenant.billing.invoices", tenantID); err != nil {
			return nil, err
		}
	}
	return r.DoGetInvoicesConnection(platformTenantCtx(ctx, tenantID), first, after, last, before)
}

func (r *Resolver) DoPlatformTenantPrepaidBalance(ctx context.Context, tenantID string, currency *string) (*model.PrepaidBalance, error) {
	if !middleware.IsDemoMode(ctx) {
		if err := r.platformGate(ctx, "platform.tenant.billing.prepaidBalance", tenantID); err != nil {
			return nil, err
		}
	}
	return r.DoGetPrepaidBalance(platformTenantCtx(ctx, tenantID), currency)
}

func (r *Resolver) DoPlatformTenantBalanceTransactions(ctx context.Context, tenantID string, page *model.ConnectionInput, transactionType *string, timeRange *model.TimeRangeInput) (*model.BalanceTransactionsConnection, error) {
	if !middleware.IsDemoMode(ctx) {
		if err := r.platformGate(ctx, "platform.tenant.billing.balanceTransactions", tenantID); err != nil {
			return nil, err
		}
	}
	return r.DoGetBalanceTransactionsConnection(platformTenantCtx(ctx, tenantID), page, transactionType, timeRange)
}

func (r *Resolver) DoPlatformTenantUsageRecords(ctx context.Context, tenantID string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string) (*model.UsageRecordsConnection, error) {
	if !middleware.IsDemoMode(ctx) {
		if err := r.platformGate(ctx, "platform.tenant.billing.usageRecords", tenantID); err != nil {
			return nil, err
		}
	}
	return r.DoGetUsageRecordsConnection(platformTenantCtx(ctx, tenantID), timeRange, first, after, last, before)
}

func (r *Resolver) DoPlatformTenantContent(ctx context.Context, tenantID string) (*model.TenantAdminContent, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTenantAdminContent(), nil
	}
	if err := r.platformGate(ctx, "platform.tenant.content", tenantID); err != nil {
		return nil, err
	}
	sctx := platformServiceCtx(ctx)
	content := &model.TenantAdminContent{}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		resp, err := r.Clients.Commodore.ListStorageArtifacts(sctx, &commodorepb.ListStorageArtifactsRequest{TenantId: tenantID, Limit: 1})
		if err != nil {
			r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Platform tenant: artifact count failed")
			return
		}
		content.ArtifactCount = int(resp.TotalCount)
	}()
	go func() {
		defer wg.Done()
		resp, err := r.Clients.Commodore.GetTenantUserCount(sctx, tenantID)
		if err != nil {
			r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Platform tenant: user count failed")
			return
		}
		content.UserCount = int(resp.ActiveCount)
	}()
	go func() {
		defer wg.Done()
		resp, err := r.Clients.Periscope.ListTenantActivity(sctx, nil, []string{tenantID}, 1)
		if err != nil {
			r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Platform tenant: activity for content failed")
			return
		}
		for _, a := range resp.Tenants {
			if a.TenantId == tenantID {
				content.LiveStreams = int(a.LiveStreams)
				content.LastStreamAt = protoTimePtr(a.LastStreamAt)
				return
			}
		}
	}()
	wg.Wait()
	return content, nil
}

const platformClusterTenantPageSize = 10
const platformClusterFanout = 8

func (r *Resolver) DoPlatformClusters(ctx context.Context) ([]*model.ClusterPivotRow, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClusterPivotRows(), nil
	}
	if err := r.platformGate(ctx, "platform.clusters", ""); err != nil {
		return nil, err
	}
	sctx := platformServiceCtx(ctx)

	clustersResp, err := r.Clients.Quartermaster.ListClusters(sctx, &commonpb.CursorPaginationRequest{First: int32(200)})
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	liveByCluster := map[string]*periscopepb.NetworkClusterLiveStats{}
	if liveResp, err := r.Clients.Periscope.GetNetworkLiveStats(sctx); err != nil {
		r.Logger.WithError(err).Warn("Platform clusters: live stats failed; rows degrade to null liveStats")
	} else {
		for _, c := range liveResp.Clusters {
			liveByCluster[c.ClusterId] = c
		}
	}

	rows := make([]*model.ClusterPivotRow, len(clustersResp.Clusters))
	sem := make(chan struct{}, platformClusterFanout)
	var wg sync.WaitGroup
	for i, cluster := range clustersResp.Clusters {
		wg.Add(1)
		go func(i int, cluster *quartermasterpb.InfrastructureCluster) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			row := &model.ClusterPivotRow{
				Cluster:   sanitizedPivotCluster(cluster),
				LiveStats: clusterLiveStatsFromPB(liveByCluster[cluster.ClusterId]),
				Tenants:   []*quartermasterpb.Tenant{},
			}
			resp, err := r.Clients.Quartermaster.GetTenantsByCluster(sctx, cluster.ClusterId, &commonpb.CursorPaginationRequest{First: int32(platformClusterTenantPageSize)})
			if err != nil {
				r.Logger.WithError(err).WithField("cluster_id", cluster.ClusterId).Warn("Platform clusters: tenants-by-cluster failed")
			} else {
				row.Tenants = resp.Tenants
				row.TenantCount = len(resp.Tenants)
				if p := resp.GetPagination(); p != nil && p.TotalCount > 0 {
					row.TenantCount = int(p.TotalCount)
				}
			}
			rows[i] = row
		}(i, cluster)
	}
	wg.Wait()
	return rows, nil
}

// sanitizedPivotCluster strips the credentials-shaped connectivity fields
// (database/periscope URLs, Kafka brokers) before a cluster row leaves the
// pivot. The GraphQL Cluster type carries them for other surfaces; the god
// view only needs identity, health, and capacity.
func sanitizedPivotCluster(c *quartermasterpb.InfrastructureCluster) *quartermasterpb.InfrastructureCluster {
	if c == nil {
		return nil
	}
	clone, ok := proto.Clone(c).(*quartermasterpb.InfrastructureCluster)
	if !ok {
		return nil
	}
	clone.DatabaseUrl = nil
	clone.PeriscopeUrl = nil
	clone.KafkaBrokers = nil
	return clone
}

func protoTimePtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// activitySummaryFromPB tolerates nil: tenants without activity render as
// zeros, not nulls.
func activitySummaryFromPB(a *periscopepb.TenantActivity) *model.TenantActivitySummary {
	if a == nil {
		return &model.TenantActivitySummary{}
	}
	return &model.TenantActivitySummary{
		LiveStreams:    int(a.LiveStreams),
		CurrentViewers: int(a.CurrentViewers),
		IngestHours:    a.IngestHours,
		ViewerHours:    a.ViewerHours,
		EgressGb:       a.EgressGb,
		UniqueViewers:  int(a.UniqueViewers),
		TotalSessions:  int(a.TotalSessions),
		APIRequests:    int(a.ApiRequests),
		APIErrors:      int(a.ApiErrors),
		LastStreamAt:   protoTimePtr(a.LastStreamAt),
	}
}

func clusterLiveStatsFromPB(s *periscopepb.NetworkClusterLiveStats) *model.ClusterLiveStats {
	if s == nil {
		return nil
	}
	return &model.ClusterLiveStats{
		ClusterID:           s.ClusterId,
		ActiveStreams:       int(s.ActiveStreams),
		CurrentViewers:      int(s.CurrentViewers),
		UploadBytesPerSec:   float64(s.UploadBytesPerSec),
		DownloadBytesPerSec: float64(s.DownloadBytesPerSec),
		ActiveNodes:         int(s.ActiveNodes),
		EgressCapacityBps:   float64(s.EgressCapacityBps),
	}
}
