import { client } from "../client.js";
import type { ApolloError } from "@apollo/client";
import {
  GetStreamsDocument,
  GetStreamDocument,
  ValidateStreamKeyDocument,
  CreateStreamDocument,
  UpdateStreamDocument,
  DeleteStreamDocument,
  RefreshStreamKeyDocument,
  GetStreamKeysDocument,
  CreateStreamKeyDocument,
  DeleteStreamKeyDocument,
  GetRecordingsDocument,
  GetStreamRecordingsDocument,
  StreamEventsDocument,
  ViewerMetricsStreamDocument,
  TrackListUpdatesDocument,
} from "../generated/apollo-helpers";
import type {
  GetStreamsQuery,
  GetStreamQuery,
  GetStreamQueryVariables,
  ValidateStreamKeyQuery,
  ValidateStreamKeyQueryVariables,
  CreateStreamMutation,
  CreateStreamMutationVariables,
  UpdateStreamMutation,
  UpdateStreamMutationVariables,
  DeleteStreamMutation,
  DeleteStreamMutationVariables,
  RefreshStreamKeyMutation,
  RefreshStreamKeyMutationVariables,
  GetStreamKeysQuery,
  GetStreamKeysQueryVariables,
  CreateStreamKeyMutation,
  CreateStreamKeyMutationVariables,
  DeleteStreamKeyMutation,
  DeleteStreamKeyMutationVariables,
  GetRecordingsQuery,
  GetRecordingsQueryVariables,
  GetStreamRecordingsQuery,
  GetStreamRecordingsQueryVariables,
  StreamEventsSubscription,
  ViewerMetricsStreamSubscription,
  TrackListUpdatesSubscription,
} from "../generated/types";

interface StreamEventsCallbacks {
  onStreamEvent?: (event: StreamEventsSubscription["streamEvents"]) => void;
  onError?: (error: ApolloError) => void;
}

interface ViewerMetricsCallbacks {
  onViewerMetrics?: (metrics: ViewerMetricsStreamSubscription["viewerMetrics"]) => void;
  onError?: (error: ApolloError) => void;
}

interface TrackListCallbacks {
  onTrackListUpdate?: (trackList: TrackListUpdatesSubscription["trackListUpdates"]) => void;
  onError?: (error: ApolloError) => void;
}

