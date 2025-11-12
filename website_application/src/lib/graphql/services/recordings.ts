import { client } from "../client.js";
import { GetRecordingsDocument } from "../generated/apollo-helpers";
import type {
  GetRecordingsQuery,
  GetRecordingsQueryVariables,
} from "../generated/types";

interface OperationResult<T = null> {
  success: boolean;
  recording?: T;
  error?: string;
}

export const recordingsService = {
  // Get all recordings (optional streamId filter)
  async getRecordings(
    streamId: GetRecordingsQueryVariables["streamId"] = null,
  ): Promise<GetRecordingsQuery["recordings"]> {
    try {
      const result = await client.query({
        query: GetRecordingsDocument,
        variables: { streamId },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.recordings || [];
    } catch (error) {
      console.error("Failed to fetch recordings:", error);
      return [];
    }
  },

  // Get a specific recording by ID (search through all recordings)
  async getRecording(recordingId: string): Promise<GetRecordingsQuery["recordings"][0] | null> {
    try {
      const recordings = await this.getRecordings();
      return recordings.find((r) => r.id === recordingId) || null;
    } catch (error) {
      console.error("Failed to fetch recording:", error);
      return null;
    }
  },

  // Recording management operations are not available in current schema
  // These methods will return appropriate error responses

  async createRecording(_input: unknown): Promise<OperationResult> {
    console.warn("Recording creation not supported by current GraphQL schema");
    return {
      success: false,
      recording: null,
      error: "Recording creation not supported",
    };
  },

  async updateRecording(_recordingId: string, _input: unknown): Promise<OperationResult> {
    console.warn("Recording update not supported by current GraphQL schema");
    return {
      success: false,
      recording: null,
      error: "Recording update not supported",
    };
  },

  async deleteRecording(_recordingId: string): Promise<OperationResult> {
    console.warn("Recording deletion not supported by current GraphQL schema");
    return {
      success: false,
      error: "Recording deletion not supported",
    };
  },

  async stopRecording(_recordingId: string): Promise<OperationResult> {
    console.warn("Recording stop not supported by current GraphQL schema");
    return {
      success: false,
      recording: null,
      error: "Recording stop not supported",
    };
  },

  // Get recordings for a specific stream
  async getStreamRecordings(streamId: string): Promise<GetRecordingsQuery["recordings"]> {
    try {
      return await this.getRecordings(streamId);
    } catch (error) {
      console.error("Failed to fetch stream recordings:", error);
      return [];
    }
  },

  // Get recordings by status (client-side filtering)
  async getRecordingsByStatus(status: string): Promise<GetRecordingsQuery["recordings"]> {
    try {
      const recordings = await this.getRecordings();
      return recordings.filter((recording) => recording.status === status);
    } catch (error) {
      console.error("Failed to fetch recordings by status:", error);
      return [];
    }
  },

  // Get all recordings for current tenant (determined by auth context)
  async getTenantRecordings(): Promise<GetRecordingsQuery["recordings"]> {
    try {
      return await this.getRecordings();
    } catch (error) {
      console.error("Failed to fetch tenant recordings:", error);
      return [];
    }
  },
};
