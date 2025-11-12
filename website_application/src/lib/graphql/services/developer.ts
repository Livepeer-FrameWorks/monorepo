import { client } from "../client.js";
import {
  GetApiTokensDocument,
  CreateApiTokenDocument,
  RevokeApiTokenDocument,
} from "../generated/apollo-helpers";
import type {
  GetApiTokensQuery,
  CreateApiTokenMutation,
  CreateApiTokenMutationVariables,
  RevokeApiTokenMutation,
} from "../generated/types";

export const developerService = {
  // API Token management
  async getAPITokens(): Promise<GetApiTokensQuery["developerTokens"]> {
    const result = await client.query({
      query: GetApiTokensDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.developerTokens;
  },

  async createAPIToken(
    input: CreateApiTokenMutationVariables["input"],
  ): Promise<CreateApiTokenMutation["createDeveloperToken"]> {
    const result = await client.mutate({
      mutation: CreateApiTokenDocument,
      variables: { input },
      refetchQueries: [{ query: GetApiTokensDocument }],
    });
    return result.data!.createDeveloperToken;
  },

  async revokeAPIToken(id: string): Promise<RevokeApiTokenMutation["revokeDeveloperToken"]> {
    const result = await client.mutate({
      mutation: RevokeApiTokenDocument,
      variables: { id },
      refetchQueries: [{ query: GetApiTokensDocument }],
    });
    return result.data!.revokeDeveloperToken;
  },
};