export const streamsService = {
  // Queries
  async getStreams(): Promise<GetStreamsQuery["streams"]> {
    const result = await client.query({
      query: GetStreamsDocument,
      fetchPolicy: "network-only", // Always fetch fresh data to avoid cache issues
      errorPolicy: "all",
    });

    if (result.errors) {
      console.error("GraphQL errors in getStreams:", result.errors);
    }

    return result.data?.streams || [];
  },

  async getStream(id: GetStreamQueryVariables["id"]): Promise<GetStreamQuery["stream"] | null> {
    try {
      const result = await client.query({
        query: GetStreamDocument,
        variables: { id },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.stream || null;
    } catch (error) {
      console.error("Failed to fetch stream:", error);
      return null;
    }
  },

  async validateStreamKey(
    streamKey: ValidateStreamKeyQueryVariables["streamKey"],
  ): Promise<ValidateStreamKeyQuery["validateStreamKey"] | null> {
    try {
      const result = await client.query({
        query: ValidateStreamKeyDocument,
        variables: { streamKey },
        fetchPolicy: "no-cache", // Always fresh for validation
        errorPolicy: "all",
      });
      return result.data?.validateStreamKey || null;
    } catch (error) {
      console.error("Failed to validate stream key:", error);
      return null;
    }
  },

  // Mutations
  async createStream(
    input: CreateStreamMutationVariables["input"],
  ): Promise<CreateStreamMutation["createStream"]> {
    const result = await client.mutate({
      mutation: CreateStreamDocument,
      variables: { input },
      refetchQueries: [{ query: GetStreamsDocument }],
    });
    return result.data!.createStream;
  },

  async updateStream(
    id: UpdateStreamMutationVariables["id"],
    input: UpdateStreamMutationVariables["input"],
  ): Promise<UpdateStreamMutation["updateStream"]> {
    const result = await client.mutate({
      mutation: UpdateStreamDocument,
      variables: { id, input },
      refetchQueries: [
        { query: GetStreamsDocument },
        { query: GetStreamDocument, variables: { id } },
      ],
    });
    return result.data!.updateStream;
  },

  async deleteStream(
    id: DeleteStreamMutationVariables["id"],
  ): Promise<DeleteStreamMutation["deleteStream"]> {
    const result = await client.mutate({
      mutation: DeleteStreamDocument,
      variables: { id },
      refetchQueries: [{ query: GetStreamsDocument }],
    });
    return result.data!.deleteStream;
  },

  async refreshStreamKey(
    id: RefreshStreamKeyMutationVariables["id"],
  ): Promise<RefreshStreamKeyMutation["refreshStreamKey"]> {
    const result = await client.mutate({
      mutation: RefreshStreamKeyDocument,
      variables: { id },
      refetchQueries: [
        { query: GetStreamsDocument },
        { query: GetStreamDocument, variables: { id } },
      ],
    });
    return result.data!.refreshStreamKey;
  },

  // Stream Keys Management
  async getStreamKeys(
    streamId: GetStreamKeysQueryVariables["streamId"],
  ): Promise<GetStreamKeysQuery["streamKeys"]> {
    try {
      const result = await client.query({
        query: GetStreamKeysDocument,
        variables: { streamId },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.streamKeys || [];
    } catch (error) {
      console.error("Failed to fetch stream keys:", error);
      return [];
    }
  },

  async createStreamKey(
    streamId: CreateStreamKeyMutationVariables["streamId"],
    input: CreateStreamKeyMutationVariables["input"],
  ): Promise<CreateStreamKeyMutation["createStreamKey"]> {
    try {
      const result = await client.mutate({
        mutation: CreateStreamKeyDocument,
        variables: { streamId, input },
        refetchQueries: [
          { query: GetStreamKeysDocument, variables: { streamId } },
        ],
      });
      return result.data!.createStreamKey;
    } catch (error) {
      console.error("Failed to create stream key:", error);
      throw error;
    }
  },

  async deleteStreamKey(
    streamId: DeleteStreamKeyMutationVariables["streamId"],
    keyId: DeleteStreamKeyMutationVariables["keyId"],
  ): Promise<DeleteStreamKeyMutation["deleteStreamKey"]> {
    try {
      const result = await client.mutate({
        mutation: DeleteStreamKeyDocument,
        variables: { streamId, keyId },
        refetchQueries: [
          { query: GetStreamKeysDocument, variables: { streamId } },
        ],
      });
      return result.data!.deleteStreamKey;
    } catch (error) {
      console.error("Failed to delete stream key:", error);
      throw error;
    }
  },

  // Recordings Management
  async getRecordings(
    streamId: GetRecordingsQueryVariables["streamId"] = null,
  ): Promise<GetRecordingsQuery["recordings"]> {
    try {
      const result = await client.query({
        query: GetRecordingsDocument,
        variables: streamId ? { streamId } : {},
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.recordings || [];
    } catch (error) {
      console.error("Failed to fetch recordings:", error);
      return [];
    }
  },

  async getStreamRecordings(
    streamId: GetStreamRecordingsQueryVariables["streamId"],
  ): Promise<GetStreamRecordingsQuery["recordings"]> {
    try {
      const result = await client.query({
        query: GetStreamRecordingsDocument,
        variables: { streamId },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.recordings || [];
    } catch (error) {
      console.error("Failed to fetch stream recordings:", error);
      return [];
    }
  },

  // Subscriptions
  subscribeToStreamEvents(streamId: string, callbacks: StreamEventsCallbacks) {
    const observable = client.subscribe({
      query: StreamEventsDocument,
      variables: { stream: streamId },
    });

    return observable.subscribe({
      next: (result) => {
        if (result.data?.streamEvents) {
          callbacks.onStreamEvent?.(result.data.streamEvents);
        }
      },
      error: (error: ApolloError) => {
        callbacks.onError?.(error);
        console.error("Stream events subscription error:", error);
      },
    });
  },

  subscribeToViewerMetrics(streamId: string, callbacks: ViewerMetricsCallbacks) {
    const observable = client.subscribe({
      query: ViewerMetricsStreamDocument,
      variables: { stream: streamId },
    });

    return observable.subscribe({
      next: (result) => {
        if (result.data?.viewerMetrics) {
          callbacks.onViewerMetrics?.(result.data.viewerMetrics);
        }
      },
      error: (error: ApolloError) => {
        callbacks.onError?.(error);
        console.error("Viewer metrics subscription error:", error);
      },
    });
  },

  subscribeToTrackListUpdates(streamId: string, callbacks: TrackListCallbacks) {
    const observable = client.subscribe({
      query: TrackListUpdatesDocument,
      variables: { stream: streamId },
    });

    return observable.subscribe({
      next: (result) => {
        if (result.data?.trackListUpdates) {
          callbacks.onTrackListUpdate?.(result.data.trackListUpdates);
        }
      },
      error: (error: ApolloError) => {
        callbacks.onError?.(error);
        console.error("Track list subscription error:", error);
      },
    });
  },
};
