import { client } from '../client.js';
import {
  GetApiTokensDocument,
  CreateApiTokenDocument,
  RevokeApiTokenDocument
} from '../generated/apollo-helpers';

export const developerService = {
  // API Token management
  async getAPITokens() {
    const result = await client.query({
      query: GetApiTokensDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.developerTokens;
  },

  async createAPIToken(input) {
    const result = await client.mutate({
      mutation: CreateApiTokenDocument,
      variables: { input },
      refetchQueries: [{ query: GetApiTokensDocument }]
    });
    return result.data.createDeveloperToken;
  },

  async revokeAPIToken(id) {
    const result = await client.mutate({
      mutation: RevokeApiTokenDocument,
      variables: { id },
      refetchQueries: [{ query: GetApiTokensDocument }]
    });
    return result.data.revokeDeveloperToken;
  }
};