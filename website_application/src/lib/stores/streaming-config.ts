import { browser } from "$app/environment";
import { GetStreamingConfigStore, type GetStreamingConfig$result } from "$houdini";

export type StreamingConfig = NonNullable<GetStreamingConfig$result["streamingConfig"]>;

const query = new GetStreamingConfigStore();

let cached: StreamingConfig | null = null;
let fetched = false;

export async function loadStreamingConfig(force = false): Promise<void> {
  if (!browser || (fetched && !force)) return;
  fetched = true;
  try {
    const resp = await query.fetch({ policy: force ? "NetworkOnly" : "CacheOrNetwork" });
    cached = resp.data?.streamingConfig ?? null;
  } catch {
    cached = null;
  }
}

export function getStreamingConfig(): StreamingConfig | null {
  return cached;
}
