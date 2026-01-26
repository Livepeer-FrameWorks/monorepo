/**
 * GatewayClient.ts
 *
 * Framework-agnostic client for resolving viewer endpoints from the Gateway GraphQL API.
 * Extracted from useViewerEndpoints.ts for use in headless core.
 */

import { TypedEventEmitter } from './EventEmitter';
import type { ContentEndpoints, ContentType } from '../types';

// ============================================================================
// Types
// ============================================================================

export type GatewayStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface GatewayClientConfig {
  /** Gateway GraphQL endpoint URL */
  gatewayUrl: string;
  /** Content identifier (stream name) */
  contentId: string;
  /** Optional content type (no longer required for resolution) */
  contentType?: ContentType;
  /** Optional auth token for private streams */
  authToken?: string;
  /** Maximum retry attempts (default: 3) */
  maxRetries?: number;
  /** Initial retry delay in ms (default: 500) */
  initialDelayMs?: number;
}

export interface GatewayClientEvents {
  /** Emitted when status changes */
  statusChange: { status: GatewayStatus; error?: string };
  /** Emitted when endpoints are successfully resolved */
  endpointsResolved: { endpoints: ContentEndpoints };
}

// ============================================================================
// Constants
// ============================================================================

const DEFAULT_MAX_RETRIES = 3;
const DEFAULT_INITIAL_DELAY_MS = 500;
// F2: Cache TTL for resolved endpoints
const DEFAULT_CACHE_TTL_MS = 10000;
// F3: Circuit breaker constants
const CIRCUIT_BREAKER_THRESHOLD = 5;       // Open after 5 consecutive failures
const CIRCUIT_BREAKER_TIMEOUT_MS = 30000;  // Half-open after 30 seconds

type CircuitBreakerState = 'closed' | 'open' | 'half-open';

const RESOLVE_VIEWER_QUERY = `
  query ResolveViewer($contentId: String!) {
    resolveViewerEndpoint(contentId: $contentId) {
      primary { nodeId baseUrl protocol url geoDistance loadScore outputs }
      fallbacks { nodeId baseUrl protocol url geoDistance loadScore outputs }
      metadata { contentType contentId title description durationSeconds status isLive viewers recordingSizeBytes clipSource createdAt }
    }
  }
`;

// ============================================================================
// Helper Functions
// ============================================================================

/**
 * Fetch with exponential backoff retry logic.
 */
