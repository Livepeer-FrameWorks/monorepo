from datetime import datetime
from typing import Any, Optional, Union

from .abort_vod_upload import AbortVodUpload
from .accept_cluster_invite import AcceptClusterInvite
from .acknowledge_incident import AcknowledgeIncident
from .add_incident_note import AddIncidentNote
from .apply_cluster_media_consent_change import ApplyClusterMediaConsentChange
from .apply_media_placement_change import ApplyMediaPlacementChange
from .approve_cluster_subscription import ApproveClusterSubscription
from .assign_incident import AssignIncident
from .base_model import UNSET, UnsetType
from .bootstrap_edge import BootstrapEdge
from .change_billing_tier import ChangeBillingTier
from .complete_vod_upload import CompleteVodUpload
from .create_card_topup import CreateCardTopup
from .create_clip import CreateClip
from .create_cluster_invite import CreateClusterInvite
from .create_conversation import CreateConversation
from .create_crypto_topup import CreateCryptoTopup
from .create_developer_token import CreateDeveloperToken
from .create_edge_cluster import CreateEdgeCluster
from .create_enrollment_token import CreateEnrollmentToken
from .create_mollie_first_payment import CreateMollieFirstPayment
from .create_mollie_subscription import CreateMollieSubscription
from .create_payment import CreatePayment
from .create_push_target import CreatePushTarget
from .create_signing_key import CreateSigningKey
from .create_stream import CreateStream
from .create_stream_key import CreateStreamKey
from .create_stripe_billing_portal import CreateStripeBillingPortal
from .create_stripe_checkout import CreateStripeCheckout
from .create_vod_upload import CreateVodUpload
from .create_webhook_endpoint import CreateWebhookEndpoint
from .crypto_topup_status_mutation import CryptoTopupStatusMutation
from .delete_clip import DeleteClip
from .delete_dvr import DeleteDVR
from .delete_push_target import DeletePushTarget
from .delete_skipper_conversation import DeleteSkipperConversation
from .delete_stream import DeleteStream
from .delete_stream_key import DeleteStreamKey
from .delete_vod_asset import DeleteVodAsset
from .delete_webhook_endpoint import DeleteWebhookEndpoint
from .disable_webhook_endpoint import DisableWebhookEndpoint
from .enable_webhook_endpoint import EnableWebhookEndpoint
from .enums import (
    DVRChapterMode,
    InstanceStatus,
    MediaIngestProtocol,
    MediaViewerProtocol,
    NodeStatus,
    SortOrder,
    StreamSummarySortField,
    WebhookDeliveryStatus,
)
from .get_analytics_infra_service_instances_health import (
    GetAnalyticsInfraServiceInstancesHealth,
)
from .get_api_usage_connection import GetApiUsageConnection
from .get_artifact_events_connection import GetArtifactEventsConnection
from .get_artifact_node_copies import GetArtifactNodeCopies
from .get_artifact_states_connection import GetArtifactStatesConnection
from .get_balance_transactions_connection import GetBalanceTransactionsConnection
from .get_billing_details import GetBillingDetails
from .get_billing_status import GetBillingStatus
from .get_billing_tiers import GetBillingTiers
from .get_buffer_events_connection import GetBufferEventsConnection
from .get_capabilities import GetCapabilities
from .get_client_qoe_connection import GetClientQoeConnection
from .get_client_qoe_summary import GetClientQoeSummary
from .get_clip import GetClip
from .get_cluster import GetCluster
from .get_cluster_boot_ops import GetClusterBootOps
from .get_cluster_invites_connection import GetClusterInvitesConnection
from .get_cluster_media_consent import GetClusterMediaConsent
from .get_cluster_media_consent_change import GetClusterMediaConsentChange
from .get_cluster_nodes_connection import GetClusterNodesConnection
from .get_cluster_qoe_ops import GetClusterQoeOps
from .get_cluster_traffic_matrix import GetClusterTrafficMatrix
from .get_cluster_workload import GetClusterWorkload
from .get_clusters_access_connection import GetClustersAccessConnection
from .get_clusters_available_connection import GetClustersAvailableConnection
from .get_clusters_connection import GetClustersConnection
from .get_connection_events_connection import GetConnectionEventsConnection
from .get_conversation import GetConversation
from .get_conversations_connection import GetConversationsConnection
from .get_daily_stats import GetDailyStats
from .get_discover_services_connection import GetDiscoverServicesConnection
from .get_dvr_chapter import GetDVRChapter
from .get_federation_events_connection import GetFederationEventsConnection
from .get_federation_summary import GetFederationSummary
from .get_geographic_distribution import GetGeographicDistribution
from .get_incident import GetIncident
from .get_incidents_connection import GetIncidentsConnection
from .get_infrastructure_node_metrics_1_h_connection import (
    GetInfrastructureNodeMetrics1hConnection,
)
from .get_infrastructure_node_metrics_connection import (
    GetInfrastructureNodeMetricsConnection,
)
from .get_invoice import GetInvoice
from .get_invoices_connection import GetInvoicesConnection
from .get_marketplace_cluster import GetMarketplaceCluster
from .get_marketplace_clusters_connection import GetMarketplaceClustersConnection
from .get_media_placement_change import GetMediaPlacementChange
from .get_media_placement_options import GetMediaPlacementOptions
from .get_media_placement_policy import GetMediaPlacementPolicy
from .get_media_retention_policy import GetMediaRetentionPolicy
from .get_messages_connection import GetMessagesConnection
from .get_mollie_mandates import GetMollieMandates
from .get_my_cluster_invites_connection import GetMyClusterInvitesConnection
from .get_my_subscriptions_connection import GetMySubscriptionsConnection
from .get_network_status import GetNetworkStatus
from .get_node import GetNode
from .get_node_metrics_1_h_connection import GetNodeMetrics1hConnection
from .get_node_metrics_aggregated import GetNodeMetricsAggregated
from .get_node_metrics_connection import GetNodeMetricsConnection
from .get_node_performance_5_m_connection import GetNodePerformance5mConnection
from .get_nodes_connection import GetNodesConnection
from .get_orchestrator import GetOrchestrator
from .get_orchestrator_instances import GetOrchestratorInstances
from .get_orchestrator_performance_series import GetOrchestratorPerformanceSeries
from .get_orchestrator_vantages import GetOrchestratorVantages
from .get_orchestrators_connection import GetOrchestratorsConnection
from .get_overview import GetOverview
from .get_payment import GetPayment
from .get_payments_connection import GetPaymentsConnection
from .get_pending_subscriptions_connection import GetPendingSubscriptionsConnection
from .get_player_boot_summary import GetPlayerBootSummary
from .get_player_boot_time_series import GetPlayerBootTimeSeries
from .get_prepaid_balance import GetPrepaidBalance
from .get_preview_media_placement import GetPreviewMediaPlacement
from .get_processing_usage_connection import GetProcessingUsageConnection
from .get_quality_tier_daily_connection import GetQualityTierDailyConnection
from .get_rebuffering_events_connection import GetRebufferingEventsConnection
from .get_recent_pull_source_events import GetRecentPullSourceEvents
from .get_review_cluster_media_consent_change import GetReviewClusterMediaConsentChange
from .get_review_media_placement_change import GetReviewMediaPlacementChange
from .get_routing_efficiency import GetRoutingEfficiency
from .get_routing_events_connection import GetRoutingEventsConnection
from .get_service_instances_connection import GetServiceInstancesConnection
from .get_service_instances_health import GetServiceInstancesHealth
from .get_session_qoe_summary import GetSessionQoeSummary
from .get_session_qoe_time_series import GetSessionQoeTimeSeries
from .get_signing_key import GetSigningKey
from .get_skipper_conversation import GetSkipperConversation
from .get_skipper_conversations import GetSkipperConversations
from .get_skipper_report import GetSkipperReport
from .get_skipper_reports import GetSkipperReports
from .get_skipper_unread_report_count import GetSkipperUnreadReportCount
from .get_storage_events_connection import GetStorageEventsConnection
from .get_storage_usage_connection import GetStorageUsageConnection
from .get_stream import GetStream
from .get_stream_analytics_daily_connection import GetStreamAnalyticsDailyConnection
from .get_stream_analytics_summaries_connection import (
    GetStreamAnalyticsSummariesConnection,
)
from .get_stream_analytics_summary import GetStreamAnalyticsSummary
from .get_stream_connection_hourly_connection import GetStreamConnectionHourlyConnection
from .get_stream_events_connection import GetStreamEventsConnection
from .get_stream_health_5_m_connection import GetStreamHealth5mConnection
from .get_stream_health_connection import GetStreamHealthConnection
from .get_stream_health_summary import GetStreamHealthSummary
from .get_streaming_config import GetStreamingConfig
from .get_tenant import GetTenant
from .get_tenant_analytics_daily_connection import GetTenantAnalyticsDailyConnection
from .get_tenant_usage import GetTenantUsage
from .get_top_assets import GetTopAssets
from .get_track_list_connection import GetTrackListConnection
from .get_usage_aggregates import GetUsageAggregates
from .get_validate_stream_key import GetValidateStreamKey
from .get_viewer_geo_hourly_connection import GetViewerGeoHourlyConnection
from .get_viewer_geographics_connection import GetViewerGeographicsConnection
from .get_viewer_hours_hourly_connection import GetViewerHoursHourlyConnection
from .get_viewer_sessions_connection import GetViewerSessionsConnection
from .get_viewer_time_series_connection import GetViewerTimeSeriesConnection
from .get_vod_asset import GetVodAsset
from .get_vod_retention import GetVodRetention
from .get_vod_retention_assets import GetVodRetentionAssets
from .get_vod_upload_status import GetVodUploadStatus
from .get_webhook_deliveries_connection import GetWebhookDeliveriesConnection
from .get_webhook_delivery import GetWebhookDelivery
from .get_webhook_endpoint import GetWebhookEndpoint
from .get_webhook_endpoints_connection import GetWebhookEndpointsConnection
from .get_webhook_event_types import GetWebhookEventTypes
from .input_types import (
    ApplyMediaCapacityConsentInput,
    ApplyMediaPlacementChangeInput,
    BootstrapEdgeInput,
    CompleteVodUploadInput,
    ConnectionInput,
    CreateCardTopupInput,
    CreateClipInput,
    CreateClusterInviteInput,
    CreateConversationInput,
    CreateCryptoTopupInput,
    CreateDeveloperTokenInput,
    CreateEdgeClusterInput,
    CreatePaymentInput,
    CreatePushTargetInput,
    CreateSigningKeyInput,
    CreateStreamInput,
    CreateStreamKeyInput,
    CreateVodUploadInput,
    CreateWebhookEndpointInput,
    IncidentFilterInput,
    LinkEmailInput,
    MediaPlacementOptionsFilter,
    MediaPlacementScopeInput,
    OpenMistAdminSessionInput,
    PreviewMediaPlacementInput,
    ResetMediaRetentionOverrideInput,
    ReviewMediaCapacityConsentInput,
    ReviewMediaPlacementChangeInput,
    SendMessageInput,
    SetMediaRetentionPolicyInput,
    SetNodeModeInput,
    SetPlaybackPolicyInput,
    SetStreamRetentionOverridesInput,
    StorageArtifactsInput,
    TestPlaybackAccessInput,
    TimeRangeInput,
    UpdateBillingDetailsInput,
    UpdateClusterMarketplaceInput,
    UpdateMediaRetentionInput,
    UpdatePushTargetInput,
    UpdateStreamInput,
    UpdateTenantInput,
    UpdateWebhookEndpointInput,
    WalletLoginInput,
)
from .link_email import LinkEmail
from .link_wallet import LinkWallet
from .list_artifacts import ListArtifacts
from .list_developer_tokens import ListDeveloperTokens
from .list_dvr_chapters import ListDVRChapters
from .list_push_targets import ListPushTargets
from .list_signing_keys import ListSigningKeys
from .list_stream_keys import ListStreamKeys
from .list_streams import ListStreams
from .list_usage_records import ListUsageRecords
from .mark_skipper_reports_read import MarkSkipperReportsRead
from .open_mist_admin_session import OpenMistAdminSession
from .promote_to_paid import PromoteToPaid
from .refresh_stream_key import RefreshStreamKey
from .reject_cluster_subscription import RejectClusterSubscription
from .replay_webhook_deliveries import ReplayWebhookDeliveries
from .replay_webhook_delivery import ReplayWebhookDelivery
from .request_cluster_subscription import RequestClusterSubscription
from .reset_media_retention_override import ResetMediaRetentionOverride
from .resolve_incident import ResolveIncident
from .resolve_ingest_endpoint import ResolveIngestEndpoint
from .resolve_viewer_endpoint import ResolveViewerEndpoint
from .revoke_cluster_invite import RevokeClusterInvite
from .revoke_developer_token import RevokeDeveloperToken
from .revoke_signing_key import RevokeSigningKey
from .rotate_webhook_endpoint_secret import RotateWebhookEndpointSecret
from .send_message import SendMessage
from .server_info import ServerInfo
from .set_media_retention_policy import SetMediaRetentionPolicy
from .set_node_mode import SetNodeMode
from .set_playback_policy import SetPlaybackPolicy
from .set_preferred_cluster import SetPreferredCluster
from .set_stream_retention_overrides import SetStreamRetentionOverrides
from .start_dvr import StartDVR
from .stop_dvr import StopDVR
from .submit_x_402_payment import SubmitX402Payment
from .subscribe_to_cluster import SubscribeToCluster
from .sync_base_client import BaseClient
from .test_playback_access import TestPlaybackAccess
from .test_webhook_endpoint import TestWebhookEndpoint
from .unlink_wallet import UnlinkWallet
from .unsubscribe_from_cluster import UnsubscribeFromCluster
from .update_billing_details import UpdateBillingDetails
from .update_cluster_marketplace import UpdateClusterMarketplace
from .update_media_retention import UpdateMediaRetention
from .update_push_target import UpdatePushTarget
from .update_skipper_conversation import UpdateSkipperConversation
from .update_stream import UpdateStream
from .update_tenant import UpdateTenant
from .update_webhook_endpoint import UpdateWebhookEndpoint
from .wallet_login import WalletLogin


def gql(q: str) -> str:
    return q


