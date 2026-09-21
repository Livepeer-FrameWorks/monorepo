// Package authz is the platform's policy decision point (PDP). Call sites ask
// it whether an Identity may perform an Action against an optional Resource;
// they never compare roles or tenant IDs inline. Decisions are an interface
// (Authorizer) so the decision source is swappable; the default implementation
// decides from the identity's own token-borne claims with no external lookup.
package authz

import (
	"context"
	"slices"
	"strings"
)

// Action identifies an authorization-gated operation. New actions are added as
// Can cases without changing the Authorizer interface.
type Action string

const (
	// ActionAccessPlatformAdmin gates platform-wide operator surfaces (/admin
	// and the cross-tenant platform RPCs behind it).
	ActionAccessPlatformAdmin Action = "platform.admin.access"
	// ActionAdminMistNode gates break-glass Mist admin access on an edge node;
	// Resource.OwnerTenantID is the tenant that owns the node's cluster.
	ActionAdminMistNode Action = "mist.node.admin"
	// ActionManageTenantSettings gates tenant DNS and routing settings.
	ActionManageTenantSettings Action = "tenant.settings.manage"
	// ActionManageEdgeCluster gates private edge-cluster lifecycle and
	// enrollment-secret issuance.
	ActionManageEdgeCluster Action = "edge.cluster.manage"
	// ActionManageBilling gates tenant payment, subscription, and billing-profile mutations.
	ActionManageBilling Action = "billing.manage"
	// ActionManageStreams gates tenant-wide stream and push-target administration.
	ActionManageStreams Action = "streams.manage"
	// ActionManageDeveloperTokens gates revocation of credentials created by
	// another user in the same tenant.
	ActionManageDeveloperTokens Action = "developer.tokens.manage"
	// ActionManageWebhooks gates outbound webhook endpoint changes, signing
	// secret issuance, test sends, and replays.
	ActionManageWebhooks Action = "developer.webhooks.manage"
	// ActionReadPrivateInfrastructure gates full node and cluster inventory.
	ActionReadPrivateInfrastructure Action = "infrastructure.private.read"
	// Placement changes can widen paid fallback independently of stream editing.
	ActionReadMediaPlacement   Action = "media.placement.read"
	ActionManageMediaPlacement Action = "media.placement.manage"
)

// Identity is the authenticated principal, distilled from the access token or
// validated credentials. Decisions are made purely from these facts plus the
// request — never from ambient state.
type Identity struct {
	UserID           string
	TenantID         string
	Role             string
	Roles            []string
	Permissions      []string
	PlatformOperator bool
}

// HasRole reports membership in the RFC 9068 roles set.
func (id Identity) HasRole(role string) bool {
	return slices.Contains(id.Roles, role)
}

// Resource is the optional target of an action. The zero value means the
// action is not resource-scoped.
type Resource struct {
	// OwnerTenantID is the tenant that owns the targeted resource.
	OwnerTenantID string
}

// Decision is the outcome of an authorization check.
type Decision struct {
	Allow  bool
	Reason string
}

func allow() Decision             { return Decision{Allow: true} }
func deny(reason string) Decision { return Decision{Allow: false, Reason: reason} }

// Authorizer is the policy decision point seam.
type Authorizer interface {
	Can(ctx context.Context, id Identity, action Action, resource Resource) Decision
}

// DefaultAuthorizer decides purely from the identity's own claims, with no
// external policy lookup.
type DefaultAuthorizer struct{}

// Default is the process-wide authorizer that all call sites use.
var Default Authorizer = DefaultAuthorizer{}

// Can answers an authorization request. Unknown actions deny (fail-closed).
func (DefaultAuthorizer) Can(_ context.Context, id Identity, action Action, resource Resource) Decision {
	switch action {
	case ActionAccessPlatformAdmin:
		if id.PlatformOperator {
			return allow()
		}
		return deny("platform operator access required")
	case ActionReadMediaPlacement:
		if id.PlatformOperator {
			return allow()
		}
		if id.UserID != "" && resource.OwnerTenantID != "" && id.TenantID == resource.OwnerTenantID && slices.Contains([]string{"owner", "admin", "member", "viewer"}, strings.ToLower(strings.TrimSpace(id.Role))) {
			return allow()
		}
		return deny("authenticated tenant membership required")
	case ActionAdminMistNode, ActionManageTenantSettings, ActionManageEdgeCluster, ActionManageBilling, ActionManageStreams, ActionManageDeveloperTokens, ActionManageWebhooks, ActionReadPrivateInfrastructure, ActionManageMediaPlacement:
		// A platform operator may perform privileged cross-tenant work. Otherwise
		// the caller must be an owner/admin of the tenant that owns the resource.
		if id.PlatformOperator {
			return allow()
		}
		owner := strings.TrimSpace(resource.OwnerTenantID)
		caller := strings.TrimSpace(id.TenantID)
		if identityHasPrivilegedTenantRole(id) && owner != "" && caller == owner {
			return allow()
		}
		return deny("tenant owner/admin or platform operator required")
	default:
		return deny("unknown action")
	}
}

func identityHasPrivilegedTenantRole(id Identity) bool {
	// Tenant membership has one canonical role claim. RFC 9068 Roles carries
	// platform authorization attributes and must not widen tenant membership.
	return privilegedTenantRole(id.Role)
}

// privilegedTenantRole is the tenant-scoped role predicate (owner/admin) used
// for resource ownership decisions. Distinct from the platform_operator grant.
func privilegedTenantRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "owner" || role == "admin"
}