async function fetchWithRetry(
  url: string,
  options: RequestInit,
  maxRetries: number,
  initialDelay: number
): Promise<Response> {
  let lastError: Error | null = null;

  for (let attempt = 0; attempt < maxRetries; attempt++) {
    try {
      const response = await fetch(url, options);
      return response;
    } catch (e) {
      lastError = e instanceof Error ? e : new Error('Fetch failed');

      // Don't retry on abort
      if (options.signal?.aborted) {
        throw lastError;
      }

      // Wait before retrying (exponential backoff)
      if (attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(`[GatewayClient] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise(resolve => setTimeout(resolve, delay));
      }
    }
  }

  throw lastError ?? new Error('Gateway unreachable after retries');
}

// ============================================================================
// GatewayClient Class
// ============================================================================

/**
 * Client for resolving viewer endpoints from the Gateway GraphQL API.
 *
 * @example
 * ```typescript
 * const client = new GatewayClient({
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 *   contentId: 'pk_...', // playbackId (view key)
 * });
 *
 * client.on('statusChange', ({ status }) => console.log('Status:', status));
 * client.on('endpointsResolved', ({ endpoints }) => console.log('Endpoints:', endpoints));
 *
 * const endpoints = await client.resolve();
 * ```
 */
export class GatewayClient extends TypedEventEmitter<GatewayClientEvents> {
  private config: GatewayClientConfig;
  private status: GatewayStatus = 'idle';
  private endpoints: ContentEndpoints | null = null;
  private error: string | null = null;
  private abortController: AbortController | null = null;

  // F2: Request deduplication - in-flight request tracking
  private inFlightRequest: Promise<ContentEndpoints> | null = null;
  // F2: Cache with TTL for resolved endpoints
  private cacheTimestamp = 0;
  private cacheTtlMs: number;

  // F3: Circuit breaker state
  private circuitState: CircuitBreakerState = 'closed';
  private consecutiveFailures = 0;
  private circuitOpenedAt = 0;

  constructor(config: GatewayClientConfig) {
    super();
    this.config = config;
    this.cacheTtlMs = DEFAULT_CACHE_TTL_MS;
  }

  /**
   * Resolve endpoints from the gateway.
   * F2: Returns cached result if still valid, deduplicates concurrent requests.
   * F3: Respects circuit breaker state.
   * @param forceRefresh - If true, bypasses cache and fetches fresh data
   * @returns Promise resolving to ContentEndpoints
   * @throws Error if resolution fails after retries or circuit is open
   */
  async resolve(forceRefresh = false): Promise<ContentEndpoints> {
    // F2: Return cached result if still valid
    if (!forceRefresh && this.endpoints && this.isCacheValid()) {
      return this.endpoints;
    }

    // F3: Check circuit breaker
    if (!this.canAttemptRequest()) {
      throw new Error('Circuit breaker is open - too many recent failures');
    }

    // F2: Return in-flight request if one exists (deduplication)
    if (this.inFlightRequest) {
      return this.inFlightRequest;
    }

    // Create a new request and track it
    this.inFlightRequest = this.doResolve();

    try {
      const result = await this.inFlightRequest;
      // F3: Success - close circuit
      this.onSuccess();
      return result;
    } catch (e) {
      // F3: Failure - record for circuit breaker
      this.onFailure();
      throw e;
    } finally {
      this.inFlightRequest = null;
    }
  }

  /**
   * F2: Check if cache is still valid
   */
  private isCacheValid(): boolean {
    return Date.now() - this.cacheTimestamp < this.cacheTtlMs;
  }

  /**
   * F2: Set cache TTL (for testing or custom requirements)
   */
  setCacheTtl(ttlMs: number): void {
    this.cacheTtlMs = ttlMs;
  }

  /**
   * F2: Invalidate the cache manually
   */
  invalidateCache(): void {
    this.cacheTimestamp = 0;
  }

  // ==========================================================================
  // F3: Circuit Breaker Methods
  // ==========================================================================

  /**
   * F3: Check if a request can be attempted based on circuit state
   */
  private canAttemptRequest(): boolean {
    switch (this.circuitState) {
      case 'closed':
        return true;

      case 'open':
        // Check if enough time has passed to try half-open
        if (Date.now() - this.circuitOpenedAt >= CIRCUIT_BREAKER_TIMEOUT_MS) {
          this.circuitState = 'half-open';
          return true;
        }
        return false;

      case 'half-open':
        // Allow one request to test the circuit
        return true;
    }
  }

  /**
   * F3: Record a successful request
   */
  private onSuccess(): void {
    this.consecutiveFailures = 0;
    this.circuitState = 'closed';
  }

  /**
   * F3: Record a failed request
   */
  private onFailure(): void {
    this.consecutiveFailures++;

    if (this.circuitState === 'half-open') {
      // Failed during half-open - re-open the circuit
      this.circuitState = 'open';
      this.circuitOpenedAt = Date.now();
    } else if (this.consecutiveFailures >= CIRCUIT_BREAKER_THRESHOLD) {
      // Threshold reached - open the circuit
      this.circuitState = 'open';
      this.circuitOpenedAt = Date.now();
      console.warn(`[GatewayClient] Circuit breaker opened after ${this.consecutiveFailures} consecutive failures`);
    }
  }

  /**
   * F3: Get current circuit breaker state (for monitoring/debugging)
   */
  getCircuitState(): { state: CircuitBreakerState; failures: number; openedAt: number | null } {
    return {
      state: this.circuitState,
      failures: this.consecutiveFailures,
      openedAt: this.circuitState === 'open' ? this.circuitOpenedAt : null,
    };
  }

  /**
   * F3: Manually reset the circuit breaker
   */
  resetCircuitBreaker(): void {
    this.circuitState = 'closed';
    this.consecutiveFailures = 0;
    this.circuitOpenedAt = 0;
  }

  /**
   * Internal method to perform the actual resolution.
   * @returns Promise resolving to ContentEndpoints
   */
  private async doResolve(): Promise<ContentEndpoints> {
    // Abort any in-flight fetch (different from inFlightRequest promise tracking)
    this.abort();

    const {
      gatewayUrl,
      contentId,
      authToken,
      maxRetries = DEFAULT_MAX_RETRIES,
      initialDelayMs = DEFAULT_INITIAL_DELAY_MS,
    } = this.config;

    // Validate required params
    if (!gatewayUrl || !contentId) {
      const error = 'Missing required parameters: gatewayUrl or contentId';
      this.setStatus('error', error);
      throw new Error(error);
    }

    this.setStatus('loading');

    const ac = new AbortController();
    this.abortController = ac;

    try {
      const graphqlEndpoint = gatewayUrl.replace(/\/$/, '');

      const res = await fetchWithRetry(
        graphqlEndpoint,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
          },
          body: JSON.stringify({
            query: RESOLVE_VIEWER_QUERY,
            variables: { contentId },
          }),
          signal: ac.signal,
        },
        maxRetries,
        initialDelayMs
      );

      if (!res.ok) {
        throw new Error(`Gateway GQL error ${res.status}`);
      }

      const payload = await res.json();

      if (payload.errors?.length) {
        throw new Error(payload.errors[0]?.message || 'GraphQL error');
      }

      const resp = payload.data?.resolveViewerEndpoint;
      const primary = resp?.primary;
      const fallbacks = Array.isArray(resp?.fallbacks) ? resp.fallbacks : [];

      if (!primary) {
        throw new Error('No endpoints available');
      }

      const endpoints: ContentEndpoints = {
        primary,
        fallbacks,
        metadata: resp?.metadata,
      };

      this.endpoints = endpoints;
      // F2: Update cache timestamp
      this.cacheTimestamp = Date.now();
      this.setStatus('ready');
      this.emit('endpointsResolved', { endpoints });

      return endpoints;
    } catch (e) {
      // Ignore abort errors
      if (ac.signal.aborted) {
        throw new Error('Request aborted');
      }

      const message = e instanceof Error ? e.message : 'Unknown gateway error';
      console.error('[GatewayClient] Gateway resolution failed:', message);
      this.setStatus('error', message);
      throw new Error(message);
    }
  }

  /**
   * Abort any in-flight request.
   */
  abort(): void {
    if (this.abortController) {
      this.abortController.abort();
      this.abortController = null;
    }
  }

  /**
   * Get current status.
   */
  getStatus(): GatewayStatus {
    return this.status;
  }

  /**
   * Get resolved endpoints (null if not yet resolved).
   */
  getEndpoints(): ContentEndpoints | null {
    return this.endpoints;
  }

  /**
   * Get error message (null if no error).
   */
  getError(): string | null {
    return this.error;
  }

  /**
   * Update configuration and reset state.
   * F2: Also clears cache and in-flight request
   * F3: Resets circuit breaker (new config = fresh start)
   */
  updateConfig(config: Partial<GatewayClientConfig>): void {
    this.abort();
    this.config = { ...this.config, ...config };
    this.endpoints = null;
    this.error = null;
    // F2: Clear cache and in-flight tracking
    this.cacheTimestamp = 0;
    this.inFlightRequest = null;
    // F3: Reset circuit breaker for new config
    this.resetCircuitBreaker();
    this.setStatus('idle');
  }

  /**
   * Clean up resources.
   */
  destroy(): void {
    this.abort();
    this.removeAllListeners();
  }

  private setStatus(status: GatewayStatus, error?: string): void {
    this.status = status;
    this.error = error ?? null;
    this.emit('statusChange', { status, error });
  }
}

export default GatewayClient;