class GraphQLClient(BaseClient):
    def accept_cluster_invite(
        self, invite_token: str, **kwargs: Any
    ) -> AcceptClusterInvite:
        """Accept a cluster invite."""
        query = gql("""
            mutation AcceptClusterInvite($inviteToken: String!) {
              acceptClusterInvite(inviteToken: $inviteToken) {
                __typename
                ...ClusterSubscriptionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterSubscriptionDefaultFields on ClusterSubscription {
              id
              tenantId
              clusterId
              accessLevel
              subscriptionStatus
              resourceLimits
              requestedAt
              approvedAt
              approvedBy
              rejectionReason
              expiresAt
              createdAt
              updatedAt
              clusterName
              tenantName
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"inviteToken": invite_token}
        response = self.execute(
            query=query,
            operation_name="AcceptClusterInvite",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return AcceptClusterInvite.model_validate(data)

    def acknowledge_incident(self, id: str, **kwargs: Any) -> AcknowledgeIncident:
        """Acknowledge an incident. The incident stays open until its alerts resolve
        or someone resolves it."""
        query = gql("""
            mutation AcknowledgeIncident($id: ID!) {
              acknowledgeIncident(id: $id) {
                __typename
                ...IncidentDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment IncidentDefaultFields on Incident {
              id
              scope
              tenantId
              clusterId
              region
              alertname
              severity
              status
              resolution
              title
              summary
              firingAlertCount
              startedAt
              lastAlertAt
              acknowledgedAt
              acknowledgedBy
              assignedTo
              resolvedAt
              resolvedBy
              createdAt
              updatedAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="AcknowledgeIncident",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return AcknowledgeIncident.model_validate(data)

    def add_incident_note(self, id: str, body: str, **kwargs: Any) -> AddIncidentNote:
        """Add a note to an incident's timeline."""
        query = gql("""
            mutation AddIncidentNote($id: ID!, $body: String!) {
              addIncidentNote(id: $id, body: $body) {
                __typename
                ...IncidentDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment IncidentDefaultFields on Incident {
              id
              scope
              tenantId
              clusterId
              region
              alertname
              severity
              status
              resolution
              title
              summary
              firingAlertCount
              startedAt
              lastAlertAt
              acknowledgedAt
              acknowledgedBy
              assignedTo
              resolvedAt
              resolvedBy
              createdAt
              updatedAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id, "body": body}
        response = self.execute(
            query=query, operation_name="AddIncidentNote", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return AddIncidentNote.model_validate(data)

    def apply_cluster_media_consent_change(
        self, input: ApplyMediaCapacityConsentInput, **kwargs: Any
    ) -> ApplyClusterMediaConsentChange:
        query = gql("""
            mutation ApplyClusterMediaConsentChange($input: ApplyMediaCapacityConsentInput!) {
              applyClusterMediaConsentChange(input: $input) {
                __typename
                ...MediaCapacityConsentChangeDefaultFields
                ...MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaCapacityConsentChangeDefaultFields on MediaCapacityConsentChange {
              clusterId
              idempotencyKey
              revision
              digest
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
              createdAt
            }

            fragment MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="ApplyClusterMediaConsentChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ApplyClusterMediaConsentChange.model_validate(data)

    def apply_media_placement_change(
        self, input: ApplyMediaPlacementChangeInput, **kwargs: Any
    ) -> ApplyMediaPlacementChange:
        """Atomically saves the requested verbs. Saved does not mean effective on every cell."""
        query = gql("""
            mutation ApplyMediaPlacementChange($input: ApplyMediaPlacementChangeInput!) {
              applyMediaPlacementChange(input: $input) {
                __typename
                ...MediaPlacementChangeDefaultFields
                ...MediaPlacementErrorInMediaPlacementChangeResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementChangeDefaultFields on MediaPlacementChange {
              scope {
                kind
                streamId
              }
              idempotencyKey
              revision
              parentRevision
              digest
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
              createdAt
            }

            fragment MediaPlacementErrorInMediaPlacementChangeResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              mediaPlacementErrorParentRevision: parentRevision
              retryAfterSeconds
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="ApplyMediaPlacementChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ApplyMediaPlacementChange.model_validate(data)

    def approve_cluster_subscription(
        self, subscription_id: str, **kwargs: Any
    ) -> ApproveClusterSubscription:
        """Approve a pending cluster subscription request."""
        query = gql("""
            mutation ApproveClusterSubscription($subscriptionId: ID!) {
              approveClusterSubscription(subscriptionId: $subscriptionId) {
                __typename
                ...ClusterSubscriptionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterSubscriptionDefaultFields on ClusterSubscription {
              id
              tenantId
              clusterId
              accessLevel
              subscriptionStatus
              resourceLimits
              requestedAt
              approvedAt
              approvedBy
              rejectionReason
              expiresAt
              createdAt
              updatedAt
              clusterName
              tenantName
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"subscriptionId": subscription_id}
        response = self.execute(
            query=query,
            operation_name="ApproveClusterSubscription",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ApproveClusterSubscription.model_validate(data)

    def assign_incident(
        self,
        id: str,
        assignee_user_id: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> AssignIncident:
        """Assign an incident to a user of the incident's tenant. A null assignee
        clears the assignment."""
        query = gql("""
            mutation AssignIncident($id: ID!, $assigneeUserId: ID) {
              assignIncident(id: $id, assigneeUserId: $assigneeUserId) {
                __typename
                ...IncidentDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment IncidentDefaultFields on Incident {
              id
              scope
              tenantId
              clusterId
              region
              alertname
              severity
              status
              resolution
              title
              summary
              firingAlertCount
              startedAt
              lastAlertAt
              acknowledgedAt
              acknowledgedBy
              assignedTo
              resolvedAt
              resolvedBy
              createdAt
              updatedAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id, "assigneeUserId": assignee_user_id}
        response = self.execute(
            query=query, operation_name="AssignIncident", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return AssignIncident.model_validate(data)

    def bootstrap_edge(self, input: BootstrapEdgeInput, **kwargs: Any) -> BootstrapEdge:
        """Bootstrap a new edge using only an opaque bootstrap token.

        Public — the bootstrap token is itself the credential. Bridge resolves
        the token's cluster via Quartermaster, finds the cluster's assigned
        Foghorn, and proxies a PreRegisterEdge call so the operator never has
        to know cluster topology."""
        query = gql("""
            mutation BootstrapEdge($input: BootstrapEdgeInput!) {
              bootstrapEdge(input: $input) {
                __typename
                ...BootstrapEdgeResponseDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment BootstrapEdgeResponseDefaultFields on BootstrapEdgeResponse {
              nodeId
              edgeDomain
              poolDomain
              clusterSlug
              clusterId
              foghornGrpcAddr
              certPem
              keyPem
              internalCaBundle
              telemetry {
                enabled
                writeUrl
                bearerToken
              }
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="BootstrapEdge", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return BootstrapEdge.model_validate(data)

    def change_billing_tier(self, tier_id: str, **kwargs: Any) -> ChangeBillingTier:
        """Change the postpaid billing tier. Upgrades apply immediately and reconcile
        cluster access; downgrades are scheduled for the current billing period end
        so the tenant keeps paid entitlements until the period closes."""
        query = gql("""
            mutation ChangeBillingTier($tierId: ID!) {
              changeBillingTier(tierId: $tierId) {
                __typename
                ...ChangeBillingTierPayloadDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ChangeBillingTierPayloadDefaultFields on ChangeBillingTierPayload {
              success
              message
              appliedTier {
                id
                tierName
                tierLevel
                displayName
                description
                basePrice
                currency
                billingPeriod
                features {
                  supportLevel
                  sla
                  processingCustomizable
                }
                pricingRules {
                  meter
                  model
                  currency
                  includedQuantity
                  unitPrice
                  configJson
                }
                entitlements {
                  key
                  value
                }
                supportLevel
                slaLevel
                meteringEnabled
                isEnterprise
              }
              pendingTier {
                id
                tierName
                tierLevel
                displayName
                description
                basePrice
                currency
                billingPeriod
                features {
                  supportLevel
                  sla
                  processingCustomizable
                }
                pricingRules {
                  meter
                  model
                  currency
                  includedQuantity
                  unitPrice
                  configJson
                }
                entitlements {
                  key
                  value
                }
                supportLevel
                slaLevel
                meteringEnabled
                isEnterprise
              }
              effectiveAt
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"tierId": tier_id}
        response = self.execute(
            query=query,
            operation_name="ChangeBillingTier",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ChangeBillingTier.model_validate(data)

    def create_card_topup(
        self, input: CreateCardTopupInput, **kwargs: Any
    ) -> CreateCardTopup:
        """Create a card checkout session for prepaid balance top-up.
        Returns a URL to redirect the user to Stripe/Mollie checkout.
        After successful payment, balance is credited automatically via webhook."""
        query = gql("""
            mutation CreateCardTopup($input: CreateCardTopupInput!) {
              createCardTopup(input: $input) {
                ...CardTopupResultDefaultFields
              }
            }

            fragment CardTopupResultDefaultFields on CardTopupResult {
              topupId
              checkoutUrl
              expiresAt
              amountCents
              currency
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="CreateCardTopup", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateCardTopup.model_validate(data)

    def create_cluster_invite(
        self, input: CreateClusterInviteInput, **kwargs: Any
    ) -> CreateClusterInvite:
        """Create an invite for a cluster."""
        query = gql("""
            mutation CreateClusterInvite($input: CreateClusterInviteInput!) {
              createClusterInvite(input: $input) {
                __typename
                ...ClusterInviteDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterInviteDefaultFields on ClusterInvite {
              id
              clusterId
              invitedTenantId
              inviteToken
              accessLevel
              resourceLimits
              status
              createdBy
              createdAt
              expiresAt
              acceptedAt
              invitedTenantName
              clusterName
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateClusterInvite",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateClusterInvite.model_validate(data)

    def create_conversation(
        self, input: CreateConversationInput, **kwargs: Any
    ) -> CreateConversation:
        """Create a new support conversation.
        Optionally include an initial message."""
        query = gql("""
            mutation CreateConversation($input: CreateConversationInput!) {
              createConversation(input: $input) {
                __typename
                ...ConversationDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ConversationDefaultFields on Conversation {
              id
              subject
              status
              lastMessage {
                id
                conversationId
                content
                sender
                createdAt
              }
              unreadCount
              createdAt
              updatedAt
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateConversation",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateConversation.model_validate(data)

    def create_crypto_topup(
        self, input: CreateCryptoTopupInput, **kwargs: Any
    ) -> CreateCryptoTopup:
        """Create a crypto deposit address for prepaid balance top-up.
        Returns an HD-derived address for the agent to send crypto.
        This is the agent-friendly payment method - no human-in-the-loop required."""
        query = gql("""
            mutation CreateCryptoTopup($input: CreateCryptoTopupInput!) {
              createCryptoTopup(input: $input) {
                ...CryptoTopupResultDefaultFields
              }
            }

            fragment CryptoTopupResultDefaultFields on CryptoTopupResult {
              topupId
              depositAddress
              asset
              assetSymbol
              expectedAmountCents
              expiresAt
              expectedAmountBaseUnits
              expectedAmountToken
              quotedPriceUsd
              quoteSource
              quotedAt
              network
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateCryptoTopup",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateCryptoTopup.model_validate(data)

    def create_edge_cluster(
        self, input: CreateEdgeClusterInput, **kwargs: Any
    ) -> CreateEdgeCluster:
        """Create an edge cluster with automatic Foghorn assignment and enrollment token."""
        query = gql("""
            mutation CreateEdgeCluster($input: CreateEdgeClusterInput!) {
              createEdgeCluster(input: $input) {
                __typename
                ...CreateEdgeClusterResponseDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment CreateEdgeClusterResponseDefaultFields on CreateEdgeClusterResponse {
              cluster {
                id
                clusterId
                clusterName
                clusterType
                deploymentModel
                baseUrl
                databaseUrl
                periscopeUrl
                kafkaBrokers
                maxConcurrentStreams
                maxConcurrentViewers
                maxBandwidthMbps
                healthStatus
                isActive
                isDefaultCluster
                isPlatformOfficial
                regionId
                isSubscribed
                createdAt
                updatedAt
                ownerTenantId
                visibility
                pricingModel
                monthlyPriceCents
                requiresApproval
                shortDescription
              }
              bootstrapToken {
                id
                name
                token
                kind
                clusterId
                expectedIp
                metadata
                usageLimit
                usageCount
                expiresAt
                usedAt
                createdBy
                createdAt
              }
              foghornAddr
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateEdgeCluster",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateEdgeCluster.model_validate(data)

    def create_enrollment_token(
        self,
        cluster_id: str,
        name: Union[Optional[str], UnsetType] = UNSET,
        ttl: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> CreateEnrollmentToken:
        """Create an enrollment token for an existing cluster.
        Requires active subscription to the cluster."""
        query = gql("""
            mutation CreateEnrollmentToken($clusterId: ID!, $name: String, $ttl: String) {
              createEnrollmentToken(clusterId: $clusterId, name: $name, ttl: $ttl) {
                __typename
                ...CreateEnrollmentTokenResponseDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment CreateEnrollmentTokenResponseDefaultFields on CreateEnrollmentTokenResponse {
              bootstrapToken {
                id
                name
                token
                kind
                clusterId
                expectedIp
                metadata
                usageLimit
                usageCount
                expiresAt
                usedAt
                createdBy
                createdAt
              }
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "name": name,
            "ttl": ttl,
        }
        response = self.execute(
            query=query,
            operation_name="CreateEnrollmentToken",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateEnrollmentToken.model_validate(data)

    def create_mollie_first_payment(
        self, tier_id: str, method: str, redirect_url: str, **kwargs: Any
    ) -> CreateMollieFirstPayment:
        """Create a Mollie first payment to establish a mandate.
        For iDEAL: User pays via bank → SEPA Direct Debit mandate is created.
        For card: User enters card → card mandate is created.
        After successful payment, call createMollieSubscription to start recurring billing."""
        query = gql("""
            mutation CreateMollieFirstPayment($tierId: ID!, $method: String!, $redirectUrl: String!) {
              createMollieFirstPayment(
                tierId: $tierId
                method: $method
                redirectUrl: $redirectUrl
              ) {
                __typename
                ...MollieFirstPaymentDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MollieFirstPaymentDefaultFields on MollieFirstPayment {
              paymentId
              customerId
              paymentUrl
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "tierId": tier_id,
            "method": method,
            "redirectUrl": redirect_url,
        }
        response = self.execute(
            query=query,
            operation_name="CreateMollieFirstPayment",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateMollieFirstPayment.model_validate(data)

    def create_mollie_subscription(
        self,
        tier_id: str,
        mandate_id: str,
        description: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> CreateMollieSubscription:
        """Create a Mollie subscription after mandate is valid.
        Call this after the first payment webhook confirms mandate creation."""
        query = gql("""
            mutation CreateMollieSubscription($tierId: ID!, $mandateId: String!, $description: String) {
              createMollieSubscription(
                tierId: $tierId
                mandateId: $mandateId
                description: $description
              ) {
                __typename
                ...MollieSubscriptionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MollieSubscriptionDefaultFields on MollieSubscription {
              subscriptionId
              status
              nextPaymentDate
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "tierId": tier_id,
            "mandateId": mandate_id,
            "description": description,
        }
        response = self.execute(
            query=query,
            operation_name="CreateMollieSubscription",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateMollieSubscription.model_validate(data)

    def create_payment(self, input: CreatePaymentInput, **kwargs: Any) -> CreatePayment:
        """Create a payment for subscription or usage."""
        query = gql("""
            mutation CreatePayment($input: CreatePaymentInput!) {
              createPayment(input: $input) {
                __typename
                ...PaymentDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment PaymentDefaultFields on Payment {
              id
              paymentUrl
              walletAddress
              amount
              currency
              method
              status
              expiresAt
              qrCode
              expectedAmountBaseUnits
              expectedAmountToken
              quotedPriceUsd
              quoteSource
              assetSymbol
              network
              quotedAt
              createdAt
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="CreatePayment", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreatePayment.model_validate(data)

    def create_stripe_billing_portal(
        self, return_url: str, **kwargs: Any
    ) -> CreateStripeBillingPortal:
        """Create a Stripe Billing Portal session.
        Returns a URL to redirect the user to manage their subscription."""
        query = gql("""
            mutation CreateStripeBillingPortal($returnUrl: String!) {
              createStripeBillingPortal(returnUrl: $returnUrl) {
                __typename
                ...StripeBillingPortalSessionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment StripeBillingPortalSessionDefaultFields on StripeBillingPortalSession {
              portalUrl
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"returnUrl": return_url}
        response = self.execute(
            query=query,
            operation_name="CreateStripeBillingPortal",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateStripeBillingPortal.model_validate(data)

    def create_stripe_checkout(
        self,
        tier_id: str,
        billing_period: str,
        success_url: str,
        cancel_url: str,
        **kwargs: Any,
    ) -> CreateStripeCheckout:
        """Create a Stripe Checkout Session for subscription setup.
        Returns a URL to redirect the user to Stripe's hosted checkout page.
        After successful payment, user is redirected to successUrl."""
        query = gql("""
            mutation CreateStripeCheckout($tierId: ID!, $billingPeriod: String!, $successUrl: String!, $cancelUrl: String!) {
              createStripeCheckout(
                tierId: $tierId
                billingPeriod: $billingPeriod
                successUrl: $successUrl
                cancelUrl: $cancelUrl
              ) {
                __typename
                ...StripeCheckoutSessionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment StripeCheckoutSessionDefaultFields on StripeCheckoutSession {
              sessionId
              checkoutUrl
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "tierId": tier_id,
            "billingPeriod": billing_period,
            "successUrl": success_url,
            "cancelUrl": cancel_url,
        }
        response = self.execute(
            query=query,
            operation_name="CreateStripeCheckout",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateStripeCheckout.model_validate(data)

    def create_webhook_endpoint(
        self, input: CreateWebhookEndpointInput, **kwargs: Any
    ) -> CreateWebhookEndpoint:
        """Create an outbound webhook endpoint. The response carries the signing secret
        once; it is never returned again. A tenant can have at most 10 endpoints."""
        query = gql("""
            mutation CreateWebhookEndpoint($input: CreateWebhookEndpointInput!) {
              createWebhookEndpoint(input: $input) {
                __typename
                ...WebhookEndpointSecretDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WebhookEndpointSecretDefaultFields on WebhookEndpointSecret {
              endpoint {
                id
                url
                description
                eventTypes
                apiVersion
                status
                disabledReason
                disabledAt
                consecutiveFailures
                failingSince
                lastSuccessAt
                lastFailureAt
                previousSecretExpiresAt
                createdAt
                updatedAt
              }
              secret
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateWebhookEndpoint.model_validate(data)

    def crypto_topup_status_mutation(
        self, topup_id: str, **kwargs: Any
    ) -> CryptoTopupStatusMutation:
        """Check the status of a crypto top-up (for polling).
        Returns current status, confirmations, and credited amount when complete."""
        query = gql("""
            mutation CryptoTopupStatusMutation($topupId: ID!) {
              cryptoTopupStatus(topupId: $topupId) {
                ...CryptoTopupStatusDefaultFields
              }
            }

            fragment CryptoTopupStatusDefaultFields on CryptoTopupStatus {
              id
              depositAddress
              asset
              status
              txHash
              confirmations
              receivedAmountBaseUnits
              receivedAmountToken
              creditedAmountCents
              creditedAmountCurrency
              quoteSource
              network
              expiresAt
              detectedAt
              completedAt
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }
            """)
        variables: dict[str, object] = {"topupId": topup_id}
        response = self.execute(
            query=query,
            operation_name="CryptoTopupStatusMutation",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CryptoTopupStatusMutation.model_validate(data)

    def delete_skipper_conversation(
        self, id: str, **kwargs: Any
    ) -> DeleteSkipperConversation:
        """Delete a Skipper conversation."""
        query = gql("""
            mutation DeleteSkipperConversation($id: ID!) {
              deleteSkipperConversation(id: $id)
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="DeleteSkipperConversation",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return DeleteSkipperConversation.model_validate(data)

    def delete_webhook_endpoint(self, id: str, **kwargs: Any) -> DeleteWebhookEndpoint:
        """Delete an endpoint with its signing secrets and delivery log."""
        query = gql("""
            mutation DeleteWebhookEndpoint($id: ID!) {
              deleteWebhookEndpoint(id: $id) {
                __typename
                ...DeleteSuccessDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment DeleteSuccessDefaultFields on DeleteSuccess {
              success
              deletedId
              pending
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="DeleteWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return DeleteWebhookEndpoint.model_validate(data)

    def disable_webhook_endpoint(
        self, id: str, **kwargs: Any
    ) -> DisableWebhookEndpoint:
        """Disable an endpoint. Its pending deliveries are skipped."""
        query = gql("""
            mutation DisableWebhookEndpoint($id: ID!) {
              disableWebhookEndpoint(id: $id) {
                __typename
                ...WebhookEndpointDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment WebhookEndpointDefaultFields on WebhookEndpoint {
              id
              url
              description
              eventTypes
              apiVersion
              status
              disabledReason
              disabledAt
              consecutiveFailures
              failingSince
              lastSuccessAt
              lastFailureAt
              previousSecretExpiresAt
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="DisableWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return DisableWebhookEndpoint.model_validate(data)

    def enable_webhook_endpoint(self, id: str, **kwargs: Any) -> EnableWebhookEndpoint:
        """Enable a disabled endpoint. Deliveries skipped while it was disabled are not
        sent again; replay them with replayWebhookDeliveries."""
        query = gql("""
            mutation EnableWebhookEndpoint($id: ID!) {
              enableWebhookEndpoint(id: $id) {
                __typename
                ...WebhookEndpointDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment WebhookEndpointDefaultFields on WebhookEndpoint {
              id
              url
              description
              eventTypes
              apiVersion
              status
              disabledReason
              disabledAt
              consecutiveFailures
              failingSince
              lastSuccessAt
              lastFailureAt
              previousSecretExpiresAt
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="EnableWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return EnableWebhookEndpoint.model_validate(data)

    def link_email(self, input: LinkEmailInput, **kwargs: Any) -> LinkEmail:
        """Link an email to a wallet-only account.
        This enables the upgrade path from prepaid to postpaid billing.
        A verification email will be sent to confirm the address."""
        query = gql("""
            mutation LinkEmail($input: LinkEmailInput!) {
              linkEmail(input: $input) {
                __typename
                ...LinkEmailPayloadDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment LinkEmailPayloadDefaultFields on LinkEmailPayload {
              success
              message
              verificationSent
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="LinkEmail", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return LinkEmail.model_validate(data)

    def link_wallet(self, input: WalletLoginInput, **kwargs: Any) -> LinkWallet:
        """Link an additional wallet to the current user's account.
        Requires an existing authenticated session."""
        query = gql("""
            mutation LinkWallet($input: WalletLoginInput!) {
              linkWallet(input: $input) {
                __typename
                ...WalletIdentityDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WalletIdentityDefaultFields on WalletIdentity {
              id
              address
              createdAt
              lastAuthAt
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="LinkWallet", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return LinkWallet.model_validate(data)

    def mark_skipper_reports_read(
        self, ids: Union[Optional[list[str]], UnsetType] = UNSET, **kwargs: Any
    ) -> MarkSkipperReportsRead:
        """Mark Skipper investigation reports as read.
        Pass specific IDs, or omit to mark all as read.
        Returns the number of reports marked."""
        query = gql("""
            mutation MarkSkipperReportsRead($ids: [ID!]) {
              markSkipperReportsRead(ids: $ids)
            }
            """)
        variables: dict[str, object] = {"ids": ids}
        response = self.execute(
            query=query,
            operation_name="MarkSkipperReportsRead",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return MarkSkipperReportsRead.model_validate(data)

    def open_mist_admin_session(
        self, input: OpenMistAdminSessionInput, **kwargs: Any
    ) -> OpenMistAdminSession:
        """Open the MistServer admin UI on a specific edge node. Returns a short-TTL
        session token plus the per-edge POST URL the webapp must submit the token
        to (NOT a query parameter, so the token doesn't leak via referrers / URL
        history / access logs). On success the browser sets a `fw_mist_admin`
        cookie scoped to /_mist and is redirected to /_mist/.

        Authority is infrastructure ownership, not subscriber access: Mist admin
        access is effectively shell on the edge box, so only owner/admin users
        in the cluster owner tenant can open it. Holders of the platform_operator
        grant are allowed as break-glass. Anything else returns AuthError without
        exposing whether the node exists."""
        query = gql("""
            mutation OpenMistAdminSession($input: OpenMistAdminSessionInput!) {
              openMistAdminSession(input: $input) {
                __typename
                ...MistAdminSessionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MistAdminSessionDefaultFields on MistAdminSession {
              postUrl
              sessionToken
              expiresAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="OpenMistAdminSession",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return OpenMistAdminSession.model_validate(data)

    def promote_to_paid(self, tier_id: str, **kwargs: Any) -> PromoteToPaid:
        """Switch from prepaid to a selected postpaid tier.
        A verified email is always required. Free needs no billing profile or payment
        provider; paid tiers require confirmed provider collection. Existing prepaid
        balance is carried forward as account credit."""
        query = gql("""
            mutation PromoteToPaid($tierId: ID!) {
              promoteToPaid(tierId: $tierId) {
                __typename
                ...PromoteToPaidPayloadDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment PromoteToPaidPayloadDefaultFields on PromoteToPaidPayload {
              success
              message
              newBillingModel
              creditBalanceCents
              subscriptionId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"tierId": tier_id}
        response = self.execute(
            query=query, operation_name="PromoteToPaid", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return PromoteToPaid.model_validate(data)

    def reject_cluster_subscription(
        self,
        subscription_id: str,
        reason: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> RejectClusterSubscription:
        """Reject a pending cluster subscription request."""
        query = gql("""
            mutation RejectClusterSubscription($subscriptionId: ID!, $reason: String) {
              rejectClusterSubscription(subscriptionId: $subscriptionId, reason: $reason) {
                __typename
                ...ClusterSubscriptionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterSubscriptionDefaultFields on ClusterSubscription {
              id
              tenantId
              clusterId
              accessLevel
              subscriptionStatus
              resourceLimits
              requestedAt
              approvedAt
              approvedBy
              rejectionReason
              expiresAt
              createdAt
              updatedAt
              clusterName
              tenantName
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "subscriptionId": subscription_id,
            "reason": reason,
        }
        response = self.execute(
            query=query,
            operation_name="RejectClusterSubscription",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RejectClusterSubscription.model_validate(data)

    def replay_webhook_deliveries(
        self,
        endpoint_id: str,
        created_after: datetime,
        created_before: datetime,
        **kwargs: Any,
    ) -> ReplayWebhookDeliveries:
        """Send again the failed and skipped deliveries of one endpoint created in
        [createdAfter, createdBefore), oldest first, at most 1000 per call. Call
        again while hasMore is true."""
        query = gql("""
            mutation ReplayWebhookDeliveries($endpointId: ID!, $createdAfter: Time!, $createdBefore: Time!) {
              replayWebhookDeliveries(
                endpointId: $endpointId
                createdAfter: $createdAfter
                createdBefore: $createdBefore
              ) {
                __typename
                ...WebhookReplayResultDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WebhookReplayResultDefaultFields on WebhookReplayResult {
              replayedCount
              hasMore
            }
            """)
        variables: dict[str, object] = {
            "endpointId": endpoint_id,
            "createdAfter": created_after,
            "createdBefore": created_before,
        }
        response = self.execute(
            query=query,
            operation_name="ReplayWebhookDeliveries",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ReplayWebhookDeliveries.model_validate(data)

    def replay_webhook_delivery(self, id: str, **kwargs: Any) -> ReplayWebhookDelivery:
        """Send a finished delivery again under the same ID, so the receiver sees the
        same webhook-id. The endpoint must be enabled."""
        query = gql("""
            mutation ReplayWebhookDelivery($id: ID!) {
              replayWebhookDelivery(id: $id) {
                __typename
                ...WebhookDeliveryDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WebhookDeliveryDefaultFields on WebhookDelivery {
              id
              endpointId
              eventId
              eventType
              kind
              status
              attempts
              nextAttemptAt
              lastStatusCode
              lastErrorClass
              deliveredAt
              replayCount
              lastReplayedAt
              createdAt
              updatedAt
              attemptHistory {
                id
                attemptNumber
                statusCode
                errorClass
                latencyMs
                responseExcerpt
                attemptedAt
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="ReplayWebhookDelivery",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ReplayWebhookDelivery.model_validate(data)

    def request_cluster_subscription(
        self,
        cluster_id: str,
        invite_token: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> RequestClusterSubscription:
        """Request to subscribe to a cluster."""
        query = gql("""
            mutation RequestClusterSubscription($clusterId: ID!, $inviteToken: String) {
              requestClusterSubscription(clusterId: $clusterId, inviteToken: $inviteToken) {
                __typename
                ...ClusterSubscriptionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterSubscriptionDefaultFields on ClusterSubscription {
              id
              tenantId
              clusterId
              accessLevel
              subscriptionStatus
              resourceLimits
              requestedAt
              approvedAt
              approvedBy
              rejectionReason
              expiresAt
              createdAt
              updatedAt
              clusterName
              tenantName
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "inviteToken": invite_token,
        }
        response = self.execute(
            query=query,
            operation_name="RequestClusterSubscription",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RequestClusterSubscription.model_validate(data)

    def reset_media_retention_override(
        self, input: ResetMediaRetentionOverrideInput, **kwargs: Any
    ) -> ResetMediaRetentionOverride:
        """Clear a per-asset retention override and recompute the horizon from the
        tenant default (or tier entitlement when no tenant default is set)."""
        query = gql("""
            mutation ResetMediaRetentionOverride($input: ResetMediaRetentionOverrideInput!) {
              resetMediaRetentionOverride(input: $input) {
                __typename
                ...EffectiveRetentionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment EffectiveRetentionDefaultFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="ResetMediaRetentionOverride",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ResetMediaRetentionOverride.model_validate(data)

    def resolve_incident(self, id: str, **kwargs: Any) -> ResolveIncident:
        """Resolve an incident manually. Repeats of the same firing alerts do not
        reopen it; a new alert opens a new incident."""
        query = gql("""
            mutation ResolveIncident($id: ID!) {
              resolveIncident(id: $id) {
                __typename
                ...IncidentDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment IncidentDefaultFields on Incident {
              id
              scope
              tenantId
              clusterId
              region
              alertname
              severity
              status
              resolution
              title
              summary
              firingAlertCount
              startedAt
              lastAlertAt
              acknowledgedAt
              acknowledgedBy
              assignedTo
              resolvedAt
              resolvedBy
              createdAt
              updatedAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="ResolveIncident", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ResolveIncident.model_validate(data)

    def revoke_cluster_invite(
        self, invite_id: str, **kwargs: Any
    ) -> RevokeClusterInvite:
        """Revoke a cluster invite."""
        query = gql("""
            mutation RevokeClusterInvite($inviteId: ID!) {
              revokeClusterInvite(inviteId: $inviteId) {
                __typename
                ...DeleteSuccessDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment DeleteSuccessDefaultFields on DeleteSuccess {
              success
              deletedId
              pending
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"inviteId": invite_id}
        response = self.execute(
            query=query,
            operation_name="RevokeClusterInvite",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RevokeClusterInvite.model_validate(data)

    def rotate_webhook_endpoint_secret(
        self,
        id: str,
        revoke_previous: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> RotateWebhookEndpointSecret:
        """Replace an endpoint's signing secret and return the new one once. The
        previous secret keeps signing alongside it for 24 hours, unless
        revokePrevious is true."""
        query = gql("""
            mutation RotateWebhookEndpointSecret($id: ID!, $revokePrevious: Boolean = false) {
              rotateWebhookEndpointSecret(id: $id, revokePrevious: $revokePrevious) {
                __typename
                ...WebhookEndpointSecretDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment WebhookEndpointSecretDefaultFields on WebhookEndpointSecret {
              endpoint {
                id
                url
                description
                eventTypes
                apiVersion
                status
                disabledReason
                disabledAt
                consecutiveFailures
                failingSince
                lastSuccessAt
                lastFailureAt
                previousSecretExpiresAt
                createdAt
                updatedAt
              }
              secret
            }
            """)
        variables: dict[str, object] = {"id": id, "revokePrevious": revoke_previous}
        response = self.execute(
            query=query,
            operation_name="RotateWebhookEndpointSecret",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RotateWebhookEndpointSecret.model_validate(data)

    def send_message(self, input: SendMessageInput, **kwargs: Any) -> SendMessage:
        """Send a message in an existing conversation.
        Messages are delivered to support agents in real-time."""
        query = gql("""
            mutation SendMessage($input: SendMessageInput!) {
              sendMessage(input: $input) {
                __typename
                ...MessageDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MessageDefaultFields on Message {
              id
              conversationId
              content
              sender
              createdAt
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="SendMessage", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return SendMessage.model_validate(data)

    def set_media_retention_policy(
        self, input: SetMediaRetentionPolicyInput, **kwargs: Any
    ) -> SetMediaRetentionPolicy:
        """Set the tenant per-class retention default. `targetType` picks the asset
        class (VOD, DVR, or CLIP). `days` is in [0, tier cap] where 0 means "keep
        forever" — only honored on uncapped (paid) tiers; Free clamps to its cap
        at write time. `clear: true` NULLs the column so the tenant inherits the
        per-class system default (VOD: keep forever; DVR/clip: 30d). The tier
        cap is exposed via `mediaRetentionPolicy.bounds.maxRecordingRetentionDays`."""
        query = gql("""
            mutation SetMediaRetentionPolicy($input: SetMediaRetentionPolicyInput!) {
              setMediaRetentionPolicy(input: $input) {
                __typename
                ...MediaRetentionPolicyDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaRetentionPolicyDefaultFields on MediaRetentionPolicy {
              bounds {
                maxRecordingRetentionDays
              }
              updatedBy
              updatedAt
              defaultVodRetentionDays
              defaultDvrRetentionDays
              defaultClipRetentionDays
              effectiveVodRetentionDays
              effectiveDvrRetentionDays
              effectiveClipRetentionDays
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="SetMediaRetentionPolicy",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SetMediaRetentionPolicy.model_validate(data)

    def set_node_mode(self, input: SetNodeModeInput, **kwargs: Any) -> SetNodeMode:
        """Set a node's operational mode. Drains/maintenance bleed traffic away from
        the node; restoring with NORMAL re-admits it to routing. Reason is
        recorded in Foghorn's audit trail."""
        query = gql("""
            mutation SetNodeMode($input: SetNodeModeInput!) {
              setNodeMode(input: $input) {
                __typename
                ...InfrastructureNodeDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment InfrastructureNodeDefaultFields on InfrastructureNode {
              id
              nodeId
              clusterId
              nodeName
              nodeType
              internalIp
              externalIp
              wireguardIp
              wireguardPublicKey
              region
              latitude
              longitude
              availabilityZone
              cpuCores
              memoryGb
              diskGb
              lastHeartbeat
              tags
              metadata
              createdAt
              updatedAt
              liveState {
                nodeId
                tenantId
                cpuPercent
                ramUsedBytes
                ramTotalBytes
                diskUsedBytes
                diskTotalBytes
                upSpeed
                downSpeed
                activeStreams
                isHealthy
                latitude
                longitude
                location
                metadata
                updatedAt
              }
              effectiveMode
              routingImpactPreview {
                activeStreams
                activeViewers
              }
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="SetNodeMode", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return SetNodeMode.model_validate(data)

    def set_preferred_cluster(
        self, cluster_id: str, **kwargs: Any
    ) -> SetPreferredCluster:
        """Set the tenant's preferred cluster. Must be a subscribed cluster.
        Used by routing services when choosing tenant-preferred infrastructure."""
        query = gql("""
            mutation SetPreferredCluster($clusterId: ID!) {
              setPreferredCluster(clusterId: $clusterId) {
                __typename
                ...ClusterDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterDefaultFields on Cluster {
              id
              clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="SetPreferredCluster",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SetPreferredCluster.model_validate(data)

    def set_stream_retention_overrides(
        self, input: SetStreamRetentionOverridesInput, **kwargs: Any
    ) -> SetStreamRetentionOverrides:
        """Write per-stream retention overrides for DVR and clip artifacts created
        on this stream. Unspecified input fields are left alone; passing -1 on
        a field clears the override (falls back to the tenant default). Values
        exceeding the tier cap are clamped at write time."""
        query = gql("""
            mutation SetStreamRetentionOverrides($input: SetStreamRetentionOverridesInput!) {
              setStreamRetentionOverrides(input: $input) {
                __typename
                ...StreamRetentionOverridesDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment StreamRetentionOverridesDefaultFields on StreamRetentionOverrides {
              streamId
              dvrRetentionDaysOverride
              clipRetentionDaysOverride
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="SetStreamRetentionOverrides",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SetStreamRetentionOverrides.model_validate(data)

    def submit_x_402_payment(
        self,
        payment: str,
        resource: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> SubmitX402Payment:
        """Submit an x402 payment payload to settle a 402 response or top up balance.
        Authenticated members may settle viewer resources; direct top-ups and
        non-viewer resources require billing management authority on the target tenant."""
        query = gql("""
            mutation SubmitX402Payment($payment: String!, $resource: String) {
              submitX402Payment(payment: $payment, resource: $resource) {
                __typename
                ...X402PaymentResultDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment X402PaymentResultDefaultFields on X402PaymentResult {
              success
              isAuthOnly
              tenantId
              walletAddress
              creditedCents
              newBalanceCents
              txHash
              message
            }
            """)
        variables: dict[str, object] = {"payment": payment, "resource": resource}
        response = self.execute(
            query=query,
            operation_name="SubmitX402Payment",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SubmitX402Payment.model_validate(data)

    def subscribe_to_cluster(
        self, cluster_id: str, **kwargs: Any
    ) -> SubscribeToCluster:
        """Subscribe to a cluster for streaming access."""
        query = gql("""
            mutation SubscribeToCluster($clusterId: ID!) {
              subscribeToCluster(clusterId: $clusterId)
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="SubscribeToCluster",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SubscribeToCluster.model_validate(data)

    def test_webhook_endpoint(self, id: str, **kwargs: Any) -> TestWebhookEndpoint:
        """Send a signed webhook.test event to the endpoint and wait for the result.
        At most one test per endpoint every 10 seconds."""
        query = gql("""
            mutation TestWebhookEndpoint($id: ID!) {
              testWebhookEndpoint(id: $id) {
                __typename
                ...WebhookTestResultDefaultFields
                ...NotFoundErrorDefaultFields
                ...RateLimitErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment RateLimitErrorDefaultFields on RateLimitError {
              message
              code
              retryAfter
            }

            fragment WebhookTestResultDefaultFields on WebhookTestResult {
              delivery {
                id
                endpointId
                eventId
                eventType
                kind
                status
                attempts
                nextAttemptAt
                lastStatusCode
                lastErrorClass
                deliveredAt
                replayCount
                lastReplayedAt
                createdAt
                updatedAt
                attemptHistory {
                  id
                  attemptNumber
                  statusCode
                  errorClass
                  latencyMs
                  responseExcerpt
                  attemptedAt
                }
              }
              attempt {
                id
                attemptNumber
                statusCode
                errorClass
                latencyMs
                responseExcerpt
                attemptedAt
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="TestWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return TestWebhookEndpoint.model_validate(data)

    def unlink_wallet(self, wallet_id: str, **kwargs: Any) -> UnlinkWallet:
        """Unlink a wallet from the current user's account.
        Cannot unlink the last wallet if user has no email."""
        query = gql("""
            mutation UnlinkWallet($walletId: ID!) {
              unlinkWallet(walletId: $walletId) {
                __typename
                ...DeleteSuccessDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment DeleteSuccessDefaultFields on DeleteSuccess {
              success
              deletedId
              pending
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"walletId": wallet_id}
        response = self.execute(
            query=query, operation_name="UnlinkWallet", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return UnlinkWallet.model_validate(data)

    def unsubscribe_from_cluster(
        self, cluster_id: str, **kwargs: Any
    ) -> UnsubscribeFromCluster:
        """Unsubscribe from a cluster."""
        query = gql("""
            mutation UnsubscribeFromCluster($clusterId: ID!) {
              unsubscribeFromCluster(clusterId: $clusterId)
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="UnsubscribeFromCluster",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UnsubscribeFromCluster.model_validate(data)

    def update_billing_details(
        self, input: UpdateBillingDetailsInput, **kwargs: Any
    ) -> UpdateBillingDetails:
        """Update billing details for the current tenant.
        Required before any payment for VAT invoicing."""
        query = gql("""
            mutation UpdateBillingDetails($input: UpdateBillingDetailsInput!) {
              updateBillingDetails(input: $input) {
                ...BillingDetailsDefaultFields
              }
            }

            fragment BillingDetailsDefaultFields on BillingDetails {
              email
              name
              company
              vatNumber
              address {
                street
                city
                state
                postalCode
                country
              }
              isComplete
              updatedAt
              presentmentCurrency
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="UpdateBillingDetails",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdateBillingDetails.model_validate(data)

    def update_cluster_marketplace(
        self, cluster_id: str, input: UpdateClusterMarketplaceInput, **kwargs: Any
    ) -> UpdateClusterMarketplace:
        """Update marketplace settings for a cluster."""
        query = gql("""
            mutation UpdateClusterMarketplace($clusterId: ID!, $input: UpdateClusterMarketplaceInput!) {
              updateClusterMarketplace(clusterId: $clusterId, input: $input) {
                __typename
                ...ClusterDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment ClusterDefaultFields on Cluster {
              id
              clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id, "input": input}
        response = self.execute(
            query=query,
            operation_name="UpdateClusterMarketplace",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdateClusterMarketplace.model_validate(data)

    def update_media_retention(
        self, input: UpdateMediaRetentionInput, **kwargs: Any
    ) -> UpdateMediaRetention:
        """Apply a per-asset retention override on a finalized DVR recording, clip,
        or VOD asset (set targetType accordingly). Override beats tenant default
        beats tier entitlement. Active assets are rejected — retention applies
        post-finalize."""
        query = gql("""
            mutation UpdateMediaRetention($input: UpdateMediaRetentionInput!) {
              updateMediaRetention(input: $input) {
                __typename
                ...EffectiveRetentionDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment EffectiveRetentionDefaultFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="UpdateMediaRetention",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdateMediaRetention.model_validate(data)

    def update_skipper_conversation(
        self, id: str, title: str, **kwargs: Any
    ) -> UpdateSkipperConversation:
        """Update the title of a Skipper conversation."""
        query = gql("""
            mutation UpdateSkipperConversation($id: ID!, $title: String!) {
              updateSkipperConversation(id: $id, title: $title) {
                ...SkipperConversationSummaryDefaultFields
              }
            }

            fragment SkipperConversationSummaryDefaultFields on SkipperConversationSummary {
              id
              title
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id, "title": title}
        response = self.execute(
            query=query,
            operation_name="UpdateSkipperConversation",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdateSkipperConversation.model_validate(data)

    def update_tenant(self, input: UpdateTenantInput, **kwargs: Any) -> UpdateTenant:
        """Update the current tenant's profile."""
        query = gql("""
            mutation UpdateTenant($input: UpdateTenantInput!) {
              updateTenant(input: $input) {
                __typename
                ...TenantDefaultFields
                ...ValidationErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment TenantDefaultFields on Tenant {
              id
              name
              subdomain
              cluster
              createdAt
              customDomain
              customDomainStatus {
                domain
                state
                requiredTrafficCname
                requiredAcmeChallengeCname
                lastVerifiedAt
                certIssuedAt
                certExpiresAt
                lastError
              }
              monitoringEnabled
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="UpdateTenant", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return UpdateTenant.model_validate(data)

    def update_webhook_endpoint(
        self, id: str, input: UpdateWebhookEndpointInput, **kwargs: Any
    ) -> UpdateWebhookEndpoint:
        """Change an endpoint's URL, description, or event types. Omitted fields keep
        their value."""
        query = gql("""
            mutation UpdateWebhookEndpoint($id: ID!, $input: UpdateWebhookEndpointInput!) {
              updateWebhookEndpoint(id: $id, input: $input) {
                __typename
                ...WebhookEndpointDefaultFields
                ...ValidationErrorDefaultFields
                ...NotFoundErrorDefaultFields
                ...AuthErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WebhookEndpointDefaultFields on WebhookEndpoint {
              id
              url
              description
              eventTypes
              apiVersion
              status
              disabledReason
              disabledAt
              consecutiveFailures
              failingSince
              lastSuccessAt
              lastFailureAt
              previousSecretExpiresAt
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id, "input": input}
        response = self.execute(
            query=query,
            operation_name="UpdateWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdateWebhookEndpoint.model_validate(data)

    def wallet_login(self, input: WalletLoginInput, **kwargs: Any) -> WalletLogin:
        """Authenticate using a signed message from an Ethereum wallet.
        Returns a JWT token for API access. Creates a new account if the wallet
        has not been seen before (auto-provisioning)."""
        query = gql("""
            mutation WalletLogin($input: WalletLoginInput!) {
              walletLogin(input: $input) {
                __typename
                ...WalletLoginPayloadDefaultFields
                ...ValidationErrorDefaultFields
              }
            }

            fragment ValidationErrorDefaultFields on ValidationError {
              message
              code
              field
              constraint
            }

            fragment WalletLoginPayloadDefaultFields on WalletLoginPayload {
              token
              user {
                id
                email
                name
                role
                createdAt
                wallets {
                  id
                  address
                  createdAt
                  lastAuthAt
                }
              }
              expiresAt
              isNewAccount
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="WalletLogin", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return WalletLogin.model_validate(data)

    def get_analytics_infra_service_instances_health(
        self, service_id: Union[Optional[str], UnsetType] = UNSET, **kwargs: Any
    ) -> GetAnalyticsInfraServiceInstancesHealth:
        query = gql("""
            query GetAnalyticsInfraServiceInstancesHealth($serviceId: String) {
              analytics {
                infra {
                  serviceInstancesHealth(serviceId: $serviceId) {
                    ...ServiceInstanceHealthDefaultFields
                  }
                }
              }
            }

            fragment ServiceInstanceHealthDefaultFields on ServiceInstanceHealth {
              instanceId
              serviceId
              clusterId
              protocol
              host
              port
              healthEndpoint
              status
              lastHealthCheck
            }
            """)
        variables: dict[str, object] = {"serviceId": service_id}
        response = self.execute(
            query=query,
            operation_name="GetAnalyticsInfraServiceInstancesHealth",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetAnalyticsInfraServiceInstancesHealth.model_validate(data)

    def get_api_usage_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        auth_type: Union[Optional[str], UnsetType] = UNSET,
        operation_type: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetApiUsageConnection:
        query = gql("""
            query GetApiUsageConnection($page: ConnectionInput, $authType: String, $operationType: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  api {
                    apiUsageConnection(
                      page: $page
                      authType: $authType
                      operationType: $operationType
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...APIUsageRecordDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                      summaries {
                        date
                        authType
                        totalRequests
                        totalErrors
                        avgDurationMs
                        totalComplexity
                        uniqueUsers
                        uniqueTokens
                      }
                      operationSummaries {
                        operationType
                        totalRequests
                        totalErrors
                        uniqueOperations
                        avgDurationMs
                        totalComplexity
                      }
                    }
                  }
                }
              }
            }

            fragment APIUsageRecordDefaultFields on APIUsageRecord {
              id
              timestamp
              authType
              operationType
              operationName
              requestCount
              errorCount
              totalDurationMs
              totalComplexity
              uniqueUsers
              uniqueTokens
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "authType": auth_type,
            "operationType": operation_type,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetApiUsageConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetApiUsageConnection.model_validate(data)

    def get_artifact_events_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        stage: Union[Optional[str], UnsetType] = UNSET,
        content_type: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetArtifactEventsConnection:
        query = gql("""
            query GetArtifactEventsConnection($page: ConnectionInput, $streamId: ID, $stage: String, $contentType: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  artifactEventsConnection(
                    page: $page
                    streamId: $streamId
                    stage: $stage
                    contentType: $contentType
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...ArtifactEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment ArtifactEventDefaultFields on ArtifactEvent {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              playbackId
              stage
              contentType
              startUnix
              stopUnix
              ingestNodeId
              percent
              message
              filePath
              s3Url
              sizeBytes
              expiresAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "stage": stage,
            "contentType": content_type,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetArtifactEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetArtifactEventsConnection.model_validate(data)

    def get_artifact_node_copies(
        self, artifact_hash: str, **kwargs: Any
    ) -> GetArtifactNodeCopies:
        """Nodes currently holding a transient LOCAL COPY of one artifact (not the durable
        object-storage copy), from the node-copy telemetry the media plane emits. `role`
        is `origin` (producer/relay source) or `cache` (synced pull); `isComplete` marks a
        full local copy. Read-through relay block caches are not represented. Node geo is
        enriched from the infrastructure registry. `artifactHash` is required. When
        `truncated` is true the node set was capped and is NOT exhaustive."""
        query = gql("""
            query GetArtifactNodeCopies($artifactHash: String!) {
              analytics {
                health {
                  artifactNodeCopies(artifactHash: $artifactHash) {
                    ...AssetNodeCopiesDefaultFields
                  }
                }
              }
            }

            fragment AssetNodeCopiesDefaultFields on AssetNodeCopies {
              copies {
                nodeId
                nodeName
                clusterId
                region
                latitude
                longitude
                role
                isComplete
                sizeBytes
                updatedAt
              }
              truncated
            }
            """)
        variables: dict[str, object] = {"artifactHash": artifact_hash}
        response = self.execute(
            query=query,
            operation_name="GetArtifactNodeCopies",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetArtifactNodeCopies.model_validate(data)

    def get_artifact_states_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        content_type: Union[Optional[str], UnsetType] = UNSET,
        stage: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetArtifactStatesConnection:
        query = gql("""
            query GetArtifactStatesConnection($page: ConnectionInput, $streamId: ID, $contentType: String, $stage: String) {
              analytics {
                lifecycle {
                  artifactStatesConnection(
                    page: $page
                    streamId: $streamId
                    contentType: $contentType
                    stage: $stage
                  ) {
                    edges {
                      cursor
                      node {
                        ...ArtifactStateDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment ArtifactStateDefaultFields on ArtifactState {
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              playbackId
              contentType
              stage
              progressPercent
              errorMessage
              requestedAt
              startedAt
              completedAt
              clipStartUnix
              clipStopUnix
              segmentCount
              manifestPath
              filePath
              s3Url
              sizeBytes
              processingNodeId
              expiresAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "contentType": content_type,
            "stage": stage,
        }
        response = self.execute(
            query=query,
            operation_name="GetArtifactStatesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetArtifactStatesConnection.model_validate(data)

    def get_balance_transactions_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        transaction_type: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetBalanceTransactionsConnection:
        """List balance transactions for the tenant with pagination."""
        query = gql("""
            query GetBalanceTransactionsConnection($page: ConnectionInput, $transactionType: String, $timeRange: TimeRangeInput) {
              balanceTransactionsConnection(
                page: $page
                transactionType: $transactionType
                timeRange: $timeRange
              ) {
                edges {
                  cursor
                  node {
                    ...BalanceTransactionDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment BalanceTransactionDefaultFields on BalanceTransaction {
              id
              tenantId
              amountCents
              balanceAfterCents
              transactionType
              description
              referenceId
              referenceType
              createdAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "transactionType": transaction_type,
            "timeRange": time_range,
        }
        response = self.execute(
            query=query,
            operation_name="GetBalanceTransactionsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetBalanceTransactionsConnection.model_validate(data)

    def get_billing_details(self, **kwargs: Any) -> GetBillingDetails:
        """Get billing details for the current tenant.
        Email and address are required before funding or postpaid setup. A VAT/tax
        identifier is optional customer-supplied data and is not automatically verified."""
        query = gql("""
            query GetBillingDetails {
              billingDetails {
                ...BillingDetailsDefaultFields
              }
            }

            fragment BillingDetailsDefaultFields on BillingDetails {
              email
              name
              company
              vatNumber
              address {
                street
                city
                state
                postalCode
                country
              }
              isComplete
              updatedAt
              presentmentCurrency
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetBillingDetails",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetBillingDetails.model_validate(data)

    def get_billing_status(self, **kwargs: Any) -> GetBillingStatus:
        """Get the current billing status including subscription tier and usage."""
        query = gql("""
            query GetBillingStatus {
              billingStatus {
                ...BillingStatusDefaultFields
              }
            }

            fragment BillingStatusDefaultFields on BillingStatus {
              currentTier {
                id
                tierName
                tierLevel
                displayName
                description
                basePrice
                currency
                billingPeriod
                features {
                  supportLevel
                  sla
                  processingCustomizable
                }
                pricingRules {
                  meter
                  model
                  currency
                  includedQuantity
                  unitPrice
                  configJson
                }
                entitlements {
                  key
                  value
                }
                supportLevel
                slaLevel
                meteringEnabled
                isEnterprise
              }
              subscription {
                id
                tenantId
                tierId
                status
                billingEmail
                startedAt
                trialEndsAt
                nextBillingDate
                cancelledAt
                customFeatures {
                  supportLevel
                  sla
                  processingCustomizable
                }
                pricingOverrides {
                  meter
                  model
                  currency
                  includedQuantity
                  unitPrice
                  configJson
                }
                entitlementOverrides {
                  key
                  value
                }
                paymentMethod
                billingModel
                pendingTier {
                  id
                  tierName
                  tierLevel
                  displayName
                  description
                  basePrice
                  currency
                  billingPeriod
                  supportLevel
                  slaLevel
                  meteringEnabled
                  isEnterprise
                }
                pendingEffectiveAt
                pendingReason
                presentmentCurrency
                createdAt
                updatedAt
              }
              billingStatus
              paymentMethods
              collectionReady
              collectionProvider
              setupProviders
              recentPayments {
                id
                invoiceId
                method
                amount
                currency
                status
                confirmedAt
                createdAt
                updatedAt
                conversion {
                  originalAmountCents
                  originalCurrency
                  eurAmountCents
                  unitsPerEur
                  source
                  referenceDate
                }
              }
              nextBillingDate
              trialEndsAt
              outstandingAmount
              currency
              liveUsage {
                tenantId
                periodStart
                periodEnd
                streamHours
                egressGb
                peakBandwidthMbps
                displayStorageGb
                livepeerH264Seconds
                livepeerVp9Seconds
                livepeerAv1Seconds
                livepeerHevcSeconds
                nativeAvH264Seconds
                nativeAvVp9Seconds
                nativeAvAv1Seconds
                nativeAvHevcSeconds
                nativeAvAacSeconds
                nativeAvOpusSeconds
                totalStreams
                totalViewers
                viewerHours
                maxViewers
                uniqueUsers
                livepeerSegmentCount
                livepeerUniqueStreams
                nativeAvSegmentCount
                nativeAvUniqueStreams
                uniqueCountries
                uniqueCities
                geoBreakdown {
                  countryCode
                  viewerCount
                  viewerHours
                  egressGb
                }
                clipsCreated
                clipsDeleted
                dvrCreated
                dvrDeleted
                vodCreated
                vodDeleted
                clipBytes
                dvrBytes
                vodBytes
                frozenClipBytes
                frozenDvrBytes
                frozenVodBytes
                syncedArtifactCount
                syncedArtifactBytes
              }
              invoicePreview {
                id
                amount
                baseAmount
                meteredAmount
                grossMeteredAmount
                prepaidCreditApplied
                currency
                presentmentAmountCents
                presentmentCurrency
                presentmentUnitsPerEur
                presentmentReferenceDate
                finalizedAt
                status
                dueDate
                paidAt
                createdAt
                updatedAt
                periodStart
                periodEnd
                usageDetails
                lineItems {
                  lineKey
                  meter
                  description
                  quantity
                  includedQuantity
                  billableQuantity
                  unitPrice
                  total
                  currency
                  clusterId
                  clusterName
                  clusterKind
                  pricingSource
                  pricingLabel
                  unit
                  dimensions
                }
              }
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetBillingStatus",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetBillingStatus.model_validate(data)

    def get_billing_tiers(self, **kwargs: Any) -> GetBillingTiers:
        """List available billing tiers and their pricing."""
        query = gql("""
            query GetBillingTiers {
              billingTiers {
                ...BillingTierDefaultFields
              }
            }

            fragment BillingTierDefaultFields on BillingTier {
              id
              tierName
              tierLevel
              displayName
              description
              basePrice
              currency
              billingPeriod
              features {
                supportLevel
                sla
                processingCustomizable
              }
              pricingRules {
                meter
                model
                currency
                includedQuantity
                unitPrice
                configJson
              }
              entitlements {
                key
                value
              }
              supportLevel
              slaLevel
              meteringEnabled
              isEnterprise
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query, operation_name="GetBillingTiers", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetBillingTiers.model_validate(data)

    def get_buffer_events_connection(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetBufferEventsConnection:
        query = gql("""
            query GetBufferEventsConnection($page: ConnectionInput, $streamId: ID!, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  bufferEventsConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...BufferEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment BufferEventDefaultFields on BufferEvent {
              id
              eventId
              timestamp
              nodeId
              bufferState
              eventData
              payload
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetBufferEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetBufferEventsConnection.model_validate(data)

    def get_capabilities(self, **kwargs: Any) -> GetCapabilities:
        """What the current tenant can use, built only from gates the platform enforces.
        Describes enforcement; it never grants access itself."""
        query = gql("""
            query GetCapabilities {
              capabilities {
                ...CapabilitiesDefaultFields
              }
            }

            fragment CapabilitiesDefaultFields on Capabilities {
              tenant {
                platformOperator
                recordingRetention {
                  capped
                  maxDays
                }
                processingCustomizable
                customSubdomain
                customDomain
              }
              clusters {
                clusterId
                clusterName
                role
                accessLevel
                media {
                  ingest
                  playback
                  storage
                  processing
                }
              }
              observedAt
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query, operation_name="GetCapabilities", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetCapabilities.model_validate(data)

    def get_client_qoe_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClientQoeConnection:
        query = gql("""
            query GetClientQoeConnection($page: ConnectionInput, $streamId: ID, $nodeId: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  clientQoeConnection(
                    page: $page
                    streamId: $streamId
                    nodeId: $nodeId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...ClientMetrics5mDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment ClientMetrics5mDefaultFields on ClientMetrics5m {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              activeSessions
              avgBandwidthIn
              avgBandwidthOut
              avgConnectionTime
              packetLossRate
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "nodeId": node_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetClientQoeConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClientQoeConnection.model_validate(data)

    def get_client_qoe_summary(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClientQoeSummary:
        """Pre-aggregated client QoE summary for dashboard views.
        Replaces paginated clientQoeConnection when only scalar stats are needed."""
        query = gql("""
            query GetClientQoeSummary($streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  clientQoeSummary(streamId: $streamId, timeRange: $timeRange, noCache: $noCache) {
                    ...ClientQoeSummaryDefaultFields
                  }
                }
              }
            }

            fragment ClientQoeSummaryDefaultFields on ClientQoeSummary {
              avgPacketLossRate
              peakPacketLossRate
              avgBandwidthIn
              avgBandwidthOut
              avgConnectionTime
              totalActiveSessions
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetClientQoeSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClientQoeSummary.model_validate(data)

    def get_cluster(self, id: str, **kwargs: Any) -> GetCluster:
        """Fetch a single cluster by ID."""
        query = gql("""
            query GetCluster($id: ID!) {
              cluster(id: $id) {
                ...ClusterDefaultFields
              }
            }

            fragment ClusterDefaultFields on Cluster {
              id
              clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetCluster", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetCluster.model_validate(data)

    def get_cluster_boot_ops(
        self,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterBootOps:
        """Player startup (boot) operations aggregate for clusters the caller owns.
        Aggregate/redacted; only token-attributed boot rows are included."""
        query = gql("""
            query GetClusterBootOps($clusterId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  clusterBootOps(clusterId: $clusterId, timeRange: $timeRange, noCache: $noCache) {
                    ...ClusterBootOpsDefaultFields
                  }
                }
              }
            }

            fragment ClusterBootOpsDefaultFields on ClusterBootOps {
              servingClusterId
              nodeId
              protocol
              bootCount
              errorCount
              p95TtfMs
              cacheHitRatio
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetClusterBootOps",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterBootOps.model_validate(data)

    def get_cluster_invites_connection(
        self,
        cluster_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterInvitesConnection:
        """List invites for a cluster (paginated)."""
        query = gql("""
            query GetClusterInvitesConnection($page: ConnectionInput, $clusterId: ID!) {
              clusterInvitesConnection(page: $page, clusterId: $clusterId) {
                edges {
                  cursor
                  node {
                    ...ClusterInviteDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterInviteDefaultFields on ClusterInvite {
              id
              clusterId
              invitedTenantId
              inviteToken
              accessLevel
              resourceLimits
              status
              createdBy
              createdAt
              expiresAt
              acceptedAt
              invitedTenantName
              clusterName
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page, "clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="GetClusterInvitesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterInvitesConnection.model_validate(data)

    def get_cluster_media_consent(
        self, cluster_id: str, **kwargs: Any
    ) -> GetClusterMediaConsent:
        query = gql("""
            query GetClusterMediaConsent($clusterId: ID!) {
              clusterMediaConsent(clusterId: $clusterId) {
                __typename
                ...MediaCapacityConsentDefaultFields
                ...MediaPlacementErrorInMediaCapacityConsentResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaCapacityConsentDefaultFields on MediaCapacityConsent {
              clusterId
              revision
              allowIngest
              allowServe
              allowExternalSource
              canManage
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
            }

            fragment MediaPlacementErrorInMediaCapacityConsentResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="GetClusterMediaConsent",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterMediaConsent.model_validate(data)

    def get_cluster_media_consent_change(
        self, cluster_id: str, idempotency_key: str, **kwargs: Any
    ) -> GetClusterMediaConsentChange:
        query = gql("""
            query GetClusterMediaConsentChange($clusterId: ID!, $idempotencyKey: String!) {
              clusterMediaConsentChange(
                clusterId: $clusterId
                idempotencyKey: $idempotencyKey
              ) {
                __typename
                ...MediaCapacityConsentChangeDefaultFields
                ...MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaCapacityConsentChangeDefaultFields on MediaCapacityConsentChange {
              clusterId
              idempotencyKey
              revision
              digest
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
              createdAt
            }

            fragment MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "idempotencyKey": idempotency_key,
        }
        response = self.execute(
            query=query,
            operation_name="GetClusterMediaConsentChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterMediaConsentChange.model_validate(data)

    def get_cluster_nodes_connection(
        self,
        id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterNodesConnection:
        """Paginated list of nodes in this cluster."""
        query = gql("""
            query GetClusterNodesConnection($id: ID!, $page: ConnectionInput) {
              cluster(id: $id) {
                nodesConnection(page: $page) {
                  edges {
                    cursor
                    node {
                      ...InfrastructureNodeDefaultFields
                    }
                  }
                  pageInfo {
                    ...PageInfoDefaultFields
                  }
                  totalCount
                }
              }
            }

            fragment InfrastructureNodeDefaultFields on InfrastructureNode {
              id
              nodeId
              clusterId
              nodeName
              nodeType
              internalIp
              externalIp
              wireguardIp
              wireguardPublicKey
              region
              latitude
              longitude
              availabilityZone
              cpuCores
              memoryGb
              diskGb
              lastHeartbeat
              tags
              metadata
              createdAt
              updatedAt
              liveState {
                nodeId
                tenantId
                cpuPercent
                ramUsedBytes
                ramTotalBytes
                diskUsedBytes
                diskTotalBytes
                upSpeed
                downSpeed
                activeStreams
                isHealthy
                latitude
                longitude
                location
                metadata
                updatedAt
              }
              effectiveMode
              routingImpactPreview {
                activeStreams
                activeViewers
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"id": id, "page": page}
        response = self.execute(
            query=query,
            operation_name="GetClusterNodesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterNodesConnection.model_validate(data)

    def get_cluster_qoe_ops(
        self,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterQoeOps:
        """Cluster-ops viewer-QoE aggregate per serving node/protocol, for operators of
        clusters they own. Aggregate/redacted; only token-attributed rows are included."""
        query = gql("""
            query GetClusterQoeOps($clusterId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  clusterQoeOps(clusterId: $clusterId, timeRange: $timeRange, noCache: $noCache) {
                    ...ClusterQoeOpsDefaultFields
                  }
                }
              }
            }

            fragment ClusterQoeOpsDefaultFields on ClusterQoeOps {
              servingClusterId
              nodeId
              protocol
              sessionCount
              rebufferingRatio
              frameDropRatio
              avgBitrateBps
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetClusterQoeOps",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterQoeOps.model_validate(data)

    def get_cluster_traffic_matrix(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterTrafficMatrix:
        """Cross-cluster routing traffic matrix from hourly rollups."""
        query = gql("""
            query GetClusterTrafficMatrix($timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  clusterTrafficMatrix(timeRange: $timeRange, noCache: $noCache) {
                    ...ClusterPairTrafficDefaultFields
                  }
                }
              }
            }

            fragment ClusterPairTrafficDefaultFields on ClusterPairTraffic {
              clusterId
              remoteClusterId
              eventCount
              successCount
              avgLatencyMs
              avgDistanceKm
              successRate
              maxLatencyMs
              localLatitude
              localLongitude
              remoteLatitude
              remoteLongitude
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range, "noCache": no_cache}
        response = self.execute(
            query=query,
            operation_name="GetClusterTrafficMatrix",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterTrafficMatrix.model_validate(data)

    def get_cluster_workload(
        self,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetClusterWorkload:
        """Aggregate/redacted media work on clusters the caller owns. Contains placement
        and volume only; never tenant, content, stream, session, URL, or client IDs."""
        query = gql("""
            query GetClusterWorkload($clusterId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  clusterWorkload(clusterId: $clusterId, timeRange: $timeRange, noCache: $noCache) {
                    ...ClusterWorkloadDefaultFields
                  }
                }
              }
            }

            fragment ClusterWorkloadDefaultFields on ClusterWorkload {
              clusterId
              nodeId
              workKind
              measurementKind
              storageScope
              observedAt
              eventCount
              activeCount
              bytes
              mediaSeconds
              errorCount
            }
            """)
        variables: dict[str, object] = {
            "clusterId": cluster_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetClusterWorkload",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClusterWorkload.model_validate(data)

    def get_clusters_access_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetClustersAccessConnection:
        """List clusters the tenant has access to (paginated)."""
        query = gql("""
            query GetClustersAccessConnection($page: ConnectionInput) {
              clustersAccessConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...ClusterAccessDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterAccessDefaultFields on ClusterAccess {
              clusterId
              clusterName
              accessLevel
              resourceLimits
              allowPrivatePullSources
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetClustersAccessConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClustersAccessConnection.model_validate(data)

    def get_clusters_available_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetClustersAvailableConnection:
        """List available clusters for subscription (paginated)."""
        query = gql("""
            query GetClustersAvailableConnection($page: ConnectionInput) {
              clustersAvailableConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...AvailableClusterDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment AvailableClusterDefaultFields on AvailableCluster {
              clusterId
              clusterName
              tiers
              autoEnroll
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetClustersAvailableConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClustersAvailableConnection.model_validate(data)

    def get_clusters_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetClustersConnection:
        """List clusters the tenant has access to."""
        query = gql("""
            query GetClustersConnection($page: ConnectionInput) {
              clustersConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...ClusterDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterDefaultFields on Cluster {
              id
              clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetClustersConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetClustersConnection.model_validate(data)

    def get_connection_events_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetConnectionEventsConnection:
        query = gql("""
            query GetConnectionEventsConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  connectionEventsConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...ConnectionEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment ConnectionEventDefaultFields on ConnectionEvent {
              id
              eventId
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              sessionId
              connectionAddr
              connector
              nodeId
              countryCode
              city
              latitude
              longitude
              clientBucket {
                h3Index
                resolution
              }
              nodeBucket {
                h3Index
                resolution
              }
              eventType
              requestUrl
              clusterId
              originClusterId
              controlCellId
              sessionDurationSeconds
              bytesTransferred
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetConnectionEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetConnectionEventsConnection.model_validate(data)

    def get_conversation(self, id: str, **kwargs: Any) -> GetConversation:
        """Fetch a single conversation by ID."""
        query = gql("""
            query GetConversation($id: ID!) {
              conversation(id: $id) {
                ...ConversationDefaultFields
              }
            }

            fragment ConversationDefaultFields on Conversation {
              id
              subject
              status
              lastMessage {
                id
                conversationId
                content
                sender
                createdAt
              }
              unreadCount
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetConversation", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetConversation.model_validate(data)

    def get_conversations_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetConversationsConnection:
        """List all support conversations for the current tenant.
        Conversations are ordered by last activity, most recent first."""
        query = gql("""
            query GetConversationsConnection($page: ConnectionInput) {
              conversationsConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...ConversationDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ConversationDefaultFields on Conversation {
              id
              subject
              status
              lastMessage {
                id
                conversationId
                content
                sender
                createdAt
              }
              unreadCount
              createdAt
              updatedAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetConversationsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetConversationsConnection.model_validate(data)

    def get_daily_stats(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        days: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetDailyStats:
        query = gql("""
            query GetDailyStats($timeRange: TimeRangeInput, $days: Int = 7) {
              analytics {
                overview(timeRange: $timeRange) {
                  dailyStats(days: $days) {
                    ...TenantDailyStatDefaultFields
                  }
                }
              }
            }

            fragment TenantDailyStatDefaultFields on TenantDailyStat {
              id
              date
              egressGb
              viewerHours
              uniqueViewers
              totalSessions
              totalViews
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range, "days": days}
        response = self.execute(
            query=query, operation_name="GetDailyStats", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetDailyStats.model_validate(data)

    def get_discover_services_connection(
        self,
        discover_services_connection_type: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetDiscoverServicesConnection:
        """Discover service instances by type."""
        query = gql("""
            query GetDiscoverServicesConnection($page: ConnectionInput, $discoverServicesConnectionType: String!, $clusterId: String) {
              discoverServicesConnection(
                page: $page
                type: $discoverServicesConnectionType
                clusterId: $clusterId
              ) {
                edges {
                  cursor
                  node {
                    ...ServiceInstanceDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ServiceInstanceDefaultFields on ServiceInstance {
              id
              instanceId
              clusterId
              nodeId
              serviceId
              version
              port
              processId
              containerId
              status
              healthStatus
              startedAt
              stoppedAt
              lastHealthCheck
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "discoverServicesConnectionType": discover_services_connection_type,
            "clusterId": cluster_id,
        }
        response = self.execute(
            query=query,
            operation_name="GetDiscoverServicesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetDiscoverServicesConnection.model_validate(data)

    def get_federation_events_connection(
        self,
        time_range: TimeRangeInput,
        first: Union[Optional[int], UnsetType] = UNSET,
        event_type: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetFederationEventsConnection:
        """Federation events: origin pulls, peer connections, leader elections, etc."""
        query = gql("""
            query GetFederationEventsConnection($timeRange: TimeRangeInput!, $first: Int, $eventType: String, $noCache: Boolean = false) {
              analytics {
                infra {
                  federationEventsConnection(
                    timeRange: $timeRange
                    first: $first
                    eventType: $eventType
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...FederationEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment FederationEventDefaultFields on FederationEvent {
              timestamp
              eventType
              localCluster
              remoteCluster
              streamName
              streamId
              sourceNode
              destNode
              dtscUrl
              latencyMs
              timeToLiveMs
              failureReason
              queriedClusters
              respondingClusters
              totalCandidates
              bestRemoteScore
              peerCluster
              role
              reason
              streamTenantId
              controlCellId
              originClusterId
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "timeRange": time_range,
            "first": first,
            "eventType": event_type,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetFederationEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetFederationEventsConnection.model_validate(data)

    def get_federation_summary(
        self,
        time_range: TimeRangeInput,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetFederationSummary:
        """Aggregated federation summary: event counts by type, latency, failure rate."""
        query = gql("""
            query GetFederationSummary($timeRange: TimeRangeInput!, $noCache: Boolean = false) {
              analytics {
                infra {
                  federationSummary(timeRange: $timeRange, noCache: $noCache) {
                    ...FederationSummaryDefaultFields
                  }
                }
              }
            }

            fragment FederationSummaryDefaultFields on FederationSummary {
              eventCounts {
                eventType
                count
                failureCount
                avgLatencyMs
              }
              totalEvents
              overallAvgLatencyMs
              overallFailureRate
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range, "noCache": no_cache}
        response = self.execute(
            query=query,
            operation_name="GetFederationSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetFederationSummary.model_validate(data)

    def get_geographic_distribution(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        top_n: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetGeographicDistribution:
        query = gql("""
            query GetGeographicDistribution($streamId: ID, $timeRange: TimeRangeInput, $topN: Int = 10) {
              analytics {
                usage {
                  streaming {
                    geographicDistribution(streamId: $streamId, timeRange: $timeRange, topN: $topN) {
                      ...GeographicDistributionDefaultFields
                    }
                  }
                }
              }
            }

            fragment GeographicDistributionDefaultFields on GeographicDistribution {
              timeRange {
                start
                end
              }
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              topCountries {
                countryCode
                viewerCount
                percentage
              }
              topCities {
                city
                countryCode
                viewerCount
                percentage
                latitude
                longitude
              }
              uniqueCountries
              uniqueCities
              totalViewers
              viewersByCountry {
                timestamp
                countryCode
                viewerCount
              }
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "timeRange": time_range,
            "topN": top_n,
        }
        response = self.execute(
            query=query,
            operation_name="GetGeographicDistribution",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetGeographicDistribution.model_validate(data)

    def get_incident(self, id: str, **kwargs: Any) -> GetIncident:
        """One incident with its alerts and timeline. Null when the incident does not
        exist or is not visible to the caller."""
        query = gql("""
            query GetIncident($id: ID!) {
              incident(id: $id) {
                ...IncidentDetailDefaultFields
              }
            }

            fragment IncidentDetailDefaultFields on IncidentDetail {
              incident {
                id
                scope
                tenantId
                clusterId
                region
                alertname
                severity
                status
                resolution
                title
                summary
                firingAlertCount
                startedAt
                lastAlertAt
                acknowledgedAt
                acknowledgedBy
                assignedTo
                resolvedAt
                resolvedBy
                createdAt
                updatedAt
              }
              alerts {
                fingerprint
                status
                labels
                annotations
                startsAt
                endsAt
                generatorUrl
              }
              timeline {
                id
                kind
                actorUserId
                createdAt
                note
                assignedTo
                reportId
                channel
                resolution
                alertFingerprint
                alertname
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetIncident", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetIncident.model_validate(data)

    def get_incidents_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        filter_: Union[Optional[IncidentFilterInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetIncidentsConnection:
        """Incidents raised by platform alerting on clusters the current tenant owns,
        newest first."""
        query = gql("""
            query GetIncidentsConnection($page: ConnectionInput, $filter: IncidentFilterInput) {
              incidentsConnection(page: $page, filter: $filter) {
                edges {
                  cursor
                  node {
                    ...IncidentDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment IncidentDefaultFields on Incident {
              id
              scope
              tenantId
              clusterId
              region
              alertname
              severity
              status
              resolution
              title
              summary
              firingAlertCount
              startedAt
              lastAlertAt
              acknowledgedAt
              acknowledgedBy
              assignedTo
              resolvedAt
              resolvedBy
              createdAt
              updatedAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page, "filter": filter_}
        response = self.execute(
            query=query,
            operation_name="GetIncidentsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetIncidentsConnection.model_validate(data)

    def get_infrastructure_node_metrics_1_h_connection(
        self,
        id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetInfrastructureNodeMetrics1hConnection:
        """Hourly aggregated metrics for this node."""
        query = gql("""
            query GetInfrastructureNodeMetrics1hConnection($id: ID!, $page: ConnectionInput, $timeRange: TimeRangeInput) {
              node(id: $id) {
                __typename
                ... on InfrastructureNode {
                  metrics1hConnection(page: $page, timeRange: $timeRange) {
                    edges {
                      cursor
                      node {
                        ...NodeMetricHourlyDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment NodeMetricHourlyDefaultFields on NodeMetricHourly {
              id
              timestamp
              nodeId
              clusterId
              avgCpu
              peakCpu
              avgMemory
              peakMemory
              avgDisk
              peakDisk
              avgShm
              peakShm
              totalBandwidthIn
              totalBandwidthOut
              wasHealthy
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"id": id, "page": page, "timeRange": time_range}
        response = self.execute(
            query=query,
            operation_name="GetInfrastructureNodeMetrics1hConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetInfrastructureNodeMetrics1hConnection.model_validate(data)

    def get_infrastructure_node_metrics_connection(
        self,
        id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetInfrastructureNodeMetricsConnection:
        """Paginated time-series metrics for this node."""
        query = gql("""
            query GetInfrastructureNodeMetricsConnection($id: ID!, $page: ConnectionInput, $timeRange: TimeRangeInput) {
              node(id: $id) {
                __typename
                ... on InfrastructureNode {
                  metricsConnection(page: $page, timeRange: $timeRange) {
                    edges {
                      cursor
                      node {
                        ...NodeMetricDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment NodeMetricDefaultFields on NodeMetric {
              id
              timestamp
              nodeId
              clusterId
              cpuUsage
              memoryTotal
              memoryUsed
              diskTotal
              diskUsed
              shmTotal
              shmUsed
              networkRx
              networkTx
              upSpeed
              downSpeed
              connectionsCurrent
              streamCount
              status
              isHealthy
              latitude
              longitude
              metadata
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"id": id, "page": page, "timeRange": time_range}
        response = self.execute(
            query=query,
            operation_name="GetInfrastructureNodeMetricsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetInfrastructureNodeMetricsConnection.model_validate(data)

    def get_invoice(self, id: str, **kwargs: Any) -> GetInvoice:
        """Fetch a single invoice by ID."""
        query = gql("""
            query GetInvoice($id: ID!) {
              invoice(id: $id) {
                ...InvoiceDefaultFields
              }
            }

            fragment InvoiceDefaultFields on Invoice {
              id
              amount
              baseAmount
              meteredAmount
              grossMeteredAmount
              prepaidCreditApplied
              currency
              presentmentAmountCents
              presentmentCurrency
              presentmentUnitsPerEur
              presentmentReferenceDate
              finalizedAt
              status
              dueDate
              paidAt
              createdAt
              updatedAt
              periodStart
              periodEnd
              usageDetails
              lineItems {
                lineKey
                meter
                description
                quantity
                includedQuantity
                billableQuantity
                unitPrice
                total
                currency
                clusterId
                clusterName
                clusterKind
                pricingSource
                pricingLabel
                unit
                dimensions
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetInvoice", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetInvoice.model_validate(data)

    def get_invoices_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetInvoicesConnection:
        """List invoices for the current tenant."""
        query = gql("""
            query GetInvoicesConnection($page: ConnectionInput) {
              invoicesConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...InvoiceDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment InvoiceDefaultFields on Invoice {
              id
              amount
              baseAmount
              meteredAmount
              grossMeteredAmount
              prepaidCreditApplied
              currency
              presentmentAmountCents
              presentmentCurrency
              presentmentUnitsPerEur
              presentmentReferenceDate
              finalizedAt
              status
              dueDate
              paidAt
              createdAt
              updatedAt
              periodStart
              periodEnd
              usageDetails
              lineItems {
                lineKey
                meter
                description
                quantity
                includedQuantity
                billableQuantity
                unitPrice
                total
                currency
                clusterId
                clusterName
                clusterKind
                pricingSource
                pricingLabel
                unit
                dimensions
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetInvoicesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetInvoicesConnection.model_validate(data)

    def get_marketplace_cluster(
        self, cluster_id: str, **kwargs: Any
    ) -> GetMarketplaceCluster:
        """Get details for a specific marketplace cluster."""
        query = gql("""
            query GetMarketplaceCluster($clusterId: String!) {
              marketplaceCluster(clusterId: $clusterId) {
                ...MarketplaceClusterDefaultFields
              }
            }

            fragment MarketplaceClusterDefaultFields on MarketplaceCluster {
              clusterId
              clusterName
              shortDescription
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              ownerName
              maxConcurrentStreams
              maxConcurrentViewers
              currentUtilization
              isSubscribed
              subscriptionStatus
              isEligible
              denialReason
            }
            """)
        variables: dict[str, object] = {"clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="GetMarketplaceCluster",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMarketplaceCluster.model_validate(data)

    def get_marketplace_clusters_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetMarketplaceClustersConnection:
        """List clusters available in the marketplace (paginated)."""
        query = gql("""
            query GetMarketplaceClustersConnection($page: ConnectionInput) {
              marketplaceClustersConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...MarketplaceClusterDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment MarketplaceClusterDefaultFields on MarketplaceCluster {
              clusterId
              clusterName
              shortDescription
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              ownerName
              maxConcurrentStreams
              maxConcurrentViewers
              currentUtilization
              isSubscribed
              subscriptionStatus
              isEligible
              denialReason
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetMarketplaceClustersConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMarketplaceClustersConnection.model_validate(data)

    def get_media_placement_change(
        self, scope: MediaPlacementScopeInput, idempotency_key: str, **kwargs: Any
    ) -> GetMediaPlacementChange:
        query = gql("""
            query GetMediaPlacementChange($scope: MediaPlacementScopeInput!, $idempotencyKey: String!) {
              mediaPlacementChange(scope: $scope, idempotencyKey: $idempotencyKey) {
                __typename
                ...MediaPlacementChangeDefaultFields
                ...MediaPlacementErrorInMediaPlacementChangeResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementChangeDefaultFields on MediaPlacementChange {
              scope {
                kind
                streamId
              }
              idempotencyKey
              revision
              parentRevision
              digest
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
              createdAt
            }

            fragment MediaPlacementErrorInMediaPlacementChangeResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              mediaPlacementErrorParentRevision: parentRevision
              retryAfterSeconds
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {
            "scope": scope,
            "idempotencyKey": idempotency_key,
        }
        response = self.execute(
            query=query,
            operation_name="GetMediaPlacementChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMediaPlacementChange.model_validate(data)

    def get_media_placement_options(
        self,
        scope: MediaPlacementScopeInput,
        filter_: Union[Optional[MediaPlacementOptionsFilter], UnsetType] = UNSET,
        after: Union[Optional[str], UnsetType] = UNSET,
        first: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetMediaPlacementOptions:
        query = gql("""
            query GetMediaPlacementOptions($scope: MediaPlacementScopeInput!, $filter: MediaPlacementOptionsFilter, $after: String, $first: Int = 50) {
              mediaPlacementOptions(
                scope: $scope
                filter: $filter
                after: $after
                first: $first
              ) {
                __typename
                ...MediaPlacementOptionsConnectionDefaultFields
                ...MediaPlacementErrorInMediaPlacementOptionsResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementErrorInMediaPlacementOptionsResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment MediaPlacementOptionsConnectionDefaultFields on MediaPlacementOptionsConnection {
              nodes {
                id
                name
                kind
                clusterClass
                region
                ownerId
                clusterId
                eligible
                reason
              }
              pageInfo {
                startCursor
                endCursor
                hasNextPage
                hasPreviousPage
              }
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {
            "scope": scope,
            "filter": filter_,
            "after": after,
            "first": first,
        }
        response = self.execute(
            query=query,
            operation_name="GetMediaPlacementOptions",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMediaPlacementOptions.model_validate(data)

    def get_media_placement_policy(
        self, scope: MediaPlacementScopeInput, **kwargs: Any
    ) -> GetMediaPlacementPolicy:
        """Tenant-bound placement intent and actual enforcement progress."""
        query = gql("""
            query GetMediaPlacementPolicy($scope: MediaPlacementScopeInput!) {
              mediaPlacementPolicy(scope: $scope) {
                __typename
                ...MediaPlacementPolicyStateDefaultFields
                ...MediaPlacementErrorInMediaPlacementPolicyResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementErrorInMediaPlacementPolicyResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              mediaPlacementErrorParentRevision: parentRevision
              retryAfterSeconds
            }

            fragment MediaPlacementPolicyStateDefaultFields on MediaPlacementPolicyState {
              scope {
                kind
                streamId
              }
              revision
              parentRevision
              activeRevision
              activeParentRevision
              verbs {
                verb
                ownRules {
                  schemaVersion
                }
                inheritedRules {
                  schemaVersion
                }
                requestedEffective {
                  schemaVersion
                  digest
                }
              }
              rollout {
                status
                requiredRecipients
                appliedRecipients
                pendingRecipients {
                  id
                  name
                  status
                  reason
                  authorityExpiresAt
                }
                existingSessionsRetained
                updatedAt
              }
              actions {
                canRead
                canPreview
                canManage
                canInspectPrivateCandidates
              }
              features {
                schemaVersion
                geographicSpillover
                priceOrdering
                supportedPresets
              }
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"scope": scope}
        response = self.execute(
            query=query,
            operation_name="GetMediaPlacementPolicy",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMediaPlacementPolicy.model_validate(data)

    def get_media_retention_policy(self, **kwargs: Any) -> GetMediaRetentionPolicy:
        """Tenant-default retention policy + entitlement bounds + the value the
        cascade (per-asset override → tenant default → tier entitlement) would
        resolve to today. Used by the webapp's "Storage retention defaults"
        panel to size sliders and explain the allowed range."""
        query = gql("""
            query GetMediaRetentionPolicy {
              mediaRetentionPolicy {
                ...MediaRetentionPolicyDefaultFields
              }
            }

            fragment MediaRetentionPolicyDefaultFields on MediaRetentionPolicy {
              bounds {
                maxRecordingRetentionDays
              }
              updatedBy
              updatedAt
              defaultVodRetentionDays
              defaultDvrRetentionDays
              defaultClipRetentionDays
              effectiveVodRetentionDays
              effectiveDvrRetentionDays
              effectiveClipRetentionDays
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetMediaRetentionPolicy",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMediaRetentionPolicy.model_validate(data)

    def get_messages_connection(
        self,
        conversation_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetMessagesConnection:
        """List messages in a conversation.
        Messages are ordered chronologically, oldest first."""
        query = gql("""
            query GetMessagesConnection($conversationId: ID!, $page: ConnectionInput) {
              messagesConnection(conversationId: $conversationId, page: $page) {
                edges {
                  cursor
                  node {
                    ...MessageDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment MessageDefaultFields on Message {
              id
              conversationId
              content
              sender
              createdAt
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"conversationId": conversation_id, "page": page}
        response = self.execute(
            query=query,
            operation_name="GetMessagesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMessagesConnection.model_validate(data)

    def get_mollie_mandates(self, **kwargs: Any) -> GetMollieMandates:
        """List Mollie mandates for the current tenant."""
        query = gql("""
            query GetMollieMandates {
              mollieMandates {
                ...MollieMandateDefaultFields
              }
            }

            fragment MollieMandateDefaultFields on MollieMandate {
              mandateId
              customerId
              status
              method
              details
              createdAt
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetMollieMandates",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMollieMandates.model_validate(data)

    def get_my_cluster_invites_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetMyClusterInvitesConnection:
        """List cluster invites sent to the current tenant (paginated)."""
        query = gql("""
            query GetMyClusterInvitesConnection($page: ConnectionInput) {
              myClusterInvitesConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...ClusterInviteDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterInviteDefaultFields on ClusterInvite {
              id
              clusterId
              invitedTenantId
              inviteToken
              accessLevel
              resourceLimits
              status
              createdBy
              createdAt
              expiresAt
              acceptedAt
              invitedTenantName
              clusterName
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetMyClusterInvitesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMyClusterInvitesConnection.model_validate(data)

    def get_my_subscriptions_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetMySubscriptionsConnection:
        """List clusters the tenant is subscribed to (paginated)."""
        query = gql("""
            query GetMySubscriptionsConnection($page: ConnectionInput) {
              mySubscriptionsConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...ClusterDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterDefaultFields on Cluster {
              id
              clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetMySubscriptionsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetMySubscriptionsConnection.model_validate(data)

    def get_network_status(self, **kwargs: Any) -> GetNetworkStatus:
        """Public network status: cluster topology, node counts, peer connections.
        No tenant data exposed. Part of public allowlist."""
        query = gql("""
            query GetNetworkStatus {
              networkStatus {
                ...NetworkStatusDefaultFields
              }
            }

            fragment NetworkStatusDefaultFields on NetworkStatus {
              clusters {
                clusterId
                name
                region
                latitude
                longitude
                nodeCount
                healthyNodeCount
                peerCount
                status
                clusterType
                shortDescription
                currentStreams
                currentViewers
                egressMbps
                egressCapacityMbps
                ingressMbps
                services
              }
              peerConnections {
                sourceCluster
                targetCluster
                connected
                connectionType
              }
              nodes {
                nodeId
                name
                nodeType
                latitude
                longitude
                status
                clusterId
              }
              serviceInstances {
                instanceId
                serviceId
                clusterId
                nodeId
                status
                healthStatus
              }
              totalNodes
              healthyNodes
              updatedAt
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetNetworkStatus",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNetworkStatus.model_validate(data)

    def get_node(self, id: str, **kwargs: Any) -> GetNode:
        """Fetch a single node by its global ID."""
        query = gql("""
            query GetNode($id: ID!) {
              node(id: $id) {
                __typename
                ...APIUsageRecordDefaultFields
                ...ArtifactEventInNodeDefaultFields
                ...BufferEventInNodeDefaultFields
                ...ClientMetrics5mDefaultFields
                ...ClipInNodeDefaultFields
                ...ClusterInNodeDefaultFields
                ...ConnectionEventInNodeDefaultFields
                ...ConversationInNodeDefaultFields
                ...InfrastructureNodeInNodeDefaultFields
                ...MessageDefaultFields
                ...NodeMetricDefaultFields
                ...NodeMetricHourlyDefaultFields
                ...NodePerformance5mDefaultFields
                ...ProcessingUsageRecordInNodeDefaultFields
                ...QualityTierDailyDefaultFields
                ...SigningKeyInNodeDefaultFields
                ...StorageEventInNodeDefaultFields
                ...StorageUsageRecordDefaultFields
                ...StreamInNodeDefaultFields
                ...StreamAnalyticsDailyDefaultFields
                ...StreamConnectionHourlyDefaultFields
                ...StreamEventInNodeDefaultFields
                ...StreamHealth5mInNodeDefaultFields
                ...StreamHealthMetricInNodeDefaultFields
                ...TenantDailyStatDefaultFields
                ...TrackListEventInNodeDefaultFields
                ...ViewerGeoHourlyInNodeDefaultFields
                ...ViewerHoursHourlyInNodeDefaultFields
                ...ViewerSessionInNodeDefaultFields
                ...VodAssetInNodeDefaultFields
              }
            }

            fragment APIUsageRecordDefaultFields on APIUsageRecord {
              id
              timestamp
              authType
              operationType
              operationName
              requestCount
              errorCount
              totalDurationMs
              totalComplexity
              uniqueUsers
              uniqueTokens
            }

            fragment ArtifactEventInNodeDefaultFields on ArtifactEvent {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              artifactEventPlaybackId: playbackId
              stage
              contentType
              startUnix
              stopUnix
              ingestNodeId
              percent
              message
              filePath
              s3Url
              sizeBytes
              artifactEventExpiresAt: expiresAt
            }

            fragment BufferEventInNodeDefaultFields on BufferEvent {
              id
              eventId
              timestamp
              bufferEventNodeId: nodeId
              bufferState
              eventData
              payload
            }

            fragment ClientMetrics5mDefaultFields on ClientMetrics5m {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              activeSessions
              avgBandwidthIn
              avgBandwidthOut
              avgConnectionTime
              packetLossRate
            }

            fragment ClipInNodeDefaultFields on Clip {
              id
              clipHash
              playbackId
              streamId
              sourceStreamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              title
              description
              startTime
              duration
              clipNodeId: nodeId
              storagePath
              sizeBytes
              status
              clipCreatedAt: createdAt
              updatedAt
              clipMode
              requestedParams
              storageLocation
              syncStatus
              hasLocalCopy
              isSynced
              isFinalized
              expiresAt
              isExpired
              playbackPolicy {
                type
                jwt {
                  allowedKids
                  requiredAudience
                }
                webhook {
                  url
                  timeoutMs
                  secretMasked
                }
              }
              thumbnailAssets {
                posterUrl
                spriteVttUrl
                spriteJpgUrl
                assetKey
              }
              effectiveRetention {
                retentionDays
                retentionUntil
                source
              }
              storageCost {
                perDay
                perMonth
                currency
              }
            }

            fragment ClusterInNodeDefaultFields on Cluster {
              id
              clusterClusterId: clusterId
              clusterName
              clusterType
              deploymentModel
              baseUrl
              databaseUrl
              periscopeUrl
              kafkaBrokers
              maxConcurrentStreams
              maxConcurrentViewers
              maxBandwidthMbps
              healthStatus
              isActive
              isDefaultCluster
              isPlatformOfficial
              regionId
              isSubscribed
              clusterCreatedAt: createdAt
              updatedAt
              ownerTenantId
              visibility
              pricingModel
              monthlyPriceCents
              requiresApproval
              shortDescription
            }

            fragment ConnectionEventInNodeDefaultFields on ConnectionEvent {
              id
              connectionEventEventId: eventId
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              sessionId
              connectionAddr
              connector
              nodeId
              countryCode
              city
              latitude
              longitude
              clientBucket {
                h3Index
                resolution
              }
              nodeBucket {
                h3Index
                resolution
              }
              eventType
              requestUrl
              connectionEventClusterId: clusterId
              originClusterId
              controlCellId
              sessionDurationSeconds
              bytesTransferred
            }

            fragment ConversationInNodeDefaultFields on Conversation {
              id
              subject
              conversationStatus: status
              lastMessage {
                id
                conversationId
                content
                sender
                createdAt
              }
              unreadCount
              createdAt
              conversationUpdatedAt: updatedAt
            }

            fragment InfrastructureNodeInNodeDefaultFields on InfrastructureNode {
              id
              nodeId
              infrastructureNodeClusterId: clusterId
              nodeName
              nodeType
              internalIp
              externalIp
              wireguardIp
              wireguardPublicKey
              region
              latitude
              longitude
              availabilityZone
              cpuCores
              memoryGb
              diskGb
              lastHeartbeat
              tags
              metadata
              infrastructureNodeCreatedAt: createdAt
              updatedAt
              liveState {
                nodeId
                tenantId
                cpuPercent
                ramUsedBytes
                ramTotalBytes
                diskUsedBytes
                diskTotalBytes
                upSpeed
                downSpeed
                activeStreams
                isHealthy
                latitude
                longitude
                location
                metadata
                updatedAt
              }
              effectiveMode
              routingImpactPreview {
                activeStreams
                activeViewers
              }
            }

            fragment MessageDefaultFields on Message {
              id
              conversationId
              content
              sender
              createdAt
            }

            fragment NodeMetricDefaultFields on NodeMetric {
              id
              timestamp
              nodeId
              clusterId
              cpuUsage
              memoryTotal
              memoryUsed
              diskTotal
              diskUsed
              shmTotal
              shmUsed
              networkRx
              networkTx
              upSpeed
              downSpeed
              connectionsCurrent
              streamCount
              status
              isHealthy
              latitude
              longitude
              metadata
            }

            fragment NodeMetricHourlyDefaultFields on NodeMetricHourly {
              id
              timestamp
              nodeId
              clusterId
              avgCpu
              peakCpu
              avgMemory
              peakMemory
              avgDisk
              peakDisk
              avgShm
              peakShm
              totalBandwidthIn
              totalBandwidthOut
              wasHealthy
            }

            fragment NodePerformance5mDefaultFields on NodePerformance5m {
              id
              timestamp
              nodeId
              avgCpu
              maxCpu
              avgMemory
              maxMemory
              totalBandwidth
              avgStreams
              maxStreams
            }

            fragment ProcessingUsageRecordInNodeDefaultFields on ProcessingUsageRecord {
              id
              timestamp
              nodeId
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              processType
              clusterId
              originClusterId
              controlCellId
              trackType
              processingUsageRecordDurationMs: durationMs
              inputCodec
              outputCodec
              segmentNumber
              width
              height
              renditionCount
              broadcasterUrl
              uploadTimeUs
              livepeerSessionId
              segmentStartMs
              inputBytes
              outputBytesTotal
              attemptCount
              turnaroundMs
              speedFactor
              renditionsJson
              inputFrames
              outputFrames
              decodeUsPerFrame
              transformUsPerFrame
              encodeUsPerFrame
              isFinal
              inputFramesDelta
              outputFramesDelta
              inputBytesDelta
              outputBytesDelta
              inputWidth
              inputHeight
              outputWidth
              outputHeight
              inputFpks
              outputFpsMeasured
              sampleRate
              channels
              sourceTimestampMs
              sinkTimestampMs
              sourceAdvancedMs
              sinkAdvancedMs
              rtfIn
              rtfOut
              pipelineLagMs
              outputBitrateBps
            }

            fragment QualityTierDailyDefaultFields on QualityTierDaily {
              id
              day
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              tier2160pMinutes
              tier1440pMinutes
              tier1080pMinutes
              tier720pMinutes
              tier480pMinutes
              tierSdMinutes
              primaryTier
              codecH264Minutes
              codecH265Minutes
              codecVp9Minutes
              codecAv1Minutes
              avgBitrate
              avgFps
            }

            fragment SigningKeyInNodeDefaultFields on SigningKey {
              id
              kid
              name
              algorithm
              publicKeyPem
              signingKeyStatus: status
              createdAt
              lastUsedAt
              revokedAt
            }

            fragment StorageEventInNodeDefaultFields on StorageEvent {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              assetHash
              action
              assetType
              storageEventSizeBytes: sizeBytes
              s3Url
              localPath
              nodeId
              clusterId
              originClusterId
              controlCellId
              durationMs
              warmDurationMs
              error
            }

            fragment StorageUsageRecordDefaultFields on StorageUsageRecord {
              id
              timestamp
              nodeId
              storageScope
              totalBytes
              fileCount
              dvrBytes
              clipBytes
              vodBytes
              frozenDvrBytes
              frozenClipBytes
              frozenVodBytes
            }

            fragment StreamAnalyticsDailyDefaultFields on StreamAnalyticsDaily {
              id
              day
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              totalViews
              uniqueViewers
              uniqueCountries
              uniqueCities
              egressBytes
            }

            fragment StreamConnectionHourlyDefaultFields on StreamConnectionHourly {
              id
              hour
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              totalBytes
              uniqueViewers
              totalSessions
            }

            fragment StreamEventInNodeDefaultFields on StreamEvent {
              id
              eventId
              streamEventStreamId: streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              streamEventNodeId: nodeId
              type
              streamEventStatus: status
              timestamp
              details
              payload
              source
              streamEventBufferState: bufferState
              hasIssues
              trackCount
              qualityTier
              primaryWidth
              primaryHeight
              primaryFps
              primaryCodec
              primaryBitrate
              downloadedBytes
              uploadedBytes
              totalViewers
              totalInputs
              totalOutputs
              viewerSeconds
              requestUrl
              protocol
              latitude
              longitude
              location
              countryCode
              city
              sourceRegion
              sourceClusterId
              streamOriginRegion
              streamOriginClusterId
              schemaVersion
            }

            fragment StreamHealth5mInNodeDefaultFields on StreamHealth5m {
              id
              timestamp
              streamHealth5mNodeId: nodeId
              rebufferCount
              issueCount
              sampleIssues
              avgBitrate
              avgFps
              avgBufferHealth
              avgFrameJitterMs
              maxFrameJitterMs
              bufferDryCount
              qualityTier
            }

            fragment StreamHealthMetricInNodeDefaultFields on StreamHealthMetric {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              issuesDescription
              streamHealthMetricHasIssues: hasIssues
              bitrate
              fps
              width
              height
              codec
              qualityTier
              gopSize
              frameMsMax
              frameMsMin
              framesMax
              framesMin
              keyframeMsMax
              keyframeMsMin
              frameJitterMs
              trackCount
              bufferState
              bufferHealth
              bufferSize
              audioChannels
              audioSampleRate
              audioCodec
              audioBitrate
              trackMetadata
            }

            fragment StreamInNodeDefaultFields on Stream {
              id
              streamStreamId: streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              managedSource {
                sourceKind
                alwaysOn
                placementCount
              }
              sourceLocation {
                mode
                clusters {
                  clusterId
                  nodeIds
                }
                avoidNodeIds
              }
              createdAt
              streamUpdatedAt: updatedAt
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
                nodeId
                trackCount
                totalInputs
                uploadedBytes
                downloadedBytes
                viewerSeconds
                packetsSent
                packetsLost
                packetsRetransmitted
                bufferState
                qualityTier
                primaryWidth
                primaryHeight
                primaryFps
                primaryCodec
                primaryBitrate
                hasIssues
                issuesDescription
              }
              pushTargets {
                id
                streamId
                platform
                name
                targetUri
                isEnabled
                status
                lastError
                reasonCode
                lastPushedAt
                createdAt
              }
              playbackPolicy {
                type
                jwt {
                  allowedKids
                  requiredAudience
                }
                webhook {
                  url
                  timeoutMs
                  secretMasked
                }
              }
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              thumbnailAssets {
                posterUrl
                spriteVttUrl
                spriteJpgUrl
                assetKey
              }
              retentionOverrides {
                streamId
                dvrRetentionDaysOverride
                clipRetentionDaysOverride
              }
            }

            fragment TenantDailyStatDefaultFields on TenantDailyStat {
              id
              date
              egressGb
              viewerHours
              uniqueViewers
              totalSessions
              totalViews
            }

            fragment TrackListEventInNodeDefaultFields on TrackListEvent {
              id
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              trackListEventNodeId: nodeId
              trackList
              trackListEventTrackCount: trackCount
              timestamp
              tracks {
                trackName
                trackType
                codec
                bitrateKbps
                bitrateBps
                buffer
                jitter
                width
                height
                fps
                resolution
                hasBFrames
                channels
                sampleRate
              }
            }

            fragment ViewerGeoHourlyInNodeDefaultFields on ViewerGeoHourly {
              id
              hour
              viewerGeoHourlyCountryCode: countryCode
              viewerCount
              viewerHours
              egressGb
            }

            fragment ViewerHoursHourlyInNodeDefaultFields on ViewerHoursHourly {
              id
              hour
              viewerHoursHourlyStreamId: streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              countryCode
              uniqueViewers
              totalSessionSeconds
              totalBytes
              viewerHours
              egressGb
            }

            fragment ViewerSessionInNodeDefaultFields on ViewerSession {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              viewerSessionNodeId: nodeId
              sessionId
              connectedAt
              disconnectedAt
              viewerSessionConnector: connector
              countryCode
              city
              latitude
              longitude
              durationSeconds
              bytesUp
              bytesDown
              connectionQuality
              bufferHealth
              clientBucket {
                h3Index
                resolution
              }
            }

            fragment VodAssetInNodeDefaultFields on VodAsset {
              id
              artifactHash
              playbackId
              vodAssetStreamId: streamId
              originType
              originId
              vodAssetTitle: title
              description
              filename
              vodAssetStatus: status
              vodAssetStorageLocation: storageLocation
              syncStatus
              hasLocalCopy
              vodAssetIsSynced: isSynced
              vodAssetIsFinalized: isFinalized
              sizeBytes
              durationMs
              resolution
              videoCodec
              audioCodec
              bitrateKbps
              createdAt
              vodAssetUpdatedAt: updatedAt
              expiresAt
              errorMessage
              playbackPolicy {
                type
                jwt {
                  allowedKids
                  requiredAudience
                }
                webhook {
                  url
                  timeoutMs
                  secretMasked
                }
              }
              thumbnailAssets {
                posterUrl
                spriteVttUrl
                spriteJpgUrl
                assetKey
              }
              effectiveRetention {
                retentionDays
                retentionUntil
                source
              }
              storageCost {
                perDay
                perMonth
                currency
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetNode", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetNode.model_validate(data)

    def get_node_metrics_1_h_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetNodeMetrics1hConnection:
        query = gql("""
            query GetNodeMetrics1hConnection($page: ConnectionInput, $timeRange: TimeRangeInput, $nodeId: String, $noCache: Boolean = false) {
              analytics {
                infra {
                  nodeMetrics1hConnection(
                    page: $page
                    timeRange: $timeRange
                    nodeId: $nodeId
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...NodeMetricHourlyDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment NodeMetricHourlyDefaultFields on NodeMetricHourly {
              id
              timestamp
              nodeId
              clusterId
              avgCpu
              peakCpu
              avgMemory
              peakMemory
              avgDisk
              peakDisk
              avgShm
              peakShm
              totalBandwidthIn
              totalBandwidthOut
              wasHealthy
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "timeRange": time_range,
            "nodeId": node_id,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetNodeMetrics1hConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNodeMetrics1hConnection.model_validate(data)

    def get_node_metrics_aggregated(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetNodeMetricsAggregated:
        query = gql("""
            query GetNodeMetricsAggregated($timeRange: TimeRangeInput, $nodeId: String, $noCache: Boolean = false) {
              analytics {
                infra {
                  nodeMetricsAggregated(timeRange: $timeRange, nodeId: $nodeId, noCache: $noCache) {
                    ...NodeMetricsAggregatedDefaultFields
                  }
                }
              }
            }

            fragment NodeMetricsAggregatedDefaultFields on NodeMetricsAggregated {
              nodeId
              clusterId
              avgCpu
              avgMemory
              avgDisk
              avgShm
              totalBandwidthIn
              totalBandwidthOut
              sampleCount
            }
            """)
        variables: dict[str, object] = {
            "timeRange": time_range,
            "nodeId": node_id,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetNodeMetricsAggregated",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNodeMetricsAggregated.model_validate(data)

    def get_node_metrics_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetNodeMetricsConnection:
        query = gql("""
            query GetNodeMetricsConnection($page: ConnectionInput, $nodeId: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  nodeMetricsConnection(
                    page: $page
                    nodeId: $nodeId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...NodeMetricDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment NodeMetricDefaultFields on NodeMetric {
              id
              timestamp
              nodeId
              clusterId
              cpuUsage
              memoryTotal
              memoryUsed
              diskTotal
              diskUsed
              shmTotal
              shmUsed
              networkRx
              networkTx
              upSpeed
              downSpeed
              connectionsCurrent
              streamCount
              status
              isHealthy
              latitude
              longitude
              metadata
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "nodeId": node_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetNodeMetricsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNodeMetricsConnection.model_validate(data)

    def get_node_performance_5_m_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetNodePerformance5mConnection:
        query = gql("""
            query GetNodePerformance5mConnection($page: ConnectionInput, $nodeId: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  nodePerformance5mConnection(
                    page: $page
                    nodeId: $nodeId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...NodePerformance5mDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment NodePerformance5mDefaultFields on NodePerformance5m {
              id
              timestamp
              nodeId
              avgCpu
              maxCpu
              avgMemory
              maxMemory
              totalBandwidth
              avgStreams
              maxStreams
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "nodeId": node_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetNodePerformance5mConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNodePerformance5mConnection.model_validate(data)

    def get_nodes_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        status: Union[Optional[NodeStatus], UnsetType] = UNSET,
        nodes_connection_type: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetNodesConnection:
        """List nodes (edge servers) with optional filters."""
        query = gql("""
            query GetNodesConnection($page: ConnectionInput, $clusterId: String, $status: NodeStatus, $nodesConnectionType: String) {
              nodesConnection(
                page: $page
                clusterId: $clusterId
                status: $status
                type: $nodesConnectionType
              ) {
                edges {
                  cursor
                  node {
                    ...InfrastructureNodeDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment InfrastructureNodeDefaultFields on InfrastructureNode {
              id
              nodeId
              clusterId
              nodeName
              nodeType
              internalIp
              externalIp
              wireguardIp
              wireguardPublicKey
              region
              latitude
              longitude
              availabilityZone
              cpuCores
              memoryGb
              diskGb
              lastHeartbeat
              tags
              metadata
              createdAt
              updatedAt
              liveState {
                nodeId
                tenantId
                cpuPercent
                ramUsedBytes
                ramTotalBytes
                diskUsedBytes
                diskTotalBytes
                upSpeed
                downSpeed
                activeStreams
                isHealthy
                latitude
                longitude
                location
                metadata
                updatedAt
              }
              effectiveMode
              routingImpactPreview {
                activeStreams
                activeViewers
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "clusterId": cluster_id,
            "status": status,
            "nodesConnectionType": nodes_connection_type,
        }
        response = self.execute(
            query=query,
            operation_name="GetNodesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetNodesConnection.model_validate(data)

    def get_orchestrator(self, orch_addr: str, **kwargs: Any) -> GetOrchestrator:
        """Fetch a public orchestrator with every known instance and per-(gateway,
        instance) vantage. Side-panel data source for the federation map."""
        query = gql("""
            query GetOrchestrator($orchAddr: String!) {
              orchestrator(orchAddr: $orchAddr) {
                ...OrchestratorWithDetailsDefaultFields
              }
            }

            fragment OrchestratorWithDetailsDefaultFields on OrchestratorWithDetails {
              orchestrator {
                tenantId
                orchAddr
                lastSeen
                updatedAt
              }
              instances {
                tenantId
                orchAddr
                resolvedIp
                canonicalUrl
                advertisedNodeUrls
                capabilities
                pricePerUnitEth
                pixelsPerUnit
                capabilityPrices {
                  capability
                  pricePerUnitEth
                  pixelsPerUnit
                }
                hardware
                source
                lastSeen
                updatedAt
              }
              vantages {
                tenantId
                gatewayId
                gatewayRegion
                orchAddr
                resolvedIp
                latitude
                longitude
                city
                countryCode
                geoSource
                geoResolvedAt
                latestLatencyMs
                score
                dialedRecently
                lastSeen
              }
            }
            """)
        variables: dict[str, object] = {"orchAddr": orch_addr}
        response = self.execute(
            query=query, operation_name="GetOrchestrator", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetOrchestrator.model_validate(data)

    def get_orchestrator_instances(
        self, orch_addr: Union[Optional[str], UnsetType] = UNSET, **kwargs: Any
    ) -> GetOrchestratorInstances:
        """List public per-instance rows. Each carries its own
        price/capabilities/hardware — usually consistent across an orch's pool but
        not guaranteed. Use the optional `orchAddr` filter to scope to one orch."""
        query = gql("""
            query GetOrchestratorInstances($orchAddr: String) {
              orchestratorInstances(orchAddr: $orchAddr) {
                ...OrchestratorInstanceDefaultFields
              }
            }

            fragment OrchestratorInstanceDefaultFields on OrchestratorInstance {
              tenantId
              orchAddr
              resolvedIp
              canonicalUrl
              advertisedNodeUrls
              capabilities
              pricePerUnitEth
              pixelsPerUnit
              capabilityPrices {
                capability
                pricePerUnitEth
                pixelsPerUnit
              }
              hardware
              source
              lastSeen
              updatedAt
            }
            """)
        variables: dict[str, object] = {"orchAddr": orch_addr}
        response = self.execute(
            query=query,
            operation_name="GetOrchestratorInstances",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetOrchestratorInstances.model_validate(data)

    def get_orchestrator_performance_series(
        self,
        orch_addr: str,
        time_range: TimeRangeInput,
        interval: Union[Optional[str], UnsetType] = UNSET,
        gateway_id: Union[Optional[str], UnsetType] = UNSET,
        resolved_ip: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetOrchestratorPerformanceSeries:
        """Time-series performance points for an orchestrator from the discovery
        rollups (5m or 1h). `meanLatencyMs` is server-pre-computed; callers don't
        divide latency_sum/latency_count themselves."""
        query = gql("""
            query GetOrchestratorPerformanceSeries($orchAddr: String!, $timeRange: TimeRangeInput!, $interval: String, $gatewayId: String, $resolvedIp: String) {
              orchestratorPerformanceSeries(
                orchAddr: $orchAddr
                timeRange: $timeRange
                interval: $interval
                gatewayId: $gatewayId
                resolvedIp: $resolvedIp
              ) {
                ...OrchestratorPerformancePointDefaultFields
              }
            }

            fragment OrchestratorPerformancePointDefaultFields on OrchestratorPerformancePoint {
              timestamp
              gatewayId
              gatewayRegion
              resolvedIp
              attempts
              successes
              failures
              meanLatencyMs
              maxLatencyMs
              transcodeAttempts
              transcodeSuccesses
              transcodeFailures
              transcodeMeanOverallMs
              transcodeMaxOverallMs
              transcodePixels
              aiAttempts
              aiSuccesses
              aiFailures
              aiMeanLatencyMs
              aiMaxLatencyMs
            }
            """)
        variables: dict[str, object] = {
            "orchAddr": orch_addr,
            "timeRange": time_range,
            "interval": interval,
            "gatewayId": gateway_id,
            "resolvedIp": resolved_ip,
        }
        response = self.execute(
            query=query,
            operation_name="GetOrchestratorPerformanceSeries",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetOrchestratorPerformanceSeries.model_validate(data)

    def get_orchestrator_vantages(
        self, orch_addr: Union[Optional[str], UnsetType] = UNSET, **kwargs: Any
    ) -> GetOrchestratorVantages:
        """List per-(gateway, instance) vantage observations. Multi-region observation
        surfaces here as multiple rows. Use the optional `orchAddr` filter to
        scope to one orchestrator."""
        query = gql("""
            query GetOrchestratorVantages($orchAddr: String) {
              orchestratorVantages(orchAddr: $orchAddr) {
                ...OrchestratorVantageDefaultFields
              }
            }

            fragment OrchestratorVantageDefaultFields on OrchestratorVantage {
              tenantId
              gatewayId
              gatewayRegion
              orchAddr
              resolvedIp
              latitude
              longitude
              city
              countryCode
              geoSource
              geoResolvedAt
              latestLatencyMs
              score
              dialedRecently
              lastSeen
            }
            """)
        variables: dict[str, object] = {"orchAddr": orch_addr}
        response = self.execute(
            query=query,
            operation_name="GetOrchestratorVantages",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetOrchestratorVantages.model_validate(data)

    def get_orchestrators_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        orch_addr: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetOrchestratorsConnection:
        """List orchestrators discovered by Livepeer gateways under this cluster owner
        tenant. Federation-map data source. Vantage-independent; the per-region
        table comes from `orchestratorVantages`."""
        query = gql("""
            query GetOrchestratorsConnection($page: ConnectionInput, $orchAddr: String) {
              orchestratorsConnection(page: $page, orchAddr: $orchAddr) {
                ...OrchestratorsConnectionDefaultFields
              }
            }

            fragment OrchestratorsConnectionDefaultFields on OrchestratorsConnection {
              nodes {
                tenantId
                orchAddr
                lastSeen
                updatedAt
              }
              totalCount
            }
            """)
        variables: dict[str, object] = {"page": page, "orchAddr": orch_addr}
        response = self.execute(
            query=query,
            operation_name="GetOrchestratorsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetOrchestratorsConnection.model_validate(data)

    def get_overview(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetOverview:
        """Platform-wide overview metrics for the given time range."""
        query = gql("""
            query GetOverview($timeRange: TimeRangeInput) {
              analytics {
                overview(timeRange: $timeRange) {
                  ...PlatformOverviewDefaultFields
                }
              }
            }

            fragment PlatformOverviewDefaultFields on PlatformOverview {
              totalStreams
              activeStreams
              totalViewers
              averageViewers
              totalBandwidth
              peakBandwidth
              streamHours
              egressGb
              peakViewers
              timeRange {
                start
                end
              }
              totalUploadBytes
              totalDownloadBytes
              viewerHours
              deliveredMinutes
              uniqueViewers
              ingestHours
              peakConcurrentViewers
              totalViews
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range}
        response = self.execute(
            query=query, operation_name="GetOverview", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetOverview.model_validate(data)

    def get_payment(self, id: str, **kwargs: Any) -> GetPayment:
        """Fetch one invoice payment owned by the current tenant."""
        query = gql("""
            query GetPayment($id: ID!) {
              payment(id: $id) {
                ...InvoicePaymentDefaultFields
              }
            }

            fragment InvoicePaymentDefaultFields on InvoicePayment {
              id
              invoiceId
              method
              amount
              currency
              status
              confirmedAt
              createdAt
              updatedAt
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetPayment", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetPayment.model_validate(data)

    def get_payments_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        invoice_id: Union[Optional[str], UnsetType] = UNSET,
        status: Union[Optional[str], UnsetType] = UNSET,
        method: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetPaymentsConnection:
        """List invoice payments owned by the current tenant."""
        query = gql("""
            query GetPaymentsConnection($page: ConnectionInput, $invoiceId: ID, $status: String, $method: String) {
              paymentsConnection(
                page: $page
                invoiceId: $invoiceId
                status: $status
                method: $method
              ) {
                edges {
                  cursor
                  node {
                    ...InvoicePaymentDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment InvoicePaymentDefaultFields on InvoicePayment {
              id
              invoiceId
              method
              amount
              currency
              status
              confirmedAt
              createdAt
              updatedAt
              conversion {
                originalAmountCents
                originalCurrency
                eurAmountCents
                unitsPerEur
                source
                referenceDate
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "invoiceId": invoice_id,
            "status": status,
            "method": method,
        }
        response = self.execute(
            query=query,
            operation_name="GetPaymentsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPaymentsConnection.model_validate(data)

    def get_pending_subscriptions_connection(
        self,
        cluster_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetPendingSubscriptionsConnection:
        """List pending subscription requests (paginated)."""
        query = gql("""
            query GetPendingSubscriptionsConnection($page: ConnectionInput, $clusterId: ID!) {
              pendingSubscriptionsConnection(page: $page, clusterId: $clusterId) {
                edges {
                  cursor
                  node {
                    ...ClusterSubscriptionDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment ClusterSubscriptionDefaultFields on ClusterSubscription {
              id
              tenantId
              clusterId
              accessLevel
              subscriptionStatus
              resourceLimits
              requestedAt
              approvedAt
              approvedBy
              rejectionReason
              expiresAt
              createdAt
              updatedAt
              clusterName
              tenantName
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page, "clusterId": cluster_id}
        response = self.execute(
            query=query,
            operation_name="GetPendingSubscriptionsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPendingSubscriptionsConnection.model_validate(data)

    def get_player_boot_summary(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        artifact_hash: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetPlayerBootSummary:
        """Player startup (boot) summary for the tenant that owns the content. Diagnostic."""
        query = gql("""
            query GetPlayerBootSummary($streamId: ID, $artifactHash: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  playerBootSummary(
                    streamId: $streamId
                    artifactHash: $artifactHash
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    ...PlayerBootSummaryDefaultFields
                  }
                }
              }
            }

            fragment PlayerBootSummaryDefaultFields on PlayerBootSummary {
              bootCount
              errorCount
              p50TtfMs
              p95TtfMs
              p99TtfMs
              avgGatewayResolveMs
              avgMistHydrateMs
              avgPlayerSelectMs
              avgConnectMs
              avgPrebufferMs
              cacheHitRatio
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "artifactHash": artifact_hash,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetPlayerBootSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPlayerBootSummary.model_validate(data)

    def get_player_boot_time_series(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        artifact_hash: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        interval: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetPlayerBootTimeSeries:
        """Player startup summary bucketed over time (read-time TTF percentiles per
        `interval` window: "5m"/"15m"/"1h"/"1d"). Companion to playerBootSummary for
        trend charts. `bootCount` per bucket is the denominator for low-sample windows."""
        query = gql("""
            query GetPlayerBootTimeSeries($streamId: ID, $artifactHash: String, $timeRange: TimeRangeInput, $interval: String = "1h", $noCache: Boolean = false) {
              analytics {
                health {
                  playerBootTimeSeries(
                    streamId: $streamId
                    artifactHash: $artifactHash
                    timeRange: $timeRange
                    interval: $interval
                    noCache: $noCache
                  ) {
                    ...PlayerBootTimeSeriesBucketDefaultFields
                  }
                }
              }
            }

            fragment PlayerBootTimeSeriesBucketDefaultFields on PlayerBootTimeSeriesBucket {
              timestamp
              bootCount
              p50TtfMs
              p95TtfMs
              p99TtfMs
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "artifactHash": artifact_hash,
            "timeRange": time_range,
            "interval": interval,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetPlayerBootTimeSeries",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPlayerBootTimeSeries.model_validate(data)

    def get_prepaid_balance(self, **kwargs: Any) -> GetPrepaidBalance:
        """Get the current prepaid balance for the tenant, held in EUR.
        Only available for tenants with billing_model = 'prepaid'."""
        query = gql("""
            query GetPrepaidBalance {
              prepaidBalance {
                ...PrepaidBalanceDefaultFields
              }
            }

            fragment PrepaidBalanceDefaultFields on PrepaidBalance {
              id
              tenantId
              balanceCents
              reservedBalanceCents
              availableBalanceCents
              currency
              lowBalanceThresholdCents
              isLowBalance
              drainRateCentsPerHour
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetPrepaidBalance",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPrepaidBalance.model_validate(data)

    def get_preview_media_placement(
        self, input: PreviewMediaPlacementInput, **kwargs: Any
    ) -> GetPreviewMediaPlacement:
        """Read-only evaluation. Never claims ingest, reserves capacity, or starts a source pull."""
        query = gql("""
            query GetPreviewMediaPlacement($input: PreviewMediaPlacementInput!) {
              previewMediaPlacement(input: $input) {
                __typename
                ...MediaPlacementPreviewDefaultFields
                ...MediaPlacementErrorInMediaPlacementPreviewResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementErrorInMediaPlacementPreviewResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              mediaPlacementErrorParentRevision: parentRevision
              retryAfterSeconds
            }

            fragment MediaPlacementPreviewDefaultFields on MediaPlacementPreview {
              scope {
                kind
                streamId
              }
              verb
              revision
              parentRevision
              digest
              reason
              selected {
                clusterId
                clusterName
                region
                nodeId
                groupId
                reason
                distanceKm
                requiresSourcePull
                price {
                  amountMicros
                  currency
                  unit
                  revision
                  expiresAt
                }
              }
              candidates {
                clusterId
                clusterName
                region
                nodeId
                groupId
                reason
                distanceKm
                requiresSourcePull
                price {
                  amountMicros
                  currency
                  unit
                  revision
                  expiresAt
                }
              }
              transitions {
                fromGroup
                reason
              }
              observedAt
              expiresAt
              complete
              sourceEvaluated
              activeIngestClusterId
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="GetPreviewMediaPlacement",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetPreviewMediaPlacement.model_validate(data)

    def get_processing_usage_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        process_type: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetProcessingUsageConnection:
        query = gql("""
            query GetProcessingUsageConnection($page: ConnectionInput, $streamId: ID, $processType: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  processing {
                    processingUsageConnection(
                      page: $page
                      streamId: $streamId
                      processType: $processType
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...ProcessingUsageRecordDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                      summaries {
                        date
                        livepeerSeconds
                        livepeerSegmentCount
                        livepeerUniqueStreams
                        livepeerH264Seconds
                        livepeerVp9Seconds
                        livepeerAv1Seconds
                        livepeerHevcSeconds
                        nativeAvSeconds
                        nativeAvSegmentCount
                        nativeAvUniqueStreams
                        nativeAvH264Seconds
                        nativeAvVp9Seconds
                        nativeAvAv1Seconds
                        nativeAvHevcSeconds
                        nativeAvAacSeconds
                        nativeAvOpusSeconds
                        audioSeconds
                        videoSeconds
                      }
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ProcessingUsageRecordDefaultFields on ProcessingUsageRecord {
              id
              timestamp
              nodeId
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              processType
              clusterId
              originClusterId
              controlCellId
              trackType
              durationMs
              inputCodec
              outputCodec
              segmentNumber
              width
              height
              renditionCount
              broadcasterUrl
              uploadTimeUs
              livepeerSessionId
              segmentStartMs
              inputBytes
              outputBytesTotal
              attemptCount
              turnaroundMs
              speedFactor
              renditionsJson
              inputFrames
              outputFrames
              decodeUsPerFrame
              transformUsPerFrame
              encodeUsPerFrame
              isFinal
              inputFramesDelta
              outputFramesDelta
              inputBytesDelta
              outputBytesDelta
              inputWidth
              inputHeight
              outputWidth
              outputHeight
              inputFpks
              outputFpsMeasured
              sampleRate
              channels
              sourceTimestampMs
              sinkTimestampMs
              sourceAdvancedMs
              sinkAdvancedMs
              rtfIn
              rtfOut
              pipelineLagMs
              outputBitrateBps
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "processType": process_type,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetProcessingUsageConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetProcessingUsageConnection.model_validate(data)

    def get_quality_tier_daily_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetQualityTierDailyConnection:
        query = gql("""
            query GetQualityTierDailyConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    qualityTierDailyConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...QualityTierDailyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment QualityTierDailyDefaultFields on QualityTierDaily {
              id
              day
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              tier2160pMinutes
              tier1440pMinutes
              tier1080pMinutes
              tier720pMinutes
              tier480pMinutes
              tierSdMinutes
              primaryTier
              codecH264Minutes
              codecH265Minutes
              codecVp9Minutes
              codecAv1Minutes
              avgBitrate
              avgFps
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetQualityTierDailyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetQualityTierDailyConnection.model_validate(data)

    def get_rebuffering_events_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetRebufferingEventsConnection:
        query = gql("""
            query GetRebufferingEventsConnection($page: ConnectionInput, $streamId: ID, $nodeId: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  rebufferingEventsConnection(
                    page: $page
                    streamId: $streamId
                    nodeId: $nodeId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...RebufferingEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment RebufferingEventDefaultFields on RebufferingEvent {
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              bufferState
              previousState
              rebufferStart
              rebufferEnd
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "nodeId": node_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetRebufferingEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetRebufferingEventsConnection.model_validate(data)

    def get_recent_pull_source_events(
        self, id: str, limit: Union[Optional[int], UnsetType] = UNSET, **kwargs: Any
    ) -> GetRecentPullSourceEvents:
        """Recent pull-source resolution events for pull streams. Captures the
        customer-facing resolution outcome (resolved, not_found, disabled,
        blocked_uri, cluster_not_allowed_delegate, commodore_error,
        foghorn_base_unresolved). Null for push streams."""
        query = gql("""
            query GetRecentPullSourceEvents($id: ID!, $limit: Int = 50) {
              stream(id: $id) {
                recentPullSourceEvents(limit: $limit) {
                  ...PullSourceEventDefaultFields
                }
              }
            }

            fragment PullSourceEventDefaultFields on PullSourceEvent {
              id
              internalName
              eventKind
              detail
              createdAt
            }
            """)
        variables: dict[str, object] = {"id": id, "limit": limit}
        response = self.execute(
            query=query,
            operation_name="GetRecentPullSourceEvents",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetRecentPullSourceEvents.model_validate(data)

    def get_review_cluster_media_consent_change(
        self, input: ReviewMediaCapacityConsentInput, **kwargs: Any
    ) -> GetReviewClusterMediaConsentChange:
        query = gql("""
            query GetReviewClusterMediaConsentChange($input: ReviewMediaCapacityConsentInput!) {
              reviewClusterMediaConsentChange(input: $input) {
                __typename
                ...MediaPlacementReviewDefaultFields
                ...MediaPlacementErrorInMediaPlacementReviewResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementErrorInMediaPlacementReviewResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment MediaPlacementReviewDefaultFields on MediaPlacementReview {
              reviewToken
              digest
              expiresAt
              differences {
                path
                label
                before
                after
              }
              warnings {
                id
                severity
                message
                acknowledgementRequired
              }
              impact {
                affectedStreams
                activePublishers
                complete
                existingSessionsRetained
              }
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="GetReviewClusterMediaConsentChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetReviewClusterMediaConsentChange.model_validate(data)

    def get_review_media_placement_change(
        self, input: ReviewMediaPlacementChangeInput, **kwargs: Any
    ) -> GetReviewMediaPlacementChange:
        query = gql("""
            query GetReviewMediaPlacementChange($input: ReviewMediaPlacementChangeInput!) {
              reviewMediaPlacementChange(input: $input) {
                __typename
                ...MediaPlacementReviewDefaultFields
                ...MediaPlacementErrorInMediaPlacementReviewResultDefaultFields
                ...AuthErrorDefaultFields
                ...NotFoundErrorDefaultFields
              }
            }

            fragment AuthErrorDefaultFields on AuthError {
              message
              code
            }

            fragment MediaPlacementErrorInMediaPlacementReviewResultDefaultFields on MediaPlacementError {
              mediaPlacementErrorCode: code
              message
              fields {
                path
                groupId
                message
              }
              currentRevision
              parentRevision
              retryAfterSeconds
            }

            fragment MediaPlacementReviewDefaultFields on MediaPlacementReview {
              reviewToken
              digest
              expiresAt
              differences {
                path
                label
                before
                after
              }
              warnings {
                id
                severity
                message
                acknowledgementRequired
              }
              impact {
                affectedStreams
                activePublishers
                complete
                existingSessionsRetained
              }
            }

            fragment NotFoundErrorDefaultFields on NotFoundError {
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="GetReviewMediaPlacementChange",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetReviewMediaPlacementChange.model_validate(data)

    def get_routing_efficiency(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetRoutingEfficiency:
        """Pre-aggregated routing efficiency summary for dashboard views.
        Replaces client-side aggregation of raw routingEventsConnection data."""
        query = gql("""
            query GetRoutingEfficiency($streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                infra {
                  routingEfficiency(streamId: $streamId, timeRange: $timeRange, noCache: $noCache) {
                    ...RoutingEfficiencyDefaultFields
                  }
                }
              }
            }

            fragment RoutingEfficiencyDefaultFields on RoutingEfficiency {
              totalDecisions
              successCount
              successRate
              avgRoutingDistance
              avgLatencyMs
              topCountries {
                countryCode
                requestCount
              }
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetRoutingEfficiency",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetRoutingEfficiency.model_validate(data)

    def get_routing_events_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        subject_tenant_id: Union[Optional[str], UnsetType] = UNSET,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetRoutingEventsConnection:
        query = gql("""
            query GetRoutingEventsConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $subjectTenantId: ID, $clusterId: String, $noCache: Boolean = false) {
              analytics {
                infra {
                  routingEventsConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    subjectTenantId: $subjectTenantId
                    clusterId: $clusterId
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...RoutingEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment RoutingEventDefaultFields on RoutingEvent {
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              selectedNode
              nodeId
              status
              details
              score
              clientCountry
              clientLatitude
              clientLongitude
              clientBucket {
                h3Index
                resolution
              }
              nodeLatitude
              nodeLongitude
              nodeName
              nodeBucket {
                h3Index
                resolution
              }
              routingDistance
              candidatesCount
              latencyMs
              eventType
              source
              streamTenantId
              clusterId
              remoteClusterId
              selectedClusterId
              controlCellId
              originClusterId
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "subjectTenantId": subject_tenant_id,
            "clusterId": cluster_id,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetRoutingEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetRoutingEventsConnection.model_validate(data)

    def get_service_instances_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        cluster_id: Union[Optional[str], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        status: Union[Optional[InstanceStatus], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetServiceInstancesConnection:
        query = gql("""
            query GetServiceInstancesConnection($page: ConnectionInput, $clusterId: String, $nodeId: String, $status: InstanceStatus) {
              analytics {
                infra {
                  serviceInstancesConnection(
                    page: $page
                    clusterId: $clusterId
                    nodeId: $nodeId
                    status: $status
                  ) {
                    edges {
                      cursor
                      node {
                        ...ServiceInstanceDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ServiceInstanceDefaultFields on ServiceInstance {
              id
              instanceId
              clusterId
              nodeId
              serviceId
              version
              port
              processId
              containerId
              status
              healthStatus
              startedAt
              stoppedAt
              lastHealthCheck
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "clusterId": cluster_id,
            "nodeId": node_id,
            "status": status,
        }
        response = self.execute(
            query=query,
            operation_name="GetServiceInstancesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetServiceInstancesConnection.model_validate(data)

    def get_service_instances_health(
        self, service_id: Union[Optional[str], UnsetType] = UNSET, **kwargs: Any
    ) -> GetServiceInstancesHealth:
        """Check health status of service instances for owned infrastructure."""
        query = gql("""
            query GetServiceInstancesHealth($serviceId: String) {
              serviceInstancesHealth(serviceId: $serviceId) {
                ...ServiceInstanceHealthDefaultFields
              }
            }

            fragment ServiceInstanceHealthDefaultFields on ServiceInstanceHealth {
              instanceId
              serviceId
              clusterId
              protocol
              host
              port
              healthEndpoint
              status
              lastHealthCheck
            }
            """)
        variables: dict[str, object] = {"serviceId": service_id}
        response = self.execute(
            query=query,
            operation_name="GetServiceInstancesHealth",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetServiceInstancesHealth.model_validate(data)

    def get_session_qoe_summary(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        artifact_hash: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetSessionQoeSummary:
        """Viewer-experienced QoE summary (rebuffering, frame drops, bitrate, EBVS) for the
        tenant that owns the content. Read-time ratios over the player-reported session
        beacons. Diagnostic only — never billing/viewer-count truth."""
        query = gql("""
            query GetSessionQoeSummary($streamId: ID, $artifactHash: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  sessionQoeSummary(
                    streamId: $streamId
                    artifactHash: $artifactHash
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    ...SessionQoeSummaryDefaultFields
                  }
                }
              }
            }

            fragment SessionQoeSummaryDefaultFields on SessionQoeSummary {
              sessionCount
              playedHours
              rebufferingRatio
              rebuffersPerHour
              avgRebufferMs
              frameDropRatio
              playbackFailureRate
              ebvsRate
              avgBitrateBps
              abrSwitchesPerHour
              avgLiveEdgeLatencyMs
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "artifactHash": artifact_hash,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetSessionQoeSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSessionQoeSummary.model_validate(data)

    def get_session_qoe_time_series(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        artifact_hash: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        interval: Union[Optional[str], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetSessionQoeTimeSeries:
        """Viewer-experienced QoE bucketed over time (per-`interval` rebuffering/frame-drop/
        bitrate). Companion to sessionQoeSummary for trend charts. `sessionCount` and
        `playedHours` per bucket are the denominators for low-sample windows."""
        query = gql("""
            query GetSessionQoeTimeSeries($streamId: ID, $artifactHash: String, $timeRange: TimeRangeInput, $interval: String = "1h", $noCache: Boolean = false) {
              analytics {
                health {
                  sessionQoeTimeSeries(
                    streamId: $streamId
                    artifactHash: $artifactHash
                    timeRange: $timeRange
                    interval: $interval
                    noCache: $noCache
                  ) {
                    ...SessionQoeTimeSeriesBucketDefaultFields
                  }
                }
              }
            }

            fragment SessionQoeTimeSeriesBucketDefaultFields on SessionQoeTimeSeriesBucket {
              timestamp
              sessionCount
              playedHours
              rebufferingRatio
              frameDropRatio
              avgBitrateBps
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "artifactHash": artifact_hash,
            "timeRange": time_range,
            "interval": interval,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetSessionQoeTimeSeries",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSessionQoeTimeSeries.model_validate(data)

    def get_skipper_conversation(
        self, id: str, **kwargs: Any
    ) -> GetSkipperConversation:
        """Get a single Skipper conversation with full message history."""
        query = gql("""
            query GetSkipperConversation($id: ID!) {
              skipperConversation(id: $id) {
                ...SkipperConversationDefaultFields
              }
            }

            fragment SkipperConversationDefaultFields on SkipperConversation {
              id
              title
              messages {
                id
                role
                content
                confidence
                sources
                toolsUsed
                confidenceBlocks
                tokensInput
                tokensOutput
                createdAt
              }
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="GetSkipperConversation",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSkipperConversation.model_validate(data)

    def get_skipper_conversations(
        self,
        limit: Union[Optional[int], UnsetType] = UNSET,
        offset: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetSkipperConversations:
        """List Skipper AI consultant conversations for the current user."""
        query = gql("""
            query GetSkipperConversations($limit: Int, $offset: Int) {
              skipperConversations(limit: $limit, offset: $offset) {
                ...SkipperConversationSummaryDefaultFields
              }
            }

            fragment SkipperConversationSummaryDefaultFields on SkipperConversationSummary {
              id
              title
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"limit": limit, "offset": offset}
        response = self.execute(
            query=query,
            operation_name="GetSkipperConversations",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSkipperConversations.model_validate(data)

    def get_skipper_report(self, id: str, **kwargs: Any) -> GetSkipperReport:
        """Fetch a single Skipper investigation report by id.
        Returns null when the report does not exist or is not owned by the current tenant."""
        query = gql("""
            query GetSkipperReport($id: ID!) {
              skipperReport(id: $id) {
                ...SkipperReportDefaultFields
              }
            }

            fragment SkipperReportDefaultFields on SkipperReport {
              id
              trigger
              summary
              metricsReviewed
              rootCause
              recommendations {
                text
                confidence
              }
              createdAt
              readAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="GetSkipperReport",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSkipperReport.model_validate(data)

    def get_skipper_reports(
        self,
        limit: Union[Optional[int], UnsetType] = UNSET,
        offset: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetSkipperReports:
        """List Skipper investigation reports for the current tenant.
        Returns newest first with read/unread status and counts."""
        query = gql("""
            query GetSkipperReports($limit: Int, $offset: Int) {
              skipperReports(limit: $limit, offset: $offset) {
                ...SkipperReportsConnectionDefaultFields
              }
            }

            fragment SkipperReportsConnectionDefaultFields on SkipperReportsConnection {
              nodes {
                id
                trigger
                summary
                metricsReviewed
                rootCause
                recommendations {
                  text
                  confidence
                }
                createdAt
                readAt
              }
              totalCount
              unreadCount
            }
            """)
        variables: dict[str, object] = {"limit": limit, "offset": offset}
        response = self.execute(
            query=query,
            operation_name="GetSkipperReports",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSkipperReports.model_validate(data)

    def get_skipper_unread_report_count(
        self, **kwargs: Any
    ) -> GetSkipperUnreadReportCount:
        """Get count of unread Skipper investigation reports."""
        query = gql("""
            query GetSkipperUnreadReportCount {
              skipperUnreadReportCount
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetSkipperUnreadReportCount",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetSkipperUnreadReportCount.model_validate(data)

    def get_storage_events_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        asset_type: Union[Optional[str], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStorageEventsConnection:
        query = gql("""
            query GetStorageEventsConnection($page: ConnectionInput, $assetType: String, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  storageEventsConnection(
                    page: $page
                    assetType: $assetType
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...StorageEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StorageEventDefaultFields on StorageEvent {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              assetHash
              action
              assetType
              sizeBytes
              s3Url
              localPath
              nodeId
              clusterId
              originClusterId
              controlCellId
              durationMs
              warmDurationMs
              error
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "assetType": asset_type,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStorageEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStorageEventsConnection.model_validate(data)

    def get_storage_usage_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        node_id: Union[Optional[str], UnsetType] = UNSET,
        storage_scope: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStorageUsageConnection:
        query = gql("""
            query GetStorageUsageConnection($page: ConnectionInput, $nodeId: String, $storageScope: String, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  storage {
                    storageUsageConnection(
                      page: $page
                      nodeId: $nodeId
                      storageScope: $storageScope
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...StorageUsageRecordDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StorageUsageRecordDefaultFields on StorageUsageRecord {
              id
              timestamp
              nodeId
              storageScope
              totalBytes
              fileCount
              dvrBytes
              clipBytes
              vodBytes
              frozenDvrBytes
              frozenClipBytes
              frozenVodBytes
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "nodeId": node_id,
            "storageScope": storage_scope,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStorageUsageConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStorageUsageConnection.model_validate(data)

    def get_stream_analytics_daily_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamAnalyticsDailyConnection:
        query = gql("""
            query GetStreamAnalyticsDailyConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    streamAnalyticsDailyConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...StreamAnalyticsDailyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamAnalyticsDailyDefaultFields on StreamAnalyticsDaily {
              id
              day
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              totalViews
              uniqueViewers
              uniqueCountries
              uniqueCities
              egressBytes
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamAnalyticsDailyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamAnalyticsDailyConnection.model_validate(data)

    def get_stream_analytics_summaries_connection(
        self,
        time_range: TimeRangeInput,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        sort_by: Union[Optional[StreamSummarySortField], UnsetType] = UNSET,
        sort_order: Union[Optional[SortOrder], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamAnalyticsSummariesConnection:
        """Pre-aggregated analytics summaries for multiple streams.
        Returns sorted, paginated results with tenant-wide share percentages."""
        query = gql("""
            query GetStreamAnalyticsSummariesConnection($page: ConnectionInput, $timeRange: TimeRangeInput!, $sortBy: StreamSummarySortField = EGRESS_GB, $sortOrder: SortOrder = DESC) {
              analytics {
                usage {
                  streaming {
                    streamAnalyticsSummariesConnection(
                      page: $page
                      timeRange: $timeRange
                      sortBy: $sortBy
                      sortOrder: $sortOrder
                    ) {
                      edges {
                        cursor
                        node {
                          ...StreamAnalyticsSummaryDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamAnalyticsSummaryDefaultFields on StreamAnalyticsSummary {
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              timeRange {
                start
                end
              }
              rangeAvgViewers
              rangePeakConcurrentViewers
              rangeTotalViews
              rangeTotalSessions
              rangeAvgBufferHealth
              rangeAvgBitrate
              rangeAvgFps
              rangePacketLossRate
              rangeAvgConnectionTime
              rangeViewerHours
              rangeEgressGb
              rangeAvgSessionSeconds
              rangeAvgBytesPerSession
              rangeUniqueViewers
              rangeUniqueCountries
              rangeRebufferCount
              rangeIssueCount
              rangeBufferDryCount
              rangeQuality {
                tier2160pMinutes
                tier1440pMinutes
                tier1080pMinutes
                tier720pMinutes
                tier480pMinutes
                tierSdMinutes
                codecH264Minutes
                codecH265Minutes
                codecVp9Minutes
                codecAv1Minutes
              }
              rangeEgressSharePercent
              rangeViewerSharePercent
              rangeViewerHoursSharePercent
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "timeRange": time_range,
            "sortBy": sort_by,
            "sortOrder": sort_order,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamAnalyticsSummariesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamAnalyticsSummariesConnection.model_validate(data)

    def get_stream_analytics_summary(
        self,
        stream_id: str,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamAnalyticsSummary:
        query = gql("""
            query GetStreamAnalyticsSummary($streamId: ID!, $timeRange: TimeRangeInput) {
              analytics {
                usage {
                  streaming {
                    streamAnalyticsSummary(streamId: $streamId, timeRange: $timeRange) {
                      ...StreamAnalyticsSummaryDefaultFields
                    }
                  }
                }
              }
            }

            fragment StreamAnalyticsSummaryDefaultFields on StreamAnalyticsSummary {
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              timeRange {
                start
                end
              }
              rangeAvgViewers
              rangePeakConcurrentViewers
              rangeTotalViews
              rangeTotalSessions
              rangeAvgBufferHealth
              rangeAvgBitrate
              rangeAvgFps
              rangePacketLossRate
              rangeAvgConnectionTime
              rangeViewerHours
              rangeEgressGb
              rangeAvgSessionSeconds
              rangeAvgBytesPerSession
              rangeUniqueViewers
              rangeUniqueCountries
              rangeRebufferCount
              rangeIssueCount
              rangeBufferDryCount
              rangeQuality {
                tier2160pMinutes
                tier1440pMinutes
                tier1080pMinutes
                tier720pMinutes
                tier480pMinutes
                tierSdMinutes
                codecH264Minutes
                codecH265Minutes
                codecVp9Minutes
                codecAv1Minutes
              }
              rangeEgressSharePercent
              rangeViewerSharePercent
              rangeViewerHoursSharePercent
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id, "timeRange": time_range}
        response = self.execute(
            query=query,
            operation_name="GetStreamAnalyticsSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamAnalyticsSummary.model_validate(data)

    def get_stream_connection_hourly_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamConnectionHourlyConnection:
        query = gql("""
            query GetStreamConnectionHourlyConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    streamConnectionHourlyConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...StreamConnectionHourlyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamConnectionHourlyDefaultFields on StreamConnectionHourly {
              id
              hour
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              totalBytes
              uniqueViewers
              totalSessions
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamConnectionHourlyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamConnectionHourlyConnection.model_validate(data)

    def get_stream_events_connection(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamEventsConnection:
        query = gql("""
            query GetStreamEventsConnection($page: ConnectionInput, $streamId: ID!, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  streamEventsConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...StreamEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamEventDefaultFields on StreamEvent {
              id
              eventId
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              type
              status
              timestamp
              details
              payload
              source
              bufferState
              hasIssues
              trackCount
              qualityTier
              primaryWidth
              primaryHeight
              primaryFps
              primaryCodec
              primaryBitrate
              downloadedBytes
              uploadedBytes
              totalViewers
              totalInputs
              totalOutputs
              viewerSeconds
              requestUrl
              protocol
              latitude
              longitude
              location
              countryCode
              city
              sourceRegion
              sourceClusterId
              streamOriginRegion
              streamOriginClusterId
              schemaVersion
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamEventsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamEventsConnection.model_validate(data)

    def get_stream_health_5_m_connection(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamHealth5mConnection:
        query = gql("""
            query GetStreamHealth5mConnection($page: ConnectionInput, $streamId: ID!, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  streamHealth5mConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...StreamHealth5mDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamHealth5mDefaultFields on StreamHealth5m {
              id
              timestamp
              nodeId
              rebufferCount
              issueCount
              sampleIssues
              avgBitrate
              avgFps
              avgBufferHealth
              avgFrameJitterMs
              maxFrameJitterMs
              bufferDryCount
              qualityTier
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamHealth5mConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamHealth5mConnection.model_validate(data)

    def get_stream_health_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamHealthConnection:
        query = gql("""
            query GetStreamHealthConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  streamHealthConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...StreamHealthMetricDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamHealthMetricDefaultFields on StreamHealthMetric {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              issuesDescription
              hasIssues
              bitrate
              fps
              width
              height
              codec
              qualityTier
              gopSize
              frameMsMax
              frameMsMin
              framesMax
              framesMin
              keyframeMsMax
              keyframeMsMin
              frameJitterMs
              trackCount
              bufferState
              bufferHealth
              bufferSize
              audioChannels
              audioSampleRate
              audioCodec
              audioBitrate
              trackMetadata
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamHealthConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamHealthConnection.model_validate(data)

    def get_stream_health_summary(
        self,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetStreamHealthSummary:
        """Pre-aggregated health summary for dashboard views.
        Replaces paginated streamHealthConnection when only scalar stats are needed."""
        query = gql("""
            query GetStreamHealthSummary($streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  streamHealthSummary(
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    ...StreamHealthSummaryDefaultFields
                  }
                }
              }
            }

            fragment StreamHealthSummaryDefaultFields on StreamHealthSummary {
              avgBitrate
              avgFps
              avgBufferHealth
              totalRebufferCount
              totalIssueCount
              sampleCount
              hasActiveIssues
              currentQualityTier
            }
            """)
        variables: dict[str, object] = {
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetStreamHealthSummary",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamHealthSummary.model_validate(data)

    def get_streaming_config(self, **kwargs: Any) -> GetStreamingConfig:
        """Cluster-aware streaming configuration for the authenticated tenant.
        Returns preferred and official cluster domains for building protocol-specific URLs.
        Null when not authenticated or cluster routing unavailable — frontend falls back to env vars."""
        query = gql("""
            query GetStreamingConfig {
              streamingConfig {
                ...StreamingConfigDefaultFields
              }
            }

            fragment StreamingConfigDefaultFields on StreamingConfig {
              preferredClusterLabel
              ingestDomain
              edgeDomain
              playDomain
              officialClusterLabel
              officialIngestDomain
              officialEdgeDomain
              officialPlayDomain
              globalIngestDomain
              globalEdgeDomain
              globalPlayDomain
              globalLivepeerDomain
              tenantIngestDomain
              tenantEdgeDomain
              tenantPlayDomain
              tenantLivepeerDomain
              srtPort
              rtmpPort
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetStreamingConfig",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetStreamingConfig.model_validate(data)

    def get_tenant(self, **kwargs: Any) -> GetTenant:
        """Get the current tenant's profile and settings."""
        query = gql("""
            query GetTenant {
              tenant {
                ...TenantDefaultFields
              }
            }

            fragment TenantDefaultFields on Tenant {
              id
              name
              subdomain
              cluster
              createdAt
              customDomain
              customDomainStatus {
                domain
                state
                requiredTrafficCname
                requiredAcmeChallengeCname
                lastVerifiedAt
                certIssuedAt
                certExpiresAt
                lastError
              }
              monitoringEnabled
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query, operation_name="GetTenant", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetTenant.model_validate(data)

    def get_tenant_analytics_daily_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetTenantAnalyticsDailyConnection:
        query = gql("""
            query GetTenantAnalyticsDailyConnection($page: ConnectionInput, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    tenantAnalyticsDailyConnection(
                      page: $page
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...TenantAnalyticsDailyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment TenantAnalyticsDailyDefaultFields on TenantAnalyticsDaily {
              id
              day
              totalStreams
              totalViews
              uniqueViewers
              egressBytes
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetTenantAnalyticsDailyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetTenantAnalyticsDailyConnection.model_validate(data)

    def get_top_assets(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        limit: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetTopAssets:
        """Top assets by audience in the window — server-ranked by sessions, across every kind
        (VOD, clip, DVR, chapter). Backs the "Top Assets" table on the analytics overview.
        title/playbackId are composed from the catalog by artifactHash."""
        query = gql("""
            query GetTopAssets($timeRange: TimeRangeInput, $limit: Int = 10) {
              analytics {
                health {
                  topAssets(timeRange: $timeRange, limit: $limit) {
                    ...TopAssetEntryDefaultFields
                  }
                }
              }
            }

            fragment TopAssetEntryDefaultFields on TopAssetEntry {
              artifactHash
              kind
              totalSessions
              watchHours
              durationS
              title
              playbackId
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range, "limit": limit}
        response = self.execute(
            query=query, operation_name="GetTopAssets", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetTopAssets.model_validate(data)

    def get_track_list_connection(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetTrackListConnection:
        query = gql("""
            query GetTrackListConnection($page: ConnectionInput, $streamId: ID!, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  trackListConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...TrackListEventDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment TrackListEventDefaultFields on TrackListEvent {
              id
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              trackList
              trackCount
              timestamp
              tracks {
                trackName
                trackType
                codec
                bitrateKbps
                bitrateBps
                buffer
                jitter
                width
                height
                fps
                resolution
                hasBFrames
                channels
                sampleRate
              }
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetTrackListConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetTrackListConnection.model_validate(data)

    def get_validate_stream_key(
        self, stream_key: str, **kwargs: Any
    ) -> GetValidateStreamKey:
        """Validate a stream key and return the associated stream info."""
        query = gql("""
            query GetValidateStreamKey($streamKey: String!) {
              validateStreamKey(streamKey: $streamKey) {
                ...StreamValidationDefaultFields
              }
            }

            fragment StreamValidationDefaultFields on StreamValidation {
              status
              streamKey
              error
            }
            """)
        variables: dict[str, object] = {"streamKey": stream_key}
        response = self.execute(
            query=query,
            operation_name="GetValidateStreamKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetValidateStreamKey.model_validate(data)

    def get_viewer_geo_hourly_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetViewerGeoHourlyConnection:
        query = gql("""
            query GetViewerGeoHourlyConnection($page: ConnectionInput, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    viewerGeoHourlyConnection(page: $page, timeRange: $timeRange, noCache: $noCache) {
                      edges {
                        cursor
                        node {
                          ...ViewerGeoHourlyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ViewerGeoHourlyDefaultFields on ViewerGeoHourly {
              id
              hour
              countryCode
              viewerCount
              viewerHours
              egressGb
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetViewerGeoHourlyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetViewerGeoHourlyConnection.model_validate(data)

    def get_viewer_geographics_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetViewerGeographicsConnection:
        query = gql("""
            query GetViewerGeographicsConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput) {
              analytics {
                usage {
                  streaming {
                    viewerGeographicsConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                    ) {
                      edges {
                        cursor
                        node {
                          ...ViewerGeographicDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ViewerGeographicDefaultFields on ViewerGeographic {
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              countryCode
              city
              latitude
              longitude
              viewerCount
              connectionAddr
              eventType
              source
              sessionDurationSeconds
              bytesTransferred
              connector
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
        }
        response = self.execute(
            query=query,
            operation_name="GetViewerGeographicsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetViewerGeographicsConnection.model_validate(data)

    def get_viewer_hours_hourly_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetViewerHoursHourlyConnection:
        query = gql("""
            query GetViewerHoursHourlyConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                usage {
                  streaming {
                    viewerHoursHourlyConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                      noCache: $noCache
                    ) {
                      edges {
                        cursor
                        node {
                          ...ViewerHoursHourlyDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ViewerHoursHourlyDefaultFields on ViewerHoursHourly {
              id
              hour
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              countryCode
              uniqueViewers
              totalSessionSeconds
              totalBytes
              viewerHours
              egressGb
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetViewerHoursHourlyConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetViewerHoursHourlyConnection.model_validate(data)

    def get_viewer_sessions_connection(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetViewerSessionsConnection:
        query = gql("""
            query GetViewerSessionsConnection($page: ConnectionInput, $streamId: ID, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                lifecycle {
                  viewerSessionsConnection(
                    page: $page
                    streamId: $streamId
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    edges {
                      cursor
                      node {
                        ...ViewerSessionDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ViewerSessionDefaultFields on ViewerSession {
              id
              timestamp
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
              nodeId
              sessionId
              connectedAt
              disconnectedAt
              connector
              countryCode
              city
              latitude
              longitude
              durationSeconds
              bytesUp
              bytesDown
              connectionQuality
              bufferHealth
              clientBucket {
                h3Index
                resolution
              }
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetViewerSessionsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetViewerSessionsConnection.model_validate(data)

    def get_viewer_time_series_connection(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        interval: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetViewerTimeSeriesConnection:
        query = gql("""
            query GetViewerTimeSeriesConnection($page: ConnectionInput, $streamId: ID!, $timeRange: TimeRangeInput, $interval: String) {
              analytics {
                usage {
                  streaming {
                    viewerTimeSeriesConnection(
                      page: $page
                      streamId: $streamId
                      timeRange: $timeRange
                      interval: $interval
                    ) {
                      edges {
                        cursor
                        node {
                          ...ViewerCountBucketDefaultFields
                        }
                      }
                      pageInfo {
                        ...PageInfoDefaultFields
                      }
                      totalCount
                    }
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment ViewerCountBucketDefaultFields on ViewerCountBucket {
              timestamp
              viewerCount
              streamId
              stream {
                id
                streamId
                name
                description
                streamKey
                playbackId
                record
                ingestMode
                pullSource {
                  sourceUriRedacted
                  enabled
                  class
                }
                managedSource {
                  sourceKind
                  alwaysOn
                  placementCount
                }
                sourceLocation {
                  mode
                  avoidNodeIds
                }
                createdAt
                updatedAt
                metrics {
                  status
                  isLive
                  currentViewers
                  startedAt
                  updatedAt
                  nodeId
                  trackCount
                  totalInputs
                  uploadedBytes
                  downloadedBytes
                  viewerSeconds
                  packetsSent
                  packetsLost
                  packetsRetransmitted
                  bufferState
                  qualityTier
                  primaryWidth
                  primaryHeight
                  primaryFps
                  primaryCodec
                  primaryBitrate
                  hasIssues
                  issuesDescription
                }
                pushTargets {
                  id
                  streamId
                  platform
                  name
                  targetUri
                  isEnabled
                  status
                  lastError
                  reasonCode
                  lastPushedAt
                  createdAt
                }
                playbackPolicy {
                  type
                }
                dvrChapterMode
                dvrChapterIntervalSeconds
                monitoring
                thumbnailAssets {
                  posterUrl
                  spriteVttUrl
                  spriteJpgUrl
                  assetKey
                }
                retentionOverrides {
                  streamId
                  dvrRetentionDaysOverride
                  clipRetentionDaysOverride
                }
              }
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "streamId": stream_id,
            "timeRange": time_range,
            "interval": interval,
        }
        response = self.execute(
            query=query,
            operation_name="GetViewerTimeSeriesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetViewerTimeSeriesConnection.model_validate(data)

    def get_vod_retention(
        self,
        artifact_hash: str,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetVodRetention:
        """VOD retention curve for one artifact: per-bucket watched-seconds (density / "most
        replayed") plus reached-count audience retention (sessions reaching each bucket).
        `artifactHash` is required."""
        query = gql("""
            query GetVodRetention($artifactHash: String!, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  vodRetention(
                    artifactHash: $artifactHash
                    timeRange: $timeRange
                    noCache: $noCache
                  ) {
                    ...VodRetentionDefaultFields
                  }
                }
              }
            }

            fragment VodRetentionDefaultFields on VodRetention {
              bucketWidthS
              assetDurationS
              totalSessions
              points {
                bucketIndex
                secondsWatched
                reached
              }
            }
            """)
        variables: dict[str, object] = {
            "artifactHash": artifact_hash,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query, operation_name="GetVodRetention", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetVodRetention.model_validate(data)

    def get_vod_retention_assets(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        no_cache: Union[Optional[bool], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetVodRetentionAssets:
        """VOD assets that have retention data in the window — the picker backing the
        retention view. Eligibility (VOD content with real reach samples) is owned by
        analytics; title/playbackId are composed from the catalog by artifactHash."""
        query = gql("""
            query GetVodRetentionAssets($page: ConnectionInput, $timeRange: TimeRangeInput, $noCache: Boolean = false) {
              analytics {
                health {
                  vodRetentionAssets(page: $page, timeRange: $timeRange, noCache: $noCache) {
                    edges {
                      cursor
                      node {
                        ...VodRetentionAssetDefaultFields
                      }
                    }
                    pageInfo {
                      ...PageInfoDefaultFields
                    }
                    totalCount
                  }
                }
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment VodRetentionAssetDefaultFields on VodRetentionAsset {
              artifactHash
              totalSessions
              durationS
              lastSeen
              title
              playbackId
            }
            """)
        variables: dict[str, object] = {
            "page": page,
            "timeRange": time_range,
            "noCache": no_cache,
        }
        response = self.execute(
            query=query,
            operation_name="GetVodRetentionAssets",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetVodRetentionAssets.model_validate(data)

    def get_webhook_deliveries_connection(
        self,
        endpoint_id: Union[Optional[str], UnsetType] = UNSET,
        statuses: Union[Optional[list[WebhookDeliveryStatus]], UnsetType] = UNSET,
        event_type: Union[Optional[str], UnsetType] = UNSET,
        event_id: Union[Optional[str], UnsetType] = UNSET,
        created_after: Union[Optional[datetime], UnsetType] = UNSET,
        created_before: Union[Optional[datetime], UnsetType] = UNSET,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetWebhookDeliveriesConnection:
        """List webhook deliveries newest first, optionally filtered. Only forward
        pagination is supported."""
        query = gql("""
            query GetWebhookDeliveriesConnection($endpointId: ID, $statuses: [WebhookDeliveryStatus!], $eventType: String, $eventId: ID, $createdAfter: Time, $createdBefore: Time, $page: ConnectionInput) {
              webhookDeliveriesConnection(
                endpointId: $endpointId
                statuses: $statuses
                eventType: $eventType
                eventId: $eventId
                createdAfter: $createdAfter
                createdBefore: $createdBefore
                page: $page
              ) {
                edges {
                  cursor
                  node {
                    ...WebhookDeliveryDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment WebhookDeliveryDefaultFields on WebhookDelivery {
              id
              endpointId
              eventId
              eventType
              kind
              status
              attempts
              nextAttemptAt
              lastStatusCode
              lastErrorClass
              deliveredAt
              replayCount
              lastReplayedAt
              createdAt
              updatedAt
              attemptHistory {
                id
                attemptNumber
                statusCode
                errorClass
                latencyMs
                responseExcerpt
                attemptedAt
              }
            }
            """)
        variables: dict[str, object] = {
            "endpointId": endpoint_id,
            "statuses": statuses,
            "eventType": event_type,
            "eventId": event_id,
            "createdAfter": created_after,
            "createdBefore": created_before,
            "page": page,
        }
        response = self.execute(
            query=query,
            operation_name="GetWebhookDeliveriesConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetWebhookDeliveriesConnection.model_validate(data)

    def get_webhook_delivery(self, id: str, **kwargs: Any) -> GetWebhookDelivery:
        """Get one webhook delivery with every HTTP attempt."""
        query = gql("""
            query GetWebhookDelivery($id: ID!) {
              webhookDelivery(id: $id) {
                ...WebhookDeliveryDefaultFields
              }
            }

            fragment WebhookDeliveryDefaultFields on WebhookDelivery {
              id
              endpointId
              eventId
              eventType
              kind
              status
              attempts
              nextAttemptAt
              lastStatusCode
              lastErrorClass
              deliveredAt
              replayCount
              lastReplayedAt
              createdAt
              updatedAt
              attemptHistory {
                id
                attemptNumber
                statusCode
                errorClass
                latencyMs
                responseExcerpt
                attemptedAt
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="GetWebhookDelivery",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetWebhookDelivery.model_validate(data)

    def get_webhook_endpoint(self, id: str, **kwargs: Any) -> GetWebhookEndpoint:
        """Get one of the tenant's webhook endpoints."""
        query = gql("""
            query GetWebhookEndpoint($id: ID!) {
              webhookEndpoint(id: $id) {
                ...WebhookEndpointDefaultFields
              }
            }

            fragment WebhookEndpointDefaultFields on WebhookEndpoint {
              id
              url
              description
              eventTypes
              apiVersion
              status
              disabledReason
              disabledAt
              consecutiveFailures
              failingSince
              lastSuccessAt
              lastFailureAt
              previousSecretExpiresAt
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="GetWebhookEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetWebhookEndpoint.model_validate(data)

    def get_webhook_endpoints_connection(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> GetWebhookEndpointsConnection:
        """List the tenant's webhook endpoints, oldest first. A tenant has at most 10,
        so one page holds them all."""
        query = gql("""
            query GetWebhookEndpointsConnection($page: ConnectionInput) {
              webhookEndpointsConnection(page: $page) {
                edges {
                  cursor
                  node {
                    ...WebhookEndpointDefaultFields
                  }
                }
                pageInfo {
                  ...PageInfoDefaultFields
                }
                totalCount
              }
            }

            fragment PageInfoDefaultFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment WebhookEndpointDefaultFields on WebhookEndpoint {
              id
              url
              description
              eventTypes
              apiVersion
              status
              disabledReason
              disabledAt
              consecutiveFailures
              failingSince
              lastSuccessAt
              lastFailureAt
              previousSecretExpiresAt
              createdAt
              updatedAt
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="GetWebhookEndpointsConnection",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetWebhookEndpointsConnection.model_validate(data)

    def get_webhook_event_types(self, **kwargs: Any) -> GetWebhookEventTypes:
        """The public event types an endpoint can subscribe to, as used in
        eventTypes. "*" subscribes to every type."""
        query = gql("""
            query GetWebhookEventTypes {
              webhookEventTypes
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query,
            operation_name="GetWebhookEventTypes",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetWebhookEventTypes.model_validate(data)

    def create_developer_token(
        self, input: CreateDeveloperTokenInput, **kwargs: Any
    ) -> CreateDeveloperToken:
        """Create a new API token for programmatic access."""
        query = gql("""
            mutation CreateDeveloperToken($input: CreateDeveloperTokenInput!) {
              createDeveloperToken(input: $input) {
                __typename
                ...DeveloperTokenFields
                ...ValidationErrorFields
                ...RateLimitErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeveloperTokenFields on DeveloperToken {
              __typename
              id
              tokenName
              tokenValue
              permissions
              status
              lastUsedAt
              expiresAt
              createdAt
            }

            fragment RateLimitErrorFields on RateLimitError {
              __typename
              message
              code
              retryAfter
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateDeveloperToken",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateDeveloperToken.model_validate(data)

    def revoke_developer_token(self, id: str, **kwargs: Any) -> RevokeDeveloperToken:
        """Revoke an API token."""
        query = gql("""
            mutation RevokeDeveloperToken($id: ID!) {
              revokeDeveloperToken(id: $id) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="RevokeDeveloperToken",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RevokeDeveloperToken.model_validate(data)

    def create_signing_key(
        self, input: CreateSigningKeyInput, **kwargs: Any
    ) -> CreateSigningKey:
        """Generate a new ES256 playback signing keypair. The private key is returned
        ONCE in the response and never stored or returned again — capture it.
        Up to 10 active keys per tenant; revoke before re-creating."""
        query = gql("""
            mutation CreateSigningKey($input: CreateSigningKeyInput!) {
              createSigningKey(input: $input) {
                __typename
                ... on CreateSigningKeySuccess {
                  signingKey {
                    ...SigningKeyFields
                  }
                  privateKeyPem
                }
                ...ValidationErrorFields
                ...RateLimitErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment RateLimitErrorFields on RateLimitError {
              __typename
              message
              code
              retryAfter
            }

            fragment SigningKeyFields on SigningKey {
              __typename
              id
              kid
              name
              algorithm
              publicKeyPem
              status
              createdAt
              lastUsedAt
              revokedAt
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CreateSigningKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateSigningKey.model_validate(data)

    def revoke_signing_key(self, id: str, **kwargs: Any) -> RevokeSigningKey:
        """Mark an active signing key as revoked. Triggers session re-evaluation
        across the tenant's protected playback objects: viewers with valid auth
        continue (possibly with a brief reconnect), revoked viewers are denied."""
        query = gql("""
            mutation RevokeSigningKey($id: ID!) {
              revokeSigningKey(id: $id) {
                __typename
                ...SigningKeyFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment SigningKeyFields on SigningKey {
              __typename
              id
              kid
              name
              algorithm
              publicKeyPem
              status
              createdAt
              lastUsedAt
              revokedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="RevokeSigningKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RevokeSigningKey.model_validate(data)

    def set_playback_policy(
        self, input: SetPlaybackPolicyInput, **kwargs: Any
    ) -> SetPlaybackPolicy:
        """Set or clear the playback access policy on a stream, VOD asset, or clip.
        Exactly one of streamId / vodAssetId / clipId must be set in the input.
        Webhook secrets are write-only on input and never returned in queries.
        Mutating a policy invalidates Foghorn caches and re-runs USER_NEW for
        affected sessions; valid viewers continue, invalid ones are denied."""
        query = gql("""
            mutation SetPlaybackPolicy($input: SetPlaybackPolicyInput!) {
              setPlaybackPolicy(input: $input) {
                __typename
                ... on Stream {
                  id
                  playbackPolicy {
                    ...PlaybackPolicyFields
                  }
                }
                ... on VodAsset {
                  id
                  playbackPolicy {
                    ...PlaybackPolicyFields
                  }
                }
                ... on Clip {
                  id
                  playbackPolicy {
                    ...PlaybackPolicyFields
                  }
                }
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="SetPlaybackPolicy",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SetPlaybackPolicy.model_validate(data)

    def test_playback_access(
        self, input: TestPlaybackAccessInput, **kwargs: Any
    ) -> TestPlaybackAccess:
        """Run the same evaluator the live USER_NEW path uses against a caller-
        supplied JWT (or webhook test request) without registering a viewer
        session. Mutation, not query, because webhook mode (fireWebhook=true)
        fires a real outbound HTTPS request to the customer URL.
        Tenant ownership of the playback target is validated server-side."""
        query = gql("""
            mutation TestPlaybackAccess($input: TestPlaybackAccessInput!) {
              testPlaybackAccess(input: $input) {
                __typename
                ... on PlaybackAccessDecision {
                  allowed
                  policyType
                  reason
                  detail
                  kid
                  claimsJson
                  webhookStatus
                  webhookLatencyMs
                  resolvedInternalName
                }
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="TestPlaybackAccess",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return TestPlaybackAccess.model_validate(data)

    def create_clip(self, input: CreateClipInput, **kwargs: Any) -> CreateClip:
        """Create a clip from a live or recorded stream.
        Clips are short video segments extracted from a stream."""
        query = gql("""
            mutation CreateClip($input: CreateClipInput!) {
              createClip(input: $input) {
                __typename
                ...ClipFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment ClipFields on Clip {
              __typename
              id
              clipHash
              playbackId
              streamId
              title
              description
              startTime
              duration
              sizeBytes
              status
              clipMode
              createdAt
              updatedAt
              expiresAt
              isExpired
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              thumbnailAssets {
                ...ThumbnailAssetsFields
              }
              effectiveRetention {
                ...EffectiveRetentionFields
              }
            }

            fragment EffectiveRetentionFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="CreateClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateClip.model_validate(data)

    def delete_clip(self, id: str, **kwargs: Any) -> DeleteClip:
        """Delete a clip."""
        query = gql("""
            mutation DeleteClip($id: ID!) {
              deleteClip(id: $id) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="DeleteClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteClip.model_validate(data)

    def start_dvr(self, stream_id: str, **kwargs: Any) -> StartDVR:
        """Start DVR recording for a live stream.
        DVR creates one continuous archive session. Live seekback is bounded by
        the resolved DVR policy; archive playback uses virtual chapters."""
        query = gql("""
            mutation StartDVR($streamId: ID!) {
              startDVR(streamId: $streamId) {
                __typename
                ...DVRRequestFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DVRRequestFields on DVRRequest {
              __typename
              id
              dvrHash
              playbackId
              streamId
              title
              status
              createdAt
              updatedAt
              startedAt
              endedAt
              expiresAt
              isExpired
              durationSeconds
              sizeBytes
              errorMessage
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id}
        response = self.execute(
            query=query, operation_name="StartDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return StartDVR.model_validate(data)

    def stop_dvr(self, dvr_hash: str, **kwargs: Any) -> StopDVR:
        """Stop DVR recording for a stream."""
        query = gql("""
            mutation StopDVR($dvrHash: ID!) {
              stopDVR(dvrHash: $dvrHash) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"dvrHash": dvr_hash}
        response = self.execute(
            query=query, operation_name="StopDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return StopDVR.model_validate(data)

    def delete_dvr(self, dvr_hash: str, **kwargs: Any) -> DeleteDVR:
        """Delete a DVR recording."""
        query = gql("""
            mutation DeleteDVR($dvrHash: ID!) {
              deleteDVR(dvrHash: $dvrHash) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"dvrHash": dvr_hash}
        response = self.execute(
            query=query, operation_name="DeleteDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteDVR.model_validate(data)

    def create_vod_upload(
        self, input: CreateVodUploadInput, **kwargs: Any
    ) -> CreateVodUpload:
        """Create a new VOD upload session.
        Returns presigned URLs for multipart upload."""
        query = gql("""
            mutation CreateVodUpload($input: CreateVodUploadInput!) {
              createVodUpload(input: $input) {
                __typename
                ... on VodUploadSession {
                  id
                  artifactId
                  artifactHash
                  playbackId
                  partSize
                  parts {
                    partNumber
                    presignedUrl
                  }
                  expiresAt
                }
                ...ValidationErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="CreateVodUpload", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateVodUpload.model_validate(data)

    def complete_vod_upload(
        self, input: CompleteVodUploadInput, **kwargs: Any
    ) -> CompleteVodUpload:
        """Complete a VOD upload after all parts are uploaded.
        Triggers processing and thumbnail generation."""
        query = gql("""
            mutation CompleteVodUpload($input: CompleteVodUploadInput!) {
              completeVodUpload(input: $input) {
                __typename
                ...VodAssetFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment EffectiveRetentionFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }

            fragment VodAssetFields on VodAsset {
              __typename
              id
              artifactHash
              playbackId
              streamId
              title
              description
              filename
              status
              sizeBytes
              durationMs
              resolution
              videoCodec
              audioCodec
              bitrateKbps
              createdAt
              updatedAt
              expiresAt
              errorMessage
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              thumbnailAssets {
                ...ThumbnailAssetsFields
              }
              effectiveRetention {
                ...EffectiveRetentionFields
              }
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query,
            operation_name="CompleteVodUpload",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CompleteVodUpload.model_validate(data)

    def abort_vod_upload(self, upload_id: str, **kwargs: Any) -> AbortVodUpload:
        """Abort an in-progress VOD upload."""
        query = gql("""
            mutation AbortVodUpload($uploadId: ID!) {
              abortVodUpload(uploadId: $uploadId) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"uploadId": upload_id}
        response = self.execute(
            query=query, operation_name="AbortVodUpload", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return AbortVodUpload.model_validate(data)

    def delete_vod_asset(self, id: str, **kwargs: Any) -> DeleteVodAsset:
        """Delete a VOD asset."""
        query = gql("""
            mutation DeleteVodAsset($id: ID!) {
              deleteVodAsset(id: $id) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="DeleteVodAsset", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteVodAsset.model_validate(data)

    def create_stream(self, input: CreateStreamInput, **kwargs: Any) -> CreateStream:
        """Create a new stream for live broadcasting."""
        query = gql("""
            mutation CreateStream($input: CreateStreamInput!) {
              createStream(input: $input) {
                __typename
                ...StreamFields
                ...ValidationErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment StreamFields on Stream {
              __typename
              id
              streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              createdAt
              updatedAt
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
              }
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="CreateStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateStream.model_validate(data)

    def update_stream(
        self, id: str, input: UpdateStreamInput, **kwargs: Any
    ) -> UpdateStream:
        """Update an existing stream's configuration."""
        query = gql("""
            mutation UpdateStream($id: ID!, $input: UpdateStreamInput!) {
              updateStream(id: $id, input: $input) {
                __typename
                ...StreamFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment StreamFields on Stream {
              __typename
              id
              streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              createdAt
              updatedAt
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
              }
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id, "input": input}
        response = self.execute(
            query=query, operation_name="UpdateStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return UpdateStream.model_validate(data)

    def delete_stream(self, id: str, **kwargs: Any) -> DeleteStream:
        """Delete a stream and all associated data."""
        query = gql("""
            mutation DeleteStream($id: ID!) {
              deleteStream(id: $id) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="DeleteStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteStream.model_validate(data)

    def refresh_stream_key(self, id: str, **kwargs: Any) -> RefreshStreamKey:
        """Generate a new stream key, invalidating the old one."""
        query = gql("""
            mutation RefreshStreamKey($id: ID!) {
              refreshStreamKey(id: $id) {
                __typename
                ...StreamFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment StreamFields on Stream {
              __typename
              id
              streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              createdAt
              updatedAt
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
              }
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="RefreshStreamKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RefreshStreamKey.model_validate(data)

    def create_stream_key(
        self, stream_id: str, input: CreateStreamKeyInput, **kwargs: Any
    ) -> CreateStreamKey:
        """Create an additional stream key for a stream."""
        query = gql("""
            mutation CreateStreamKey($streamId: ID!, $input: CreateStreamKeyInput!) {
              createStreamKey(streamId: $streamId, input: $input) {
                __typename
                ...StreamKeyFields
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment StreamKeyFields on StreamKey {
              __typename
              id
              streamId
              keyValue
              keyName
              isActive
              lastUsedAt
              createdAt
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id, "input": input}
        response = self.execute(
            query=query, operation_name="CreateStreamKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateStreamKey.model_validate(data)

    def delete_stream_key(
        self, stream_id: str, key_id: str, **kwargs: Any
    ) -> DeleteStreamKey:
        """Delete a stream key."""
        query = gql("""
            mutation DeleteStreamKey($streamId: ID!, $keyId: ID!) {
              deleteStreamKey(streamId: $streamId, keyId: $keyId) {
                __typename
                ...DeleteSuccessFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id, "keyId": key_id}
        response = self.execute(
            query=query, operation_name="DeleteStreamKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteStreamKey.model_validate(data)

    def create_push_target(
        self, stream_id: str, input: CreatePushTargetInput, **kwargs: Any
    ) -> CreatePushTarget:
        """Add a multistream push target to a stream.
        When the stream goes live, it will automatically push to all enabled targets."""
        query = gql("""
            mutation CreatePushTarget($streamId: ID!, $input: CreatePushTargetInput!) {
              createPushTarget(streamId: $streamId, input: $input) {
                ...PushTargetFields
              }
            }

            fragment PushTargetFields on PushTarget {
              id
              streamId
              platform
              name
              targetUri
              isEnabled
              status
              lastError
              reasonCode
              lastPushedAt
              createdAt
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id, "input": input}
        response = self.execute(
            query=query,
            operation_name="CreatePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreatePushTarget.model_validate(data)

    def update_push_target(
        self, id: str, input: UpdatePushTargetInput, **kwargs: Any
    ) -> UpdatePushTarget:
        """Update a multistream push target."""
        query = gql("""
            mutation UpdatePushTarget($id: ID!, $input: UpdatePushTargetInput!) {
              updatePushTarget(id: $id, input: $input) {
                ...PushTargetFields
              }
            }

            fragment PushTargetFields on PushTarget {
              id
              streamId
              platform
              name
              targetUri
              isEnabled
              status
              lastError
              reasonCode
              lastPushedAt
              createdAt
            }
            """)
        variables: dict[str, object] = {"id": id, "input": input}
        response = self.execute(
            query=query,
            operation_name="UpdatePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdatePushTarget.model_validate(data)

    def delete_push_target(self, id: str, **kwargs: Any) -> DeletePushTarget:
        """Delete a multistream push target."""
        query = gql("""
            mutation DeletePushTarget($id: ID!) {
              deletePushTarget(id: $id) {
                ...DeleteSuccessFields
              }
            }

            fragment DeleteSuccessFields on DeleteSuccess {
              __typename
              success
              deletedId
              pending
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query,
            operation_name="DeletePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return DeletePushTarget.model_validate(data)

    def get_tenant_usage(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetTenantUsage:
        """Get aggregated usage metrics for the tenant."""
        query = gql("""
            query GetTenantUsage($timeRange: TimeRangeInput) {
              tenantUsage(timeRange: $timeRange) {
                billingPeriod
                currency
                totalCost
                baseAmount
                usageAmount
                usage {
                  resourceType
                  amount
                }
                costs {
                  resourceType
                  cost
                }
                lineItems {
                  lineKey
                  meter
                  description
                  quantity
                  includedQuantity
                  billableQuantity
                  unitPrice
                  total
                  currency
                  clusterId
                  clusterName
                  pricingSource
                  pricingLabel
                  unit
                }
              }
            }
            """)
        variables: dict[str, object] = {"timeRange": time_range}
        response = self.execute(
            query=query, operation_name="GetTenantUsage", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetTenantUsage.model_validate(data)

    def list_usage_records(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListUsageRecords:
        """List detailed usage records with pagination."""
        query = gql("""
            query ListUsageRecords($page: ConnectionInput, $timeRange: TimeRangeInput) {
              usageRecordsConnection(page: $page, timeRange: $timeRange) {
                nodes {
                  id
                  clusterId
                  clusterName
                  usageType
                  unit
                  dimensions
                  usageValue
                  createdAt
                  periodStart
                  periodEnd
                  granularity
                }
                pageInfo {
                  ...PageInfoFields
                }
                totalCount
              }
            }

            fragment PageInfoFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page, "timeRange": time_range}
        response = self.execute(
            query=query,
            operation_name="ListUsageRecords",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ListUsageRecords.model_validate(data)

    def get_usage_aggregates(
        self,
        time_range: TimeRangeInput,
        granularity: Union[Optional[str], UnsetType] = UNSET,
        usage_types: Union[Optional[list[str]], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetUsageAggregates:
        """Get aggregated usage data grouped by time interval."""
        query = gql("""
            query GetUsageAggregates($timeRange: TimeRangeInput!, $granularity: String, $usageTypes: [String!]) {
              usageAggregates(
                timeRange: $timeRange
                granularity: $granularity
                usageTypes: $usageTypes
              ) {
                usageType
                periodStart
                periodEnd
                usageValue
                granularity
              }
            }
            """)
        variables: dict[str, object] = {
            "timeRange": time_range,
            "granularity": granularity,
            "usageTypes": usage_types,
        }
        response = self.execute(
            query=query,
            operation_name="GetUsageAggregates",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetUsageAggregates.model_validate(data)

    def list_developer_tokens(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> ListDeveloperTokens:
        """List API tokens for programmatic access.
        Used to authenticate requests to the Developer API."""
        query = gql("""
            query ListDeveloperTokens($page: ConnectionInput) {
              developerTokensConnection(page: $page) {
                nodes {
                  ...DeveloperTokenFields
                }
                pageInfo {
                  ...PageInfoFields
                }
                totalCount
              }
            }

            fragment DeveloperTokenFields on DeveloperToken {
              __typename
              id
              tokenName
              tokenValue
              permissions
              status
              lastUsedAt
              expiresAt
              createdAt
            }

            fragment PageInfoFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }
            """)
        variables: dict[str, object] = {"page": page}
        response = self.execute(
            query=query,
            operation_name="ListDeveloperTokens",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ListDeveloperTokens.model_validate(data)

    def list_signing_keys(
        self,
        status: Union[Optional[str], UnsetType] = UNSET,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListSigningKeys:
        """List the tenant's playback signing keys with optional status filter."""
        query = gql("""
            query ListSigningKeys($status: String, $page: ConnectionInput) {
              signingKeysConnection(status: $status, page: $page) {
                nodes {
                  ...SigningKeyFields
                }
                pageInfo {
                  ...PageInfoFields
                }
                totalCount
              }
            }

            fragment PageInfoFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment SigningKeyFields on SigningKey {
              __typename
              id
              kid
              name
              algorithm
              publicKeyPem
              status
              createdAt
              lastUsedAt
              revokedAt
            }
            """)
        variables: dict[str, object] = {"status": status, "page": page}
        response = self.execute(
            query=query, operation_name="ListSigningKeys", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListSigningKeys.model_validate(data)

    def get_signing_key(self, id: str, **kwargs: Any) -> GetSigningKey:
        """Get a single playback signing key by ID. Tenant-scoped."""
        query = gql("""
            query GetSigningKey($id: ID!) {
              signingKey(id: $id) {
                ...SigningKeyFields
              }
            }

            fragment SigningKeyFields on SigningKey {
              __typename
              id
              kid
              name
              algorithm
              publicKeyPem
              status
              createdAt
              lastUsedAt
              revokedAt
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetSigningKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetSigningKey.model_validate(data)

    def resolve_viewer_endpoint(
        self,
        content_id: str,
        protocol: Union[Optional[MediaViewerProtocol], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ResolveViewerEndpoint:
        """Resolve a playback ID to viewer endpoints (HLS, DASH, etc.).
        Used by players to get the optimal CDN endpoint for playback."""
        query = gql("""
            query ResolveViewerEndpoint($contentId: String!, $protocol: MediaViewerProtocol) {
              resolveViewerEndpoint(contentId: $contentId, protocol: $protocol) {
                primary {
                  ...ViewerEndpointFields
                }
                fallbacks {
                  ...ViewerEndpointFields
                }
                metadata {
                  contentType
                  contentId
                  title
                  description
                  durationSeconds
                  status
                  isLive
                  viewers
                  recordingSizeBytes
                  clipSource
                  createdAt
                  telemetryToken
                  thumbnailAssets {
                    ...ThumbnailAssetsFields
                  }
                }
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }

            fragment ViewerEndpointFields on ViewerEndpoint {
              nodeId
              baseUrl
              protocol
              url
              geoDistance
              loadScore
              outputs
            }
            """)
        variables: dict[str, object] = {"contentId": content_id, "protocol": protocol}
        response = self.execute(
            query=query,
            operation_name="ResolveViewerEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ResolveViewerEndpoint.model_validate(data)

    def resolve_ingest_endpoint(
        self,
        stream_key: str,
        protocol: Union[Optional[MediaIngestProtocol], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ResolveIngestEndpoint:
        """Resolve a stream key to ingest endpoints for StreamCrafter.
        Returns node-specific advertised protocols. A requested protocol filters candidates
        before ranking; it is not permission to substitute a different protocol."""
        query = gql("""
            query ResolveIngestEndpoint($streamKey: String!, $protocol: MediaIngestProtocol) {
              resolveIngestEndpoint(streamKey: $streamKey, protocol: $protocol) {
                primary {
                  ...IngestEndpointFields
                }
                fallbacks {
                  ...IngestEndpointFields
                }
                metadata {
                  streamId
                  streamKey
                  tenantId
                  recordingEnabled
                }
              }
            }

            fragment IngestEndpointFields on IngestEndpoint {
              nodeId
              baseUrl
              whipUrl
              rtmpUrl
              srtUrl
              region
              loadScore
              kind
              clusterId
            }
            """)
        variables: dict[str, object] = {"streamKey": stream_key, "protocol": protocol}
        response = self.execute(
            query=query,
            operation_name="ResolveIngestEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ResolveIngestEndpoint.model_validate(data)

    def get_clip(self, id: str, **kwargs: Any) -> GetClip:
        """Fetch a single clip by its global ID."""
        query = gql("""
            query GetClip($id: ID!) {
              clip(id: $id) {
                ...ClipFields
              }
            }

            fragment ClipFields on Clip {
              __typename
              id
              clipHash
              playbackId
              streamId
              title
              description
              startTime
              duration
              sizeBytes
              status
              clipMode
              createdAt
              updatedAt
              expiresAt
              isExpired
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              thumbnailAssets {
                ...ThumbnailAssetsFields
              }
              effectiveRetention {
                ...EffectiveRetentionFields
              }
            }

            fragment EffectiveRetentionFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetClip.model_validate(data)

    def get_dvr_chapter(
        self,
        dvr_id: str,
        start_ms: float,
        end_ms: float,
        mode: Union[Optional[DVRChapterMode], UnsetType] = UNSET,
        interval_seconds: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetDVRChapter:
        """Retrieve a single DVR chapter, including its finalized playbackId.

        Chapters are produced by the finalization queue as canonical .mkv
        VOD artifacts. Historical chapter mode is configured at the Stream level
        (Stream.dvrChapterMode) and snapshotted at StartDVR. Modes:
          - WINDOW_SIZED: sequential fixed-length chapters of size
            tier.MaxWindowSeconds since the recording's start.
          - FIXED_INTERVAL: UTC-only buckets of intervalSeconds, anchored at
            unix epoch 0."""
        query = gql("""
            query GetDVRChapter($dvrId: ID!, $startMs: Float!, $endMs: Float!, $mode: DVRChapterMode, $intervalSeconds: Int) {
              dvrChapter(
                dvrId: $dvrId
                startMs: $startMs
                endMs: $endMs
                mode: $mode
                intervalSeconds: $intervalSeconds
              ) {
                chapterId
                state
                playbackId
                isCurrent
                hasGaps
                segmentCount
                wallClockStartUnixMs
                wallClockEndUnixMs
                playableNow
                lastFailureReason
              }
            }
            """)
        variables: dict[str, object] = {
            "dvrId": dvr_id,
            "startMs": start_ms,
            "endMs": end_ms,
            "mode": mode,
            "intervalSeconds": interval_seconds,
        }
        response = self.execute(
            query=query, operation_name="GetDVRChapter", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetDVRChapter.model_validate(data)

    def list_dvr_chapters(
        self,
        dvr_id: str,
        mode: Union[Optional[DVRChapterMode], UnsetType] = UNSET,
        interval_seconds: Union[Optional[int], UnsetType] = UNSET,
        range_start_ms: Union[Optional[float], UnsetType] = UNSET,
        range_end_ms: Union[Optional[float], UnsetType] = UNSET,
        page_size: Union[Optional[int], UnsetType] = UNSET,
        page_token: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListDVRChapters:
        """List chapters for a DVR recording. Paginated for unbounded artifact
        lifetime — default 200 per page, max 1000."""
        query = gql("""
            query ListDVRChapters($dvrId: ID!, $mode: DVRChapterMode, $intervalSeconds: Int, $rangeStartMs: Float, $rangeEndMs: Float, $pageSize: Int, $pageToken: String) {
              dvrChapters(
                dvrId: $dvrId
                mode: $mode
                intervalSeconds: $intervalSeconds
                rangeStartMs: $rangeStartMs
                rangeEndMs: $rangeEndMs
                pageSize: $pageSize
                pageToken: $pageToken
              ) {
                chapters {
                  ...DVRChapterRefFields
                }
                nextPageToken
              }
            }

            fragment DVRChapterRefFields on DVRChapterRef {
              chapterId
              mode
              intervalSeconds
              startMs
              endMs
              isCurrent
              state
              playbackId
              hasGaps
              segmentCount
              lastFailureReason
            }
            """)
        variables: dict[str, object] = {
            "dvrId": dvr_id,
            "mode": mode,
            "intervalSeconds": interval_seconds,
            "rangeStartMs": range_start_ms,
            "rangeEndMs": range_end_ms,
            "pageSize": page_size,
            "pageToken": page_token,
        }
        response = self.execute(
            query=query, operation_name="ListDVRChapters", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListDVRChapters.model_validate(data)

    def get_vod_asset(self, id: str, **kwargs: Any) -> GetVodAsset:
        """Fetch a single VOD asset by ID."""
        query = gql("""
            query GetVodAsset($id: ID!) {
              vodAsset(id: $id) {
                ...VodAssetFields
              }
            }

            fragment EffectiveRetentionFields on EffectiveRetention {
              retentionDays
              retentionUntil
              source
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }

            fragment VodAssetFields on VodAsset {
              __typename
              id
              artifactHash
              playbackId
              streamId
              title
              description
              filename
              status
              sizeBytes
              durationMs
              resolution
              videoCodec
              audioCodec
              bitrateKbps
              createdAt
              updatedAt
              expiresAt
              errorMessage
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              thumbnailAssets {
                ...ThumbnailAssetsFields
              }
              effectiveRetention {
                ...EffectiveRetentionFields
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetVodAsset", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetVodAsset.model_validate(data)

    def get_vod_upload_status(
        self, upload_id: str, **kwargs: Any
    ) -> GetVodUploadStatus:
        """Read server-authoritative state of an in-flight VOD upload session.
        Polling complement to the upload events of tenantEvents; intended for reload-recovery
        and agent workflows that need a request/response shape."""
        query = gql("""
            query GetVodUploadStatus($uploadId: ID!) {
              vodUploadStatus(uploadId: $uploadId) {
                __typename
                ... on VodUploadStatus {
                  uploadId
                  state
                  expiresAt
                  retentionUntil
                  uploadedParts {
                    partNumber
                    etag
                    sizeBytes
                  }
                  missingParts
                  lastErrorCode
                  artifactHash
                  playbackId
                }
                ...ValidationErrorFields
                ...NotFoundErrorFields
                ...AuthErrorFields
              }
            }

            fragment AuthErrorFields on AuthError {
              __typename
              message
              code
            }

            fragment NotFoundErrorFields on NotFoundError {
              __typename
              message
              code
              resourceType
              resourceId
            }

            fragment ValidationErrorFields on ValidationError {
              __typename
              message
              code
              field
              constraint
            }
            """)
        variables: dict[str, object] = {"uploadId": upload_id}
        response = self.execute(
            query=query,
            operation_name="GetVodUploadStatus",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetVodUploadStatus.model_validate(data)

    def list_artifacts(
        self,
        input: Union[Optional[StorageArtifactsInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListArtifacts:
        """Unified storage artifact browser for the account Storage page.
        Search, kind filters, stream scoping, sorting, and pagination are
        resolved server-side against the tenant artifact registry."""
        query = gql("""
            query ListArtifacts($input: StorageArtifactsInput) {
              storageArtifactsConnection(input: $input) {
                nodes {
                  ...StorageArtifactFields
                }
                totalCount
                hasNextPage
                limit
                offset
              }
            }

            fragment StorageArtifactFields on StorageArtifact {
              key
              kind
              id
              hash
              playbackId
              streamId
              streamTitle
              title
              description
              errorMessage
              sizeBytes
              status
              createdAt
              updatedAt
              expiresAt
              deleteId
              durationSeconds
              thumbnailAssets {
                ...ThumbnailAssetsFields
              }
            }

            fragment ThumbnailAssetsFields on ThumbnailAssets {
              posterUrl
              spriteVttUrl
              spriteJpgUrl
              assetKey
            }
            """)
        variables: dict[str, object] = {"input": input}
        response = self.execute(
            query=query, operation_name="ListArtifacts", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListArtifacts.model_validate(data)

    def server_info(self, **kwargs: Any) -> ServerInfo:
        """Platform release and shipped product features. Readable without
        authentication so clients can detect what this server supports before
        signing in."""
        query = gql("""
            query ServerInfo {
              serverInfo {
                version
                features
              }
            }
            """)
        variables: dict[str, object] = {}
        response = self.execute(
            query=query, operation_name="ServerInfo", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ServerInfo.model_validate(data)

    def list_streams(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        search: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListStreams:
        """List all streams for the current tenant with pagination."""
        query = gql("""
            query ListStreams($page: ConnectionInput, $search: String) {
              streamsConnection(page: $page, search: $search) {
                nodes {
                  ...StreamFields
                }
                pageInfo {
                  ...PageInfoFields
                }
                totalCount
              }
            }

            fragment PageInfoFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment StreamFields on Stream {
              __typename
              id
              streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              createdAt
              updatedAt
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
              }
            }
            """)
        variables: dict[str, object] = {"page": page, "search": search}
        response = self.execute(
            query=query, operation_name="ListStreams", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListStreams.model_validate(data)

    def get_stream(self, id: str, **kwargs: Any) -> GetStream:
        """Fetch a single stream by its global ID."""
        query = gql("""
            query GetStream($id: ID!) {
              stream(id: $id) {
                ...StreamFields
              }
            }

            fragment PlaybackPolicyFields on PlaybackPolicy {
              type
              jwt {
                allowedKids
                requiredAudience
                requiredClaimsJson {
                  name
                  jsonValue
                }
              }
              webhook {
                url
                timeoutMs
                secretMasked
              }
            }

            fragment StreamFields on Stream {
              __typename
              id
              streamId
              name
              description
              streamKey
              playbackId
              record
              ingestMode
              pullSource {
                sourceUriRedacted
                enabled
                class
              }
              createdAt
              updatedAt
              dvrChapterMode
              dvrChapterIntervalSeconds
              monitoring
              playbackPolicy {
                ...PlaybackPolicyFields
              }
              metrics {
                status
                isLive
                currentViewers
                startedAt
                updatedAt
              }
            }
            """)
        variables: dict[str, object] = {"id": id}
        response = self.execute(
            query=query, operation_name="GetStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetStream.model_validate(data)

    def list_stream_keys(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListStreamKeys:
        """List all stream keys for a specific stream."""
        query = gql("""
            query ListStreamKeys($streamId: ID!, $page: ConnectionInput) {
              streamKeysConnection(streamId: $streamId, page: $page) {
                nodes {
                  ...StreamKeyFields
                }
                pageInfo {
                  ...PageInfoFields
                }
                totalCount
              }
            }

            fragment PageInfoFields on PageInfo {
              startCursor
              endCursor
              hasNextPage
              hasPreviousPage
            }

            fragment StreamKeyFields on StreamKey {
              __typename
              id
              streamId
              keyValue
              keyName
              isActive
              lastUsedAt
              createdAt
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id, "page": page}
        response = self.execute(
            query=query, operation_name="ListStreamKeys", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListStreamKeys.model_validate(data)

    def list_push_targets(self, stream_id: str, **kwargs: Any) -> ListPushTargets:
        """Configured multistream push targets for this stream."""
        query = gql("""
            query ListPushTargets($streamId: ID!) {
              stream(id: $streamId) {
                id
                pushTargets {
                  ...PushTargetFields
                }
              }
            }

            fragment PushTargetFields on PushTarget {
              id
              streamId
              platform
              name
              targetUri
              isEnabled
              status
              lastError
              reasonCode
              lastPushedAt
              createdAt
            }
            """)
        variables: dict[str, object] = {"streamId": stream_id}
        response = self.execute(
            query=query, operation_name="ListPushTargets", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListPushTargets.model_validate(data)
