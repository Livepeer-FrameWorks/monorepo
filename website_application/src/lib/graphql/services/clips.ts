import { client } from "../client.js";
import {
  CreateClipDocument,
  DeleteClipDocument,
  GetClipsDocument,
  GetClipDocument,
  GetClipViewingUrlsDocument,
} from "../generated/apollo-helpers";
import type {
  GetClipsQuery,
  GetClipsQueryVariables,
  GetClipQuery,
  GetClipQueryVariables,
  GetClipViewingUrlsQuery,
  GetClipViewingUrlsQueryVariables,
  CreateClipMutation,
  CreateClipMutationVariables,
  DeleteClipMutation,
  DeleteClipMutationVariables,
} from "../generated/types";

export const clipsService = {
  // Queries
  async getClips(streamId: GetClipsQueryVariables["streamId"] = null): Promise<GetClipsQuery["clips"]> {
    try {
      const result = await client.query({
        query: GetClipsDocument,
        variables: { streamId },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.clips || [];
    } catch (error) {
      console.error("Failed to get clips:", error);
      throw error;
    }
  },

  async getClip(id: GetClipQueryVariables["id"]): Promise<GetClipQuery["clip"] | null> {
    try {
      const result = await client.query({
        query: GetClipDocument,
        variables: { id },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.clip || null;
    } catch (error) {
      console.error("Failed to get clip:", error);
      throw error;
    }
  },

  async getClipViewingUrls(
    clipId: GetClipViewingUrlsQueryVariables["clipId"],
  ): Promise<GetClipViewingUrlsQuery["clipViewingUrls"] | null> {
    try {
      const result = await client.query({
        query: GetClipViewingUrlsDocument,
        variables: { clipId },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.clipViewingUrls || null;
    } catch (error) {
      console.error("Failed to get clip viewing URLs:", error);
      throw error;
    }
  },

  // Mutations
  async createClip(
    input: CreateClipMutationVariables["input"],
  ): Promise<CreateClipMutation["createClip"] | null> {
    try {
      const result = await client.mutate({
        mutation: CreateClipDocument,
        variables: { input },
        errorPolicy: "all",
      });
      return result.data?.createClip || null;
    } catch (error) {
      console.error("Failed to create clip:", error);
      throw error; // Re-throw for UI error handling
    }
  },

  async deleteClip(id: DeleteClipMutationVariables["id"]): Promise<DeleteClipMutation["deleteClip"]> {
    try {
      const result = await client.mutate({
        mutation: DeleteClipDocument,
        variables: { id },
        errorPolicy: "all",
      });
      return result.data?.deleteClip || false;
    } catch (error) {
      console.error("Failed to delete clip:", error);
      throw error;
    }
  },
};
