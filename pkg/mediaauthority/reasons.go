package mediaauthority

import mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"

// ReasonOperatorGrantExpired identifies a fail-closed denial awaiting grant expiry settlement.
const ReasonOperatorGrantExpired = "operator_grant_expired"

// ReasonPrepaidBalanceUnavailable identifies payment-required access from a non-positive prepaid balance.
const ReasonPrepaidBalanceUnavailable = "prepaid_balance_unavailable"

// ReasonAccountInactive identifies account revocation in session-stop delivery and login audit.
const ReasonAccountInactive = "account_inactive"

// PrepaidBalanceSpent reports the one billing denial that refuses new streams
// and viewers while everything already running continues. Running sessions end
// only when the balance reaches the suspension threshold, which signs SUSPENDED.
func PrepaidBalanceSpent(tenant *mediaauthoritypb.TenantAuthority) bool {
	return tenant != nil &&
		tenant.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE &&
		tenant.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED &&
		tenant.GetDecisionReason() == ReasonPrepaidBalanceUnavailable
}
