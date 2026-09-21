from collections.abc import AsyncIterator
from typing import Any, Optional, Union

from .abort_vod_upload import AbortVodUpload
from .async_base_client import AsyncBaseClient
from .base_model import UNSET, UnsetType
from .complete_vod_upload import CompleteVodUpload
from .create_clip import CreateClip
from .create_developer_token import CreateDeveloperToken
from .create_push_target import CreatePushTarget
from .create_signing_key import CreateSigningKey
from .create_stream import CreateStream
from .create_stream_key import CreateStreamKey
from .create_vod_upload import CreateVodUpload
from .delete_clip import DeleteClip
from .delete_dvr import DeleteDVR
from .delete_push_target import DeletePushTarget
from .delete_stream import DeleteStream
from .delete_stream_key import DeleteStreamKey
from .delete_vod_asset import DeleteVodAsset
from .enums import DVRChapterMode, MediaIngestProtocol, MediaViewerProtocol
from .get_clip import GetClip
from .get_dvr_chapter import GetDVRChapter
from .get_signing_key import GetSigningKey
from .get_stream import GetStream
from .get_tenant_usage import GetTenantUsage
from .get_usage_aggregates import GetUsageAggregates
from .get_vod_asset import GetVodAsset
from .get_vod_upload_status import GetVodUploadStatus
from .input_types import (
    CompleteVodUploadInput,
    ConnectionInput,
    CreateClipInput,
    CreateDeveloperTokenInput,
    CreatePushTargetInput,
    CreateSigningKeyInput,
    CreateStreamInput,
    CreateStreamKeyInput,
    CreateVodUploadInput,
    SetPlaybackPolicyInput,
    StorageArtifactsInput,
    TestPlaybackAccessInput,
    TimeRangeInput,
    UpdatePushTargetInput,
    UpdateStreamInput,
)
from .list_artifacts import ListArtifacts
from .list_developer_tokens import ListDeveloperTokens
from .list_dvr_chapters import ListDVRChapters
from .list_push_targets import ListPushTargets
from .list_signing_keys import ListSigningKeys
from .list_stream_keys import ListStreamKeys
from .list_streams import ListStreams
from .list_usage_records import ListUsageRecords
from .refresh_stream_key import RefreshStreamKey
from .resolve_ingest_endpoint import ResolveIngestEndpoint
from .resolve_viewer_endpoint import ResolveViewerEndpoint
from .revoke_developer_token import RevokeDeveloperToken
from .revoke_signing_key import RevokeSigningKey
from .server_info import ServerInfo
from .set_playback_policy import SetPlaybackPolicy
from .start_dvr import StartDVR
from .stop_dvr import StopDVR
from .tenant_events import TenantEvents
from .test_playback_access import TestPlaybackAccess
from .update_push_target import UpdatePushTarget
from .update_stream import UpdateStream


def gql(q: str) -> str:
    return q


