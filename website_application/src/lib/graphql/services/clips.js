import { client } from '../client.js';
import { 
  CreateClipDocument,
  DeleteClipDocument,
  GetClipsDocument,
  GetClipDocument,
  GetClipViewingUrlsDocument
} from '../generated/apollo-helpers';

export const clipsService = {
  // Queries
  async getClips(streamId = null) {
    try {
      const result = await client.query({
        query: GetClipsDocument,
        variables: { streamId },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.clips || [];
    } catch (error) {
      console.error('Failed to get clips:', error);
      throw error;
    }
  },

  async getClip(id) {
    try {
      const result = await client.query({
        query: GetClipDocument,
        variables: { id },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.clip || null;
    } catch (error) {
      console.error('Failed to get clip:', error);
      throw error;
    }
  },

  async getClipViewingUrls(clipId) {
    try {
      const result = await client.query({
        query: GetClipViewingUrlsDocument,
        variables: { clipId },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.clipViewingUrls || null;
    } catch (error) {
      console.error('Failed to get clip viewing URLs:', error);
      throw error;
    }
  },

  // Mutations
  async createClip(input) {
    try {
      const result = await client.mutate({
        mutation: CreateClipDocument,
        variables: { input },
        errorPolicy: 'all'
      });
      return result.data?.createClip || null;
    } catch (error) {
      console.error('Failed to create clip:', error);
      throw error; // Re-throw for UI error handling
    }
  },

  async deleteClip(id) {
    try {
      const result = await client.mutate({
        mutation: DeleteClipDocument,
        variables: { id },
        errorPolicy: 'all'
      });
      return result.data?.deleteClip || false;
    } catch (error) {
      console.error('Failed to delete clip:', error);
      throw error;
    }
  }
};