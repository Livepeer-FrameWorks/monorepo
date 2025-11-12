import { client } from "../client.js";
import {
  StartDvrDocument,
  StopDvrDocument,
  SetStreamRecordingConfigDocument,
  GetDvrRequestsDocument,
  GetRecordingConfigDocument,
} from "../generated/apollo-helpers";
import type {
  StartDvrMutation,
  StartDvrMutationVariables,
  StopDvrMutation,
  StopDvrMutationVariables,
  GetDvrRequestsQuery,
  GetDvrRequestsQueryVariables,
  SetStreamRecordingConfigMutation,
  SetStreamRecordingConfigMutationVariables,
  GetRecordingConfigQuery,
  GetRecordingConfigQueryVariables,
} from "../generated/types";

interface DVRStartResult {
  success: boolean;
  dvrRequest: StartDvrMutation["startDVR"] | null;
  error: string | null;
}

interface DVRStopResult {
  success: boolean;
  error: string | null;
}

interface DVRRequestsResult {
  success: boolean;
  recordings: GetDvrRequestsQuery["dvrRequests"]["dvrRecordings"];
  pagination: {
    total: number;
    page: number;
    limit: number;
  };
  error: string | null;
}

interface RecordingConfigResult {
  success: boolean;
  config: SetStreamRecordingConfigMutation["setStreamRecordingConfig"] | GetRecordingConfigQuery["recordingConfig"] | null;
  error: string | null;
}

interface DVRFilters {
  internalName?: string;
  status?: string;
  pagination?: {
    page?: number;
    limit?: number;
  };
}

interface RecordingConfig {
  enabled?: boolean;
  retentionDays?: number;
  format?: string;
  segmentDuration?: number;
}

export const dvrService = {
  // Start DVR recording for a stream
  async startDVR(
    internalName: string,
    streamId: StartDvrMutationVariables["streamId"] = null,
  ): Promise<DVRStartResult> {
    try {
      const result = await client.mutate({
        mutation: StartDvrDocument,
        variables: { internalName, streamId },
        errorPolicy: "all",
      });

      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }

      return {
        success: true,
        dvrRequest: result.data?.startDVR || null,
        error: null,
      };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to start DVR recording";
      console.error("Failed to start DVR:", error);
      return {
        success: false,
        dvrRequest: null,
        error: errorMessage,
      };
    }
  },

  // Stop DVR recording
  async stopDVR(dvrHash: StopDvrMutationVariables["dvrHash"]): Promise<DVRStopResult> {
    try {
      const result = await client.mutate({
        mutation: StopDvrDocument,
        variables: { dvrHash },
        errorPolicy: "all",
      });

      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }

      return {
        success: result.data?.stopDVR || false,
        error: null,
      };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to stop DVR recording";
      console.error("Failed to stop DVR:", error);
      return {
        success: false,
        error: errorMessage,
      };
    }
  },

  // Get DVR requests/recordings
  async getDVRRequests(filters: DVRFilters = {}): Promise<DVRRequestsResult> {
    try {
      const { internalName, status, pagination } = filters;
      const result = await client.query({
        query: GetDvrRequestsDocument,
        variables: { internalName, status, pagination } as GetDvrRequestsQueryVariables,
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });

      const dvrRequestList = result.data?.dvrRequests || {
        dvrRecordings: [],
        total: 0,
        page: 1,
        limit: 20,
      };

      return {
        success: true,
        recordings: dvrRequestList.dvrRecordings || [],
        pagination: {
          total: dvrRequestList.total || 0,
          page: dvrRequestList.page || 1,
          limit: dvrRequestList.limit || 20,
        },
        error: null,
      };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to fetch DVR recordings";
      console.error("Failed to fetch DVR requests:", error);
      return {
        success: false,
        recordings: [],
        pagination: { total: 0, page: 1, limit: 20 },
        error: errorMessage,
      };
    }
  },

  // Get DVR recordings for a specific stream
  async getStreamRecordings(internalName: string): Promise<DVRRequestsResult> {
    return await this.getDVRRequests({ internalName });
  },

  // Get DVR recordings by status
  async getRecordingsByStatus(status: string): Promise<DVRRequestsResult> {
    return await this.getDVRRequests({ status });
  },

  // Set stream recording configuration
  async setRecordingConfig(internalName: string, config: RecordingConfig): Promise<RecordingConfigResult> {
    try {
      const { enabled, retentionDays, format, segmentDuration } = config;
      const result = await client.mutate({
        mutation: SetStreamRecordingConfigDocument,
        variables: {
          internalName,
          enabled,
          retentionDays,
          format,
          segmentDuration,
        } as SetStreamRecordingConfigMutationVariables,
        errorPolicy: "all",
      });

      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }

      return {
        success: true,
        config: result.data?.setStreamRecordingConfig || null,
        error: null,
      };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to set recording configuration";
      console.error("Failed to set recording config:", error);
      return {
        success: false,
        config: null,
        error: errorMessage,
      };
    }
  },

  // Get stream recording configuration
  async getRecordingConfig(internalName: GetRecordingConfigQueryVariables["internalName"]): Promise<RecordingConfigResult> {
    try {
      const result = await client.query({
        query: GetRecordingConfigDocument,
        variables: { internalName },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });

      return {
        success: true,
        config: result.data?.recordingConfig || null,
        error: null,
      };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to get recording configuration";
      console.error("Failed to get recording config:", error);
      return {
        success: false,
        config: null,
        error: errorMessage,
      };
    }
  },
};
