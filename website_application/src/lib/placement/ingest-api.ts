import { ResolveIngestDestinationStore } from "$houdini";
import { resolveOperationalStreamId } from "$lib/route-ids";
import { ingestProtocolUrl, isSpecificIngest, type ResolvedIngest } from "./ingest";

/**
 * A failed ingest resolution. `code` is the gateway's `extensions.code` when
 * the resolver refused the request (UNAUTHORIZED, FORBIDDEN, NOT_FOUND,
 * UNAVAILABLE, a billing reason, ...), or one of the local codes below when
 * the response itself was unusable.
 */
export class IngestResolutionError extends Error {
  readonly code: string;

  constructor(message: string, code: string) {
    super(message);
    this.name = "IngestResolutionError";
    this.code = code;
  }
}

const RESOLUTION_FAILED =
  "Ingest resolution failed. No generic address has been substituted. Check access and capacity, then retry.";

type GraphQLErrorLike = { extensions?: { code?: unknown } };

export async function resolveIngestDestination(
  streamId: string,
  streamKey: string,
  signal?: AbortSignal,
  protocol?: "WHIP" | "RTMP" | "SRT"
): Promise<ResolvedIngest> {
  if (!streamId || !streamKey)
    throw new IngestResolutionError("Select a stream with an active key first.", "NO_STREAM_KEY");
  const abort = new AbortController();
  const cancel = () => abort.abort();
  signal?.addEventListener("abort", cancel, { once: true });
  if (signal?.aborted) cancel();
  const timeout = setTimeout(() => abort.abort(), 5000);
  try {
    const response = await new ResolveIngestDestinationStore().fetch({
      variables: { streamKey, protocol },
      policy: "NetworkOnly",
      fetch: (input, init) => fetch(input, { ...init, signal: abort.signal }),
    });
    if (abort.signal.aborted) throw new DOMException("Resolution cancelled", "AbortError");
    const graphQLError = (response.errors as GraphQLErrorLike[] | null | undefined)?.[0];
    if (graphQLError) {
      const code = graphQLError.extensions?.code;
      throw new IngestResolutionError(
        RESOLUTION_FAILED,
        typeof code === "string" && code ? code : "INTERNAL_ERROR"
      );
    }
    const result = response.data?.resolveIngestEndpoint;
    if (!result || !isSpecificIngest(result.primary)) {
      throw new IngestResolutionError(RESOLUTION_FAILED, "NO_DESTINATION");
    }
    if (
      resolveOperationalStreamId({ routeParamId: result.metadata?.streamId ?? "" }) !== streamId
    ) {
      throw new IngestResolutionError(RESOLUTION_FAILED, "STREAM_MISMATCH");
    }
    const requested = protocol === "WHIP" ? "whip" : protocol === "RTMP" ? "rtmp" : "srt";
    if (protocol && !ingestProtocolUrl(result.primary, requested)) {
      throw new IngestResolutionError(RESOLUTION_FAILED, "PROTOCOL_UNAVAILABLE");
    }
    return result;
  } catch (error) {
    if (signal?.aborted) throw new DOMException("Resolution cancelled", "AbortError");
    if (error instanceof IngestResolutionError) throw error;
    throw new IngestResolutionError(
      RESOLUTION_FAILED,
      abort.signal.aborted ? "TIMEOUT" : "NETWORK"
    );
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", cancel);
  }
}
