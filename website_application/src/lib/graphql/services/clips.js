import { client } from '../client.js';
import { 
  CreateClipDocument
} from '../generated/apollo-helpers';

export const clipsService = {
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
  }
};