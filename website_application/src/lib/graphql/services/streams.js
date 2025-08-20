import { client } from '../client.js';
import { 
  GetStreamsDocument, 
  GetStreamDocument, 
  ValidateStreamKeyDocument,
  CreateStreamDocument,
  UpdateStreamDocument, 
  DeleteStreamDocument,
  RefreshStreamKeyDocument,
  StreamEventsDocument,
  ViewerMetricsStreamDocument,
  TrackListUpdatesDocument 
} from '../generated/apollo-helpers';

export const streamsService = {
  // Queries
  async getStreams() {
    const result = await client.query({
      query: GetStreamsDocument,
      fetchPolicy: 'network-only', // Always fetch fresh data to avoid cache issues
      errorPolicy: 'all'
    });
    
    if (result.errors) {
      console.error('GraphQL errors in getStreams:', result.errors);
    }
    
    return result.data?.streams || [];
  },

  async getStream(id) {
    try {
      const result = await client.query({
        query: GetStreamDocument,
        variables: { id },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.stream || null;
    } catch (error) {
      console.error('Failed to fetch stream:', error);
      return null;
    }
  },

  async validateStreamKey(streamKey) {
    try {
      const result = await client.query({
        query: ValidateStreamKeyDocument,
        variables: { streamKey },
        fetchPolicy: 'no-cache', // Always fresh for validation
        errorPolicy: 'all'
      });
      return result.data?.validateStreamKey || null;
    } catch (error) {
      console.error('Failed to validate stream key:', error);
      return null;
    }
  },

  // Mutations
  async createStream(input) {
    const result = await client.mutate({
      mutation: CreateStreamDocument,
      variables: { input },
      refetchQueries: [{ query: GetStreamsDocument }]
    });
    return result.data.createStream;
  },

  async updateStream(id, input) {
    const result = await client.mutate({
      mutation: UpdateStreamDocument,
      variables: { id, input },
      refetchQueries: [
        { query: GetStreamsDocument },
        { query: GetStreamDocument, variables: { id } }
      ]
    });
    return result.data.updateStream;
  },

  async deleteStream(id) {
    const result = await client.mutate({
      mutation: DeleteStreamDocument,
      variables: { id },
      refetchQueries: [{ query: GetStreamsDocument }]
    });
    return result.data.deleteStream;
  },

  async refreshStreamKey(id) {
    const result = await client.mutate({
      mutation: RefreshStreamKeyDocument,
      variables: { id },
      refetchQueries: [
        { query: GetStreamsDocument },
        { query: GetStreamDocument, variables: { id } }
      ]
    });
    return result.data.refreshStreamKey;
  },

  // Subscriptions
  subscribeToStreamEvents(streamId, callbacks) {
    const observable = client.subscribe({
      query: StreamEventsDocument,
      variables: { stream: streamId }
    });

    return observable.subscribe({
      next: (result) => {
        if (callbacks.onStreamEvent) {
          callbacks.onStreamEvent(result.data.streamEvents);
        }
      },
      error: (error) => {
        if (callbacks.onError) {
          callbacks.onError(error);
        }
        console.error('Stream events subscription error:', error);
      }
    });
  },

  subscribeToViewerMetrics(streamId, callbacks) {
    const observable = client.subscribe({
      query: ViewerMetricsStreamDocument,
      variables: { stream: streamId }
    });

    return observable.subscribe({
      next: (result) => {
        if (callbacks.onViewerMetrics) {
          callbacks.onViewerMetrics(result.data.viewerMetrics);
        }
      },
      error: (error) => {
        if (callbacks.onError) {
          callbacks.onError(error);
        }
        console.error('Viewer metrics subscription error:', error);
      }
    });
  },

  subscribeToTrackListUpdates(streamId, callbacks) {
    const observable = client.subscribe({
      query: TrackListUpdatesDocument,
      variables: { stream: streamId }
    });

    return observable.subscribe({
      next: (result) => {
        if (callbacks.onTrackListUpdate) {
          callbacks.onTrackListUpdate(result.data.trackListUpdates);
        }
      },
      error: (error) => {
        if (callbacks.onError) {
          callbacks.onError(error);
        }
        console.error('Track list subscription error:', error);
      }
    });
  }
};