import { browser } from "$app/environment";
import { GetCapabilitiesStore, type GetCapabilities$result } from "$houdini";

// The gates the platform enforces for the signed-in tenant. The server reads
// each section from the service that enforces it; this store only mirrors that
// answer so the UI can show what is available instead of guessing. A section
// the server could not read comes back null and stays null here — callers
// treat null as "unknown", never as "allowed".

type Capabilities = NonNullable<GetCapabilities$result["capabilities"]>;

const query = new GetCapabilitiesStore();

let cached = $state<Capabilities | null>(null);
let fetched = false;

export async function loadCapabilities(force = false): Promise<void> {
  if (!browser || (fetched && !force)) return;
  fetched = true;
  try {
    const resp = await query.fetch({ policy: force ? "NetworkOnly" : "CacheOrNetwork" });
    cached = resp.data?.capabilities ?? null;
  } catch {
    cached = null;
  }
}

/** Drops the reading on sign-out so the next account starts from the server. */
export function clearCapabilities(): void {
  cached = null;
  fetched = false;
}

/** Null while the tenant section has not been read. */
export function getPlatformOperator(): boolean | null {
  return cached?.tenant?.platformOperator ?? null;
}

/**
 * The tier's upper bound on retention in days, or null when the tenant is
 * uncapped or the cap has not been read. Callers use it to bound an input;
 * the server clamps regardless.
 */
export function getRecordingRetentionMaxDays(): number | null {
  const retention = cached?.tenant?.recordingRetention;
  if (!retention?.capped) return null;
  return retention.maxDays ?? null;
}
