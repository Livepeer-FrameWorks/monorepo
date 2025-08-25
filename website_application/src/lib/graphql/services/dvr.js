import { client } from '../client.js';
import { 
  StartDvrDocument,
  StopDvrDocument,
  SetStreamRecordingConfigDocument,
  GetDvrRequestsDocument,
  GetRecordingConfigDocument
} from '../generated/apollo-helpers';

export const dvrService = {
  // Start DVR recording for a stream
  async startDVR(internalName, streamId = null) {
    try {
      const result = await client.mutate({
        mutation: StartDvrDocument,
        variables: { internalName, streamId },
        errorPolicy: 'all'
      });
      
      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }
      
      return {
        success: true,
        dvrRequest: result.data?.startDVR || null,
        error: null
      };
    } catch (error) {
      console.error('Failed to start DVR:', error);
      return {
        success: false,
        dvrRequest: null,
        error: error.message || 'Failed to start DVR recording'
      };
    }
  },

  // Stop DVR recording
  async stopDVR(dvrHash) {
    try {
      const result = await client.mutate({
        mutation: StopDvrDocument,
        variables: { dvrHash },
        errorPolicy: 'all'
      });
      
      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }
      
      return {
        success: result.data?.stopDVR || false,
        error: null
      };
    } catch (error) {
      console.error('Failed to stop DVR:', error);
      return {
        success: false,
        error: error.message || 'Failed to stop DVR recording'
      };
    }
  },

  // Get DVR requests/recordings
  async getDVRRequests(filters = {}) {
    try {
      const { internalName, status, pagination } = filters;
      const result = await client.query({
        query: GetDvrRequestsDocument,
        variables: { internalName, status, pagination },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      
      const dvrRequestList = result.data?.dvrRequests || { dvrRecordings: [], total: 0, page: 1, limit: 20 };
      
      return {
        success: true,
        recordings: dvrRequestList.dvrRecordings || [],
        pagination: {
          total: dvrRequestList.total || 0,
          page: dvrRequestList.page || 1,
          limit: dvrRequestList.limit || 20
        },
        error: null
      };
    } catch (error) {
      console.error('Failed to fetch DVR requests:', error);
      return {
        success: false,
        recordings: [],
        pagination: { total: 0, page: 1, limit: 20 },
        error: error.message || 'Failed to fetch DVR recordings'
      };
    }
  },

  // Get DVR recordings for a specific stream
  async getStreamRecordings(internalName) {
    return await this.getDVRRequests({ internalName });
  },

  // Get DVR recordings by status
  async getRecordingsByStatus(status) {
    return await this.getDVRRequests({ status });
  },

  // Set stream recording configuration
  async setRecordingConfig(internalName, config) {
    try {
      const { enabled, retentionDays, format, segmentDuration } = config;
      const result = await client.mutate({
        mutation: SetStreamRecordingConfigDocument,
        variables: { 
          internalName, 
          enabled,
          retentionDays,
          format,
          segmentDuration
        },
        errorPolicy: 'all'
      });
      
      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }
      
      return {
        success: true,
        config: result.data?.setStreamRecordingConfig || null,
        error: null
      };
    } catch (error) {
      console.error('Failed to set recording config:', error);
      return {
        success: false,
        config: null,
        error: error.message || 'Failed to set recording configuration'
      };
    }
  },

  // Get stream recording configuration
  async getRecordingConfig(internalName) {
    try {
      const result = await client.query({
        query: GetRecordingConfigDocument,
        variables: { internalName },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      
      return {
        success: true,
        config: result.data?.recordingConfig || null,
        error: null
      };
    } catch (error) {
      console.error('Failed to get recording config:', error);
      return {
        success: false,
        config: null,
        error: error.message || 'Failed to get recording configuration'
      };
    }
  }
};