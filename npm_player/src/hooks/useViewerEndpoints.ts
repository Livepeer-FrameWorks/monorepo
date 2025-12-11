import { useEffect, useState, useRef } from 'react';
import type { ContentEndpoints } from '../types';

const MAX_RETRIES = 3;
const INITIAL_DELAY_MS = 500;

interface Params {
  gatewayUrl: string;
  contentType: 'live' | 'dvr' | 'clip';
  contentId: string;
  authToken?: string;
}

async function fetchWithRetry(
  url: string,
  options: RequestInit,
  maxRetries: number = MAX_RETRIES,
  initialDelay: number = INITIAL_DELAY_MS
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
        console.warn(`[useViewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise(resolve => setTimeout(resolve, delay));
      }
    }
  }

  throw lastError ?? new Error('Gateway unreachable after retries');
}

export function useViewerEndpoints({ gatewayUrl, contentType, contentId, authToken }: Params) {
  const [endpoints, setEndpoints] = useState<ContentEndpoints | null>(null);
  const [status, setStatus] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle');
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!gatewayUrl || !contentType || !contentId) return;
    setStatus('loading');
    setError(null);
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;

    (async () => {
      try {
        const graphqlEndpoint = gatewayUrl.replace(/\/$/, '');
        const query = `
          query ResolveViewer($contentType: String!, $contentId: String!) {
            resolveViewerEndpoint(contentType: $contentType, contentId: $contentId) {
              endpoints { nodeId baseUrl protocol url geoDistance loadScore healthScore outputs }
              metadata { contentType contentId title description duration status isLive viewCount recordingSize clipSource createdAt }
            }
          }
        `;
        const res = await fetchWithRetry(graphqlEndpoint, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
          },
          body: JSON.stringify({ query, variables: { contentType, contentId } }),
          signal: ac.signal,
        });
        if (!res.ok) throw new Error(`Gateway GQL error ${res.status}`);
        const payload = await res.json();
        if (payload.errors?.length) throw new Error(payload.errors[0]?.message || 'GraphQL error');
        const resp = payload.data?.resolveViewerEndpoint;
        const list = Array.isArray(resp?.endpoints) ? resp.endpoints : [];
        const primary = list[0];
        const fallbacks = list.slice(1);
        if (!primary) throw new Error('No endpoints');
        setEndpoints({ primary, fallbacks, metadata: resp?.metadata });
        setStatus('ready');
      } catch (e) {
        if (ac.signal.aborted) return;
        const message = e instanceof Error ? e.message : 'Unknown gateway error';
        console.error('[useViewerEndpoints] Gateway resolution failed:', message);
        setError(message);
        setStatus('error');
      }
    })();

    return () => ac.abort();
  }, [gatewayUrl, contentType, contentId, authToken]);

  return { endpoints, status, error };
}

export default useViewerEndpoints;


