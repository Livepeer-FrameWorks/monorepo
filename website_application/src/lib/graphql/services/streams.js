import { client } from '../client.js';
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
  TrackListUpdatesDocument 
} from '../generated/apollo-helpers';

export const streamsService = {
  // Queries
  /**
   * @returns {Promise<Array>} List of streams
   */
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

  /**
   * @param {string} id - Stream ID
   * @returns {Promise<Object|null>} Stream object or null
   */
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

  // Stream Keys Management
  async getStreamKeys(streamId) {
    try {
      const result = await client.query({
        query: GetStreamKeysDocument,
        variables: { streamId },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.streamKeys || [];
    } catch (error) {
      console.error('Failed to fetch stream keys:', error);
      return [];
    }
  },

  async createStreamKey(streamId, input) {
    try {
      const result = await client.mutate({
        mutation: CreateStreamKeyDocument,
        variables: { streamId, input },
        refetchQueries: [
          { query: GetStreamKeysDocument, variables: { streamId } }
        ]
      });
      return result.data.createStreamKey;
    } catch (error) {
      console.error('Failed to create stream key:', error);
      throw error;
    }
  },

  async deleteStreamKey(streamId, keyId) {
    try {
      const result = await client.mutate({
        mutation: DeleteStreamKeyDocument,
        variables: { streamId, keyId },
        refetchQueries: [
          { query: GetStreamKeysDocument, variables: { streamId } }
        ]
      });
      return result.data.deleteStreamKey;
    } catch (error) {
      console.error('Failed to delete stream key:', error);
      throw error;
    }
  },

  // Recordings Management
  async getRecordings(streamId = null) {
    try {
      const result = await client.query({
        query: GetRecordingsDocument,
        variables: streamId ? { streamId } : {},
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.recordings || [];
    } catch (error) {
      console.error('Failed to fetch recordings:', error);
      return [];
    }
  },

  async getStreamRecordings(streamId) {
    try {
      const result = await client.query({
        query: GetStreamRecordingsDocument,
        variables: { streamId },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.recordings || [];
    } catch (error) {
      console.error('Failed to fetch stream recordings:', error);
      return [];
    }
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