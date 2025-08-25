import { useEffect, useState, useRef } from 'react';
import type { ContentEndpoints } from '../types';

interface Params {
  gatewayUrl: string;
  contentType: 'live' | 'dvr' | 'clip';
  contentId: string;
  authToken?: string;
}

export function useViewerEndpoints({ gatewayUrl, contentType, contentId, authToken }: Params) {
  const [endpoints, setEndpoints] = useState<ContentEndpoints | null>(null);
  const [status, setStatus] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle');
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!gatewayUrl || !contentType || !contentId) return;
    setStatus('loading');
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
        const res = await fetch(graphqlEndpoint, {
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
        setStatus('error');
      }
    })();

    return () => ac.abort();
  }, [gatewayUrl, contentType, contentId, authToken]);

  return { endpoints, status };
}

export default useViewerEndpoints;


