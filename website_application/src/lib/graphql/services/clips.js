import { client } from '../client.js';
import { 
  CreateClipDocument
} from '../generated/apollo-helpers';

export const clipsService = {
  // Mutations
  async createClip(input) {
    const result = await client.mutate({
      mutation: CreateClipDocument,
      variables: { input }
    });
    return result.data.createClip;
  }
};