/**
 * IngestClient - Resolves ingest endpoints via Gateway
 * Mirrors GatewayClient from npm_player for consistency
 */

import { TypedEventEmitter } from './EventEmitter';
import type {
  IngestClientConfig,
  IngestClientEvents,
  IngestEndpoints,
} from '../types';

const RESOLVE_INGEST_QUERY = `
  query ResolveIngest($streamKey: String!) {
    resolveIngestEndpoint(streamKey: $streamKey) {
      primary {
        nodeId
        baseUrl
        whipUrl
        rtmpUrl
        srtUrl
        region
        loadScore
      }
      fallbacks {
        nodeId
        baseUrl
        whipUrl
        rtmpUrl
        srtUrl
        region
        loadScore
      }
      metadata {
        streamId
        streamKey
        tenantId
        recordingEnabled
      }
    }
  }
`;

export class IngestClient extends TypedEventEmitter<IngestClientEvents> {
  private config: IngestClientConfig;
  private abortController: AbortController | null = null;
  private endpoints: IngestEndpoints | null = null;
  private retryCount = 0;
  private retryTimeout: ReturnType<typeof setTimeout> | null = null;

  constructor(config: IngestClientConfig) {
    super();
    this.config = {
      maxRetries: 3,
      initialDelayMs: 1000,
      ...config,
    };
  }

  /**
   * Get currently resolved endpoints (null if not resolved yet)
   */
  getEndpoints(): IngestEndpoints | null {
    return this.endpoints;
  }

  /**
   * Resolve ingest endpoints from the gateway
   */
  async resolve(): Promise<IngestEndpoints> {
    const { gatewayUrl, streamKey, authToken, maxRetries, initialDelayMs } = this.config;

    this.abortController = new AbortController();
    this.emit('statusChange', { status: 'loading' });

    try {
      const response = await fetch(gatewayUrl, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
        },
        body: JSON.stringify({
          query: RESOLVE_INGEST_QUERY,
          variables: { streamKey },
        }),
        signal: this.abortController.signal,
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const payload = await response.json();

      if (payload.errors?.length > 0) {
        throw new Error(payload.errors[0].message || 'GraphQL error');
      }

      const data = payload.data?.resolveIngestEndpoint;
      if (!data) {
        throw new Error('No data returned from resolveIngestEndpoint');
      }

      this.endpoints = {
        primary: data.primary,
        fallbacks: data.fallbacks || [],
        metadata: data.metadata,
      };

      this.retryCount = 0;
      this.emit('endpointsResolved', { endpoints: this.endpoints });
      this.emit('statusChange', { status: 'ready' });

      return this.endpoints;
    } catch (error) {
      if ((error as Error).name === 'AbortError') {
        this.emit('statusChange', { status: 'idle' });
        throw error;
      }

      const errorMessage = error instanceof Error ? error.message : 'Unknown error';

      // Retry logic
      if (this.retryCount < (maxRetries || 3)) {
        this.retryCount++;
        const delay = (initialDelayMs || 1000) * Math.pow(2, this.retryCount - 1);

        this.emit('statusChange', {
          status: 'loading',
          error: `Retrying (${this.retryCount}/${maxRetries})...`,
        });

        await new Promise<void>((resolve) => {
          this.retryTimeout = setTimeout(resolve, delay);
        });

        return this.resolve();
      }

      this.emit('statusChange', { status: 'error', error: errorMessage });
      throw new Error(`Failed to resolve ingest endpoint: ${errorMessage}`);
    }
  }

  /**
   * Get the primary WHIP URL for streaming
   */
  getWhipUrl(): string | null {
    return this.endpoints?.primary?.whipUrl || null;
  }

  /**
   * Get the primary RTMP URL for streaming
   */
  getRtmpUrl(): string | null {
    return this.endpoints?.primary?.rtmpUrl || null;
  }

  /**
   * Get the primary SRT URL for streaming
   */
  getSrtUrl(): string | null {
    return this.endpoints?.primary?.srtUrl || null;
  }

  /**
   * Clean up resources
   */
  destroy(): void {
    this.abortController?.abort();
    if (this.retryTimeout) {
      clearTimeout(this.retryTimeout);
      this.retryTimeout = null;
    }
    this.endpoints = null;
  }
}
