import { useEffect, useState, useRef } from "react";
import type { ContentType } from "@livepeer-frameworks/player-core";
import type { ContentEndpoints } from "../types";

const MAX_RETRIES = 3;
const INITIAL_DELAY_MS = 500;
const DEFAULT_GATEWAY_URL = "https://bridge.frameworks.network/graphql";
const RETRYABLE_HTTP_STATUSES = new Set([408, 429, 500, 502, 503, 504]);

type GraphQLErrorPayload = {
  message?: string;
  extensions?: {
    code?: string;
  };
};

interface Params {
  gatewayUrl?: string;
  contentId: string;
  contentType?: ContentType;
  authToken?: string;
}

function getFirstGraphQLError(payload: unknown): GraphQLErrorPayload | null {
  const errors = (payload as { errors?: GraphQLErrorPayload[] })?.errors;
  return Array.isArray(errors) && errors.length > 0 ? errors[0] : null;
}

function isRetryableGraphQLError(error: GraphQLErrorPayload): boolean {
  const code = error.extensions?.code?.toUpperCase();
  const message = (error.message || "").toLowerCase();

  return (
    code === "UNAVAILABLE" ||
    code === "SERVICE_UNAVAILABLE" ||
    message.includes("temporarily unavailable")
  );
}

async function fetchResolvePayloadWithRetry(
  url: string,
  options: RequestInit,
  maxRetries: number = MAX_RETRIES,
  initialDelay: number = INITIAL_DELAY_MS
): Promise<unknown> {
  let lastError: Error | null = null;

  for (let attempt = 0; attempt < maxRetries; attempt++) {
    let response: Response | null = null;

    try {
      response = await fetch(url, options);
    } catch (e) {
      lastError = e instanceof Error ? e : new Error("Fetch failed");

      // Don't retry on abort
      if (options.signal?.aborted) {
        throw lastError;
      }

      if (attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(
          `[useViewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`
        );
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }
    }

    if (!response) {
      throw lastError ?? new Error("Gateway unreachable after retries");
    }

    if (!response.ok) {
      lastError = new Error(`Gateway GQL error ${response.status}`);

      if (RETRYABLE_HTTP_STATUSES.has(response.status) && attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(
          `[useViewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`
        );
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }

      throw lastError;
    }

    const payload = await response.json();
    const gqlError = getFirstGraphQLError(payload);

    if (gqlError) {
      lastError = new Error(gqlError.message || "GraphQL error");

      if (isRetryableGraphQLError(gqlError) && attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(
          `[useViewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`
        );
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }

      throw lastError;
    }

    return payload;
  }

  throw lastError ?? new Error("Gateway unreachable after retries");
}

export function useViewerEndpoints({
  gatewayUrl,
  contentType: _contentType,
  contentId,
  authToken,
}: Params) {
  const [endpoints, setEndpoints] = useState<ContentEndpoints | null>(null);
  const [status, setStatus] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const effectiveGatewayUrl = gatewayUrl?.trim() || DEFAULT_GATEWAY_URL;

  useEffect(() => {
    if (!contentId) return;
    setStatus("loading");
    setError(null);
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;

    (async () => {
      try {
        const graphqlEndpoint = effectiveGatewayUrl.replace(/\/$/, "");
        const query = `
          query ResolveViewer($contentId: String!) {
            resolveViewerEndpoint(contentId: $contentId) {
              primary { nodeId baseUrl protocol url geoDistance loadScore outputs }
              fallbacks { nodeId baseUrl protocol url geoDistance loadScore outputs }
              metadata { contentType contentId title description durationSeconds status isLive viewers recordingSizeBytes clipSource createdAt telemetryToken }
            }
          }
        `;
        const payload = await fetchResolvePayloadWithRetry(graphqlEndpoint, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
          },
          body: JSON.stringify({ query, variables: { contentId } }),
          signal: ac.signal,
        });
        const resp = (payload as { data?: { resolveViewerEndpoint?: any } }).data
          ?.resolveViewerEndpoint;
        const primary = resp?.primary;
        const fallbacks = Array.isArray(resp?.fallbacks) ? resp.fallbacks : [];
        if (!primary) throw new Error("No endpoints");

        // Parse outputs JSON string (GraphQL returns JSON scalar as string)
        if (primary.outputs && typeof primary.outputs === "string") {
          try {
            primary.outputs = JSON.parse(primary.outputs);
          } catch {}
        }
        if (fallbacks) {
          fallbacks.forEach((fb: any) => {
            if (fb.outputs && typeof fb.outputs === "string") {
              try {
                fb.outputs = JSON.parse(fb.outputs);
              } catch {}
            }
          });
        }

        setEndpoints({ primary, fallbacks, metadata: resp?.metadata });
        setStatus("ready");
      } catch (e) {
        if (ac.signal.aborted) return;
        const message = e instanceof Error ? e.message : "Unknown gateway error";
        console.error("[useViewerEndpoints] Gateway resolution failed:", message);
        setError(message);
        setStatus("error");
      }
    })();

    return () => ac.abort();
  }, [effectiveGatewayUrl, contentId, authToken]);

  return { endpoints, status, error };
}

export default useViewerEndpoints;
