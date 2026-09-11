import { useEffect, useMemo, useState } from "react";
import {
  GatewayClient,
  type ContentEndpoints,
  type ContentType,
  type PlaybackAuth,
  type ViewerProtocol,
} from "@livepeer-frameworks/player-core";

export interface ViewerEndpointsParams {
  gatewayUrl?: string;
  contentId: string;
  contentType?: ContentType;
  authToken?: string;
  playbackAuth?: PlaybackAuth;
  /** Required format for placement; never retried as an unqualified query. */
  protocol?: ViewerProtocol;
}

interface EndpointState {
  endpoints: ContentEndpoints | null;
  status: "idle" | "loading" | "ready" | "error";
  error: string | null;
}

export function useViewerEndpoints({
  gatewayUrl,
  contentId,
  contentType,
  authToken,
  playbackAuth,
  protocol,
}: ViewerEndpointsParams): EndpointState {
  const token = playbackAuth?.token;
  const transport = playbackAuth?.transport;
  const request = useMemo(
    () => ({
      gatewayUrl,
      contentId,
      contentType,
      authToken,
      protocol,
      playbackAuth: token ? { token, transport } : undefined,
    }),
    [gatewayUrl, contentId, contentType, authToken, protocol, token, transport]
  );
  const pending: EndpointState = {
    endpoints: null,
    status: contentId ? "loading" : "idle",
    error: null,
  };
  const [result, setResult] = useState<{ request: typeof request; state: EndpointState } | null>(
    null
  );

  useEffect(() => {
    if (!request.contentId) return;
    const client = new GatewayClient(request);
    let active = true;
    void client.resolve().then(
      (endpoints) => {
        if (active) setResult({ request, state: { endpoints, status: "ready", error: null } });
      },
      (error) => {
        if (active)
          setResult({
            request,
            state: {
              endpoints: null,
              status: "error",
              error: error instanceof Error ? error.message : "Gateway resolution failed",
            },
          });
      }
    );
    return () => {
      active = false;
      client.destroy();
    };
  }, [request]);

  // A changed request must never expose the previous destination, even before effect cleanup.
  return result?.request === request ? result.state : pending;
}

export default useViewerEndpoints;