class AsyncGraphQLClient(AsyncBaseClient):
    async def create_developer_token(
        self, input: CreateDeveloperTokenInput, **kwargs: Any
    ) -> CreateDeveloperToken:
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
        response = await self.execute(
            query=query,
            operation_name="CreateDeveloperToken",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateDeveloperToken.model_validate(data)

    async def revoke_developer_token(
        self, id: str, **kwargs: Any
    ) -> RevokeDeveloperToken:
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
        response = await self.execute(
            query=query,
            operation_name="RevokeDeveloperToken",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RevokeDeveloperToken.model_validate(data)

    async def create_signing_key(
        self, input: CreateSigningKeyInput, **kwargs: Any
    ) -> CreateSigningKey:
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
        response = await self.execute(
            query=query,
            operation_name="CreateSigningKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreateSigningKey.model_validate(data)

    async def revoke_signing_key(self, id: str, **kwargs: Any) -> RevokeSigningKey:
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
        response = await self.execute(
            query=query,
            operation_name="RevokeSigningKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RevokeSigningKey.model_validate(data)

    async def set_playback_policy(
        self, input: SetPlaybackPolicyInput, **kwargs: Any
    ) -> SetPlaybackPolicy:
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
        response = await self.execute(
            query=query,
            operation_name="SetPlaybackPolicy",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return SetPlaybackPolicy.model_validate(data)

    async def test_playback_access(
        self, input: TestPlaybackAccessInput, **kwargs: Any
    ) -> TestPlaybackAccess:
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
        response = await self.execute(
            query=query,
            operation_name="TestPlaybackAccess",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return TestPlaybackAccess.model_validate(data)

    async def create_clip(self, input: CreateClipInput, **kwargs: Any) -> CreateClip:
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
        response = await self.execute(
            query=query, operation_name="CreateClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateClip.model_validate(data)

    async def delete_clip(self, id: str, **kwargs: Any) -> DeleteClip:
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
        response = await self.execute(
            query=query, operation_name="DeleteClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteClip.model_validate(data)

    async def start_dvr(self, stream_id: str, **kwargs: Any) -> StartDVR:
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
        response = await self.execute(
            query=query, operation_name="StartDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return StartDVR.model_validate(data)

    async def stop_dvr(self, dvr_hash: str, **kwargs: Any) -> StopDVR:
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
        response = await self.execute(
            query=query, operation_name="StopDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return StopDVR.model_validate(data)

    async def delete_dvr(self, dvr_hash: str, **kwargs: Any) -> DeleteDVR:
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
        response = await self.execute(
            query=query, operation_name="DeleteDVR", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteDVR.model_validate(data)

    async def create_vod_upload(
        self, input: CreateVodUploadInput, **kwargs: Any
    ) -> CreateVodUpload:
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
        response = await self.execute(
            query=query, operation_name="CreateVodUpload", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateVodUpload.model_validate(data)

    async def complete_vod_upload(
        self, input: CompleteVodUploadInput, **kwargs: Any
    ) -> CompleteVodUpload:
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
        response = await self.execute(
            query=query,
            operation_name="CompleteVodUpload",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CompleteVodUpload.model_validate(data)

    async def abort_vod_upload(self, upload_id: str, **kwargs: Any) -> AbortVodUpload:
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
        response = await self.execute(
            query=query, operation_name="AbortVodUpload", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return AbortVodUpload.model_validate(data)

    async def delete_vod_asset(self, id: str, **kwargs: Any) -> DeleteVodAsset:
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
        response = await self.execute(
            query=query, operation_name="DeleteVodAsset", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteVodAsset.model_validate(data)

    async def create_stream(
        self, input: CreateStreamInput, **kwargs: Any
    ) -> CreateStream:
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
        response = await self.execute(
            query=query, operation_name="CreateStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateStream.model_validate(data)

    async def update_stream(
        self, id: str, input: UpdateStreamInput, **kwargs: Any
    ) -> UpdateStream:
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
        response = await self.execute(
            query=query, operation_name="UpdateStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return UpdateStream.model_validate(data)

    async def delete_stream(self, id: str, **kwargs: Any) -> DeleteStream:
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
        response = await self.execute(
            query=query, operation_name="DeleteStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteStream.model_validate(data)

    async def refresh_stream_key(self, id: str, **kwargs: Any) -> RefreshStreamKey:
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
        response = await self.execute(
            query=query,
            operation_name="RefreshStreamKey",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return RefreshStreamKey.model_validate(data)

    async def create_stream_key(
        self, stream_id: str, input: CreateStreamKeyInput, **kwargs: Any
    ) -> CreateStreamKey:
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
        response = await self.execute(
            query=query, operation_name="CreateStreamKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return CreateStreamKey.model_validate(data)

    async def delete_stream_key(
        self, stream_id: str, key_id: str, **kwargs: Any
    ) -> DeleteStreamKey:
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
        response = await self.execute(
            query=query, operation_name="DeleteStreamKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return DeleteStreamKey.model_validate(data)

    async def create_push_target(
        self, stream_id: str, input: CreatePushTargetInput, **kwargs: Any
    ) -> CreatePushTarget:
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
        response = await self.execute(
            query=query,
            operation_name="CreatePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return CreatePushTarget.model_validate(data)

    async def update_push_target(
        self, id: str, input: UpdatePushTargetInput, **kwargs: Any
    ) -> UpdatePushTarget:
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
        response = await self.execute(
            query=query,
            operation_name="UpdatePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return UpdatePushTarget.model_validate(data)

    async def delete_push_target(self, id: str, **kwargs: Any) -> DeletePushTarget:
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
        response = await self.execute(
            query=query,
            operation_name="DeletePushTarget",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return DeletePushTarget.model_validate(data)

    async def get_tenant_usage(
        self,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetTenantUsage:
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
        response = await self.execute(
            query=query, operation_name="GetTenantUsage", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetTenantUsage.model_validate(data)

    async def list_usage_records(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        time_range: Union[Optional[TimeRangeInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListUsageRecords:
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
        response = await self.execute(
            query=query,
            operation_name="ListUsageRecords",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ListUsageRecords.model_validate(data)

    async def get_usage_aggregates(
        self,
        time_range: TimeRangeInput,
        granularity: Union[Optional[str], UnsetType] = UNSET,
        usage_types: Union[Optional[list[str]], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetUsageAggregates:
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
        response = await self.execute(
            query=query,
            operation_name="GetUsageAggregates",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetUsageAggregates.model_validate(data)

    async def list_developer_tokens(
        self, page: Union[Optional[ConnectionInput], UnsetType] = UNSET, **kwargs: Any
    ) -> ListDeveloperTokens:
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
        response = await self.execute(
            query=query,
            operation_name="ListDeveloperTokens",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ListDeveloperTokens.model_validate(data)

    async def list_signing_keys(
        self,
        status: Union[Optional[str], UnsetType] = UNSET,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListSigningKeys:
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
        response = await self.execute(
            query=query, operation_name="ListSigningKeys", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListSigningKeys.model_validate(data)

    async def get_signing_key(self, id: str, **kwargs: Any) -> GetSigningKey:
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
        response = await self.execute(
            query=query, operation_name="GetSigningKey", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetSigningKey.model_validate(data)

    async def resolve_viewer_endpoint(
        self,
        content_id: str,
        protocol: Union[Optional[MediaViewerProtocol], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ResolveViewerEndpoint:
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
        response = await self.execute(
            query=query,
            operation_name="ResolveViewerEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ResolveViewerEndpoint.model_validate(data)

    async def resolve_ingest_endpoint(
        self,
        stream_key: str,
        protocol: Union[Optional[MediaIngestProtocol], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ResolveIngestEndpoint:
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
        response = await self.execute(
            query=query,
            operation_name="ResolveIngestEndpoint",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return ResolveIngestEndpoint.model_validate(data)

    async def get_clip(self, id: str, **kwargs: Any) -> GetClip:
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
        response = await self.execute(
            query=query, operation_name="GetClip", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetClip.model_validate(data)

    async def get_dvr_chapter(
        self,
        dvr_id: str,
        start_ms: float,
        end_ms: float,
        mode: Union[Optional[DVRChapterMode], UnsetType] = UNSET,
        interval_seconds: Union[Optional[int], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> GetDVRChapter:
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
        response = await self.execute(
            query=query, operation_name="GetDVRChapter", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetDVRChapter.model_validate(data)

    async def list_dvr_chapters(
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
        response = await self.execute(
            query=query, operation_name="ListDVRChapters", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListDVRChapters.model_validate(data)

    async def get_vod_asset(self, id: str, **kwargs: Any) -> GetVodAsset:
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
        response = await self.execute(
            query=query, operation_name="GetVodAsset", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetVodAsset.model_validate(data)

    async def get_vod_upload_status(
        self, upload_id: str, **kwargs: Any
    ) -> GetVodUploadStatus:
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
        response = await self.execute(
            query=query,
            operation_name="GetVodUploadStatus",
            variables=variables,
            **kwargs,
        )
        data = self.get_data(response)
        return GetVodUploadStatus.model_validate(data)

    async def list_artifacts(
        self,
        input: Union[Optional[StorageArtifactsInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListArtifacts:
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
        response = await self.execute(
            query=query, operation_name="ListArtifacts", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListArtifacts.model_validate(data)

    async def server_info(self, **kwargs: Any) -> ServerInfo:
        query = gql("""
            query ServerInfo {
              serverInfo {
                version
                features
              }
            }
            """)
        variables: dict[str, object] = {}
        response = await self.execute(
            query=query, operation_name="ServerInfo", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ServerInfo.model_validate(data)

    async def list_streams(
        self,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        search: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListStreams:
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
        response = await self.execute(
            query=query, operation_name="ListStreams", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListStreams.model_validate(data)

    async def get_stream(self, id: str, **kwargs: Any) -> GetStream:
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
        response = await self.execute(
            query=query, operation_name="GetStream", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return GetStream.model_validate(data)

    async def list_stream_keys(
        self,
        stream_id: str,
        page: Union[Optional[ConnectionInput], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> ListStreamKeys:
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
        response = await self.execute(
            query=query, operation_name="ListStreamKeys", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListStreamKeys.model_validate(data)

    async def list_push_targets(self, stream_id: str, **kwargs: Any) -> ListPushTargets:
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
        response = await self.execute(
            query=query, operation_name="ListPushTargets", variables=variables, **kwargs
        )
        data = self.get_data(response)
        return ListPushTargets.model_validate(data)

    async def tenant_events(
        self,
        types: Union[Optional[list[str]], UnsetType] = UNSET,
        stream_id: Union[Optional[str], UnsetType] = UNSET,
        **kwargs: Any,
    ) -> AsyncIterator[TenantEvents]:
        query = gql("""
            subscription TenantEvents($types: [String!], $streamId: ID) {
              tenantEvents(types: $types, streamId: $streamId) {
                id
                type
                time
                subject
                data {
                  __typename
                  ... on AccountSuspended {
                    suspensionReason: reason
                  }
                  ... on ApiTokenCreated {
                    tokenId
                    name
                    permissions
                    expiresAt
                  }
                  ... on ApiTokenRevoked {
                    tokenId
                  }
                  ... on BillingDetailsUpdated {
                    changedFields
                  }
                  ... on InvoiceCreated {
                    invoiceId
                    amountDue {
                      ...EventMoneyFields
                    }
                    periodStart
                    periodEnd
                    dueAt
                  }
                  ... on InvoicePaid {
                    invoiceId
                    amountPaid {
                      ...EventMoneyFields
                    }
                  }
                  ... on PaymentFailed {
                    paymentId
                    invoiceId
                    amount {
                      ...EventMoneyFields
                    }
                    paymentFailureReason: reason
                    provider
                    providerReferenceId
                  }
                  ... on TopupCredited {
                    topupId
                    amount {
                      ...EventMoneyFields
                    }
                  }
                  ... on ClipRequested {
                    artifact {
                      ...EventArtifactFields
                    }
                    durationMs
                  }
                  ... on ClipReady {
                    artifact {
                      ...EventArtifactFields
                    }
                    durationMs
                    sizeBytes
                  }
                  ... on ClipFailed {
                    artifact {
                      ...EventArtifactFields
                    }
                    mediaFailureReason: reason
                  }
                  ... on CustomDomainVerified {
                    domain
                  }
                  ... on CustomDomainFailed {
                    domain
                    customDomainFailureReason: reason
                  }
                  ... on MultistreamStatusChanged {
                    streamId
                    targetId
                    targetName
                    status
                    previousStatus
                  }
                  ... on RecordingReady {
                    artifact {
                      ...EventArtifactFields
                    }
                    durationMs
                    sizeBytes
                  }
                  ... on RecordingFailed {
                    artifact {
                      ...EventArtifactFields
                    }
                    mediaFailureReason: reason
                  }
                  ... on StreamConnected {
                    streamId
                    protocol
                  }
                  ... on StreamCreated {
                    streamId
                    name
                    playbackId
                  }
                  ... on StreamDeleted {
                    streamId
                  }
                  ... on StreamIdle {
                    streamId
                  }
                  ... on StreamKeyRotated {
                    streamId
                  }
                  ... on StreamLive {
                    streamId
                  }
                  ... on StreamUpdated {
                    streamId
                    changedFields
                  }
                  ... on UploadCreated {
                    artifact {
                      ...EventArtifactFields
                    }
                    filename
                    expectedSizeBytes
                  }
                  ... on UploadCompleted {
                    artifact {
                      ...EventArtifactFields
                    }
                    sizeBytes
                  }
                  ... on UploadAborted {
                    artifact {
                      ...EventArtifactFields
                    }
                  }
                  ... on UploadReady {
                    artifact {
                      ...EventArtifactFields
                    }
                    durationMs
                    sizeBytes
                  }
                  ... on UploadFailed {
                    artifact {
                      ...EventArtifactFields
                    }
                    mediaFailureReason: reason
                  }
                }
              }
            }

            fragment EventArtifactFields on EventArtifact {
              artifactId
              kind
              streamId
              playbackId
            }

            fragment EventMoneyFields on EventMoney {
              amountMinor
              currency
            }
            """)
        variables: dict[str, object] = {"types": types, "streamId": stream_id}
        async for data in self.execute_ws(
            query=query, operation_name="TenantEvents", variables=variables, **kwargs
        ):
            yield TenantEvents.model_validate(data)
