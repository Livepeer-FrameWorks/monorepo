package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ScopePlatform = "platform"
	ScopeTenant   = "tenant"

	ownerLookupTimeout = 3 * time.Second
)

// Scope is the visibility of an incident.
type Scope struct {
	Kind     string
	TenantID string
}

// ClusterOwner is Quartermaster's answer for one cluster.
type ClusterOwner struct {
	Scope Scope
	// UpdatedAt is the cluster's Quartermaster updated_at; it is zero when
	// Quartermaster reported the cluster missing.
	UpdatedAt time.Time
}

// OwnerLookup asks Quartermaster who owns a cluster. An error means the owner
// could not be verified.
type OwnerLookup interface {
	LookupOwner(ctx context.Context, clusterID string) (ClusterOwner, error)
}

// ClusterGetter is the Quartermaster call used to find a cluster's owner.
type ClusterGetter interface {
	GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
}

// QuartermasterOwners maps a cluster to tenant scope when a tenant owns it and
// the cluster is not platform-operated; every other cluster, including one
// Quartermaster does not know, is platform scope.
type QuartermasterOwners struct {
	Clusters ClusterGetter
	Logger   logging.Logger
	Metrics  *Metrics
}

// LookupOwner returns the cluster's current owner from Quartermaster.
func (o *QuartermasterOwners) LookupOwner(ctx context.Context, clusterID string) (ClusterOwner, error) {
	if o == nil || o.Clusters == nil {
		return ClusterOwner{}, errors.New("no Quartermaster client")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, ownerLookupTimeout)
	defer cancel()
	resp, err := o.Clusters.GetCluster(lookupCtx, clusterID)
	switch {
	case err == nil:
		cluster := resp.GetCluster()
		owner := ClusterOwner{Scope: scopeForCluster(cluster)}
		if updated := cluster.GetUpdatedAt(); updated != nil {
			owner.UpdatedAt = updated.AsTime()
		}
		o.Metrics.observeScopeLookup(owner.Scope.Kind)
		return owner, nil
	case status.Code(err) == codes.NotFound:
		o.Metrics.observeScopeLookup("not_found")
		return ClusterOwner{Scope: Scope{Kind: ScopePlatform}}, nil
	default:
		if o.Logger != nil {
			o.Logger.WithError(err).WithField("cluster_id", clusterID).Warn("Quartermaster cluster owner lookup failed")
		}
		o.Metrics.observeScopeLookup("error")
		return ClusterOwner{}, err
	}
}

func scopeForCluster(cluster *quartermasterpb.InfrastructureCluster) Scope {
	if cluster == nil || cluster.GetIsPlatformOfficial() {
		return Scope{Kind: ScopePlatform}
	}
	owner := strings.TrimSpace(cluster.GetOwnerTenantId())
	if owner == "" {
		return Scope{Kind: ScopePlatform}
	}
	return Scope{Kind: ScopeTenant, TenantID: owner}
}

// lockClusterScope returns the stored scope of an alerting cluster under a
// share lock held until the transaction ends, creating an unverified platform
// row when none exists. An alert without a cluster is verified platform scope.
func lockClusterScope(ctx context.Context, q *lookoutdb.Queries, clusterID string) (Scope, bool, error) {
	if clusterID == "" {
		return Scope{Kind: ScopePlatform}, true, nil
	}
	if err := q.EnsureClusterScope(ctx, clusterID); err != nil {
		return Scope{}, false, fmt.Errorf("ensure cluster scope: %w", err)
	}
	row, err := q.LockClusterScopeShared(ctx, clusterID)
	if err != nil {
		return Scope{}, false, fmt.Errorf("lock cluster scope: %w", err)
	}
	return storedScope(row.Scope, row.TenantID), row.Verified, nil
}

// clusterScopeState reads the cluster's stored scope without locking it:
// whether it is verified, and whether a stored owner change still has open
// incidents to move.
func clusterScopeState(ctx context.Context, q *lookoutdb.Queries, clusterID string) (verified, movePending bool, err error) {
	row, err := q.GetClusterScope(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("get cluster scope: %w", err)
	}
	return row.Verified, row.Verified && row.AppliedRevision < row.Revision, nil
}

func storedScope(kind string, tenantID sql.NullString) Scope {
	if kind == ScopeTenant && tenantID.Valid {
		return Scope{Kind: ScopeTenant, TenantID: tenantID.String}
	}
	return Scope{Kind: ScopePlatform}
}
