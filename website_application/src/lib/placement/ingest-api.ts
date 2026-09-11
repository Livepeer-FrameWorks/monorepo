import { ResolveIngestDestinationStore } from "$houdini";
import { resolveOperationalStreamId } from "$lib/route-ids";
import { ingestProtocolUrl, isSpecificIngest, type ResolvedIngest } from "./ingest";

export async function resolveIngestDestination(
  streamId: string,
  streamKey: string,
  signal?: AbortSignal,
  protocol?: "WHIP" | "RTMP" | "SRT"
): Promise<ResolvedIngest> {
  if (!streamId || !streamKey) throw new Error("Select a stream with an active key first.");
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
    const result = response.data?.resolveIngestEndpoint;
    if (!result || !isSpecificIngest(result.primary)) {
      throw new Error(
        "No specific ingest destination was confirmed. A generic entry is not a node recommendation."
      );
    }
    if (
      resolveOperationalStreamId({ routeParamId: result.metadata?.streamId ?? "" }) !== streamId
    ) {
      throw new Error(
        "The resolver did not confirm the selected stream. Refresh its key and retry."
      );
    }
    const requested = protocol === "WHIP" ? "whip" : protocol === "RTMP" ? "rtmp" : "srt";
    if (protocol && !ingestProtocolUrl(result.primary, requested)) {
      throw new Error("The destination does not advertise the requested protocol.");
    }
    return result;
  } catch {
    if (signal?.aborted) throw new DOMException("Resolution cancelled", "AbortError");
    throw new Error(
      "Ingest resolution failed. No generic address has been substituted. Check access and capacity, then retry."
    );
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", cancel);
  }
}
