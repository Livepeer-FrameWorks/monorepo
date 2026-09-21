import {
  PaymentRequiredError,
  SchemaMismatchError,
  ServerTooOldError,
  UnsupportedOperationError,
} from "./errors.js";

/** What the client learned from the server's serverInfo. */
export interface ServerStatus {
  /** The server's release version, or null when the server predates serverInfo. */
  version: string | null;
  /** Shipped platform features the server reports. */
  features: ReadonlyArray<string>;
  /**
   * True when version is a stable release (vMAJOR.MINOR.PATCH) the client can
   * compare. Development builds, git-describe builds, and release candidates
   * are unverified and pass every check.
   */
  verified: boolean;
}

interface Semver {
  major: number;
  minor: number;
  patch: number;
}

const stableVersion = /^v?(\d+)\.(\d+)\.(\d+)$/;

export function parseStableVersion(version: string): Semver | null {
  const m = stableVersion.exec(version.trim());
  if (!m) {
    return null;
  }
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]) };
}

export function compareVersions(a: Semver, b: Semver): number {
  return a.major - b.major || a.minor - b.minor || a.patch - b.patch;
}

export function serverStatus(version: string, features: ReadonlyArray<string>): ServerStatus {
  return { version, features, verified: parseStableVersion(version) !== null };
}

/** Throws ServerTooOldError when a verified server is older than minimum. */
export function checkMinimum(status: ServerStatus, minimum: string): void {
  if (status.version === null) {
    throw new ServerTooOldError(null, minimum);
  }
  const have = parseStableVersion(status.version);
  const need = parseStableVersion(minimum);
  if (have && need && compareVersions(have, need) < 0) {
    throw new ServerTooOldError(status.version, minimum);
  }
}

/** Throws UnsupportedOperationError when a verified server is older than the operation. */
export function checkOperation(
  status: ServerStatus,
  operation: string,
  since: string | undefined
): void {
  if (!since || !status.verified || status.version === null) {
    return;
  }
  const have = parseStableVersion(status.version);
  const need = parseStableVersion(since);
  if (have && need && compareVersions(have, need) < 0) {
    throw new UnsupportedOperationError(operation, since, status.version);
  }
}

/**
 * The probe result that settles a server as too old without a version: the
 * server rejected the serverInfo query itself.
 */
export function predatesServerInfo(err: unknown): boolean {
  return err instanceof SchemaMismatchError;
}

/** How long a probe answer is reused before the server is asked again. */
export const probeTtlMs = 5 * 60 * 1000;

/**
 * A 402 answer to serverInfo is cached like an answer, though it is no
 * verdict: a v0.3.10 gateway answers an anonymous serverInfo with its x402
 * challenge because the field is not on its public allowlist, and asking it
 * again on every call would add a round trip that answers the same.
 */
export function cachedFailure(err: unknown): boolean {
  return err instanceof PaymentRequiredError;
}

interface ProbeEntry {
  pending: Promise<ServerStatus>;
  /** When the probe settled; null while it runs. */
  settledAt: number | null;
}

export interface Probe {
  pending: Promise<ServerStatus>;
  /** True when the probe was running or started for this call, not reused from the cache. */
  fresh: boolean;
}

/**
 * Probe results by GraphQL URL. An answer (and a 402) is reused for
 * probeTtlMs; a probe that failed for any other reason is dropped so the next
 * call probes again.
 */
export interface ProbeCache {
  /** The cached probe of url, or a new one started with run. */
  probe(url: string, run: () => Promise<ServerStatus>): Probe;
  /**
   * Drops the cached probe of url, so the next call asks the server again.
   * With pending, only that probe is dropped, not one a concurrent call
   * already replaced it with.
   */
  forget(url: string, pending?: Promise<ServerStatus>): void;
  /** Drops every cached probe. */
  clear(): void;
}

export function createProbeCache(): ProbeCache {
  const probes = new Map<string, ProbeEntry>();
  return {
    probe(url, run) {
      const existing = probes.get(url);
      if (existing) {
        if (existing.settledAt === null) {
          return { pending: existing.pending, fresh: true };
        }
        if (Date.now() - existing.settledAt < probeTtlMs) {
          return { pending: existing.pending, fresh: false };
        }
      }
      const entry: ProbeEntry = { pending: run(), settledAt: null };
      probes.set(url, entry);
      entry.pending.then(
        () => {
          entry.settledAt = Date.now();
        },
        (err: unknown) => {
          if (cachedFailure(err)) {
            entry.settledAt = Date.now();
          } else if (probes.get(url) === entry) {
            probes.delete(url);
          }
        }
      );
      return { pending: entry.pending, fresh: true };
    },
    forget(url, pending) {
      if (pending === undefined || probes.get(url)?.pending === pending) {
        probes.delete(url);
      }
    },
    clear() {
      probes.clear();
    },
  };
}

/**
 * Probes url and applies check to the answer. A probe that fails (or a 402)
 * is no verdict and resolves null. A cached answer that fails check with a
 * version verdict is asked again first, so an upgraded server is seen at
 * once; a fresh answer's verdict is thrown.
 */
export async function gateOnProbe(
  cache: ProbeCache,
  url: string,
  run: () => Promise<ServerStatus>,
  check: (status: ServerStatus) => void,
  isVersionVerdict: (err: unknown) => boolean
): Promise<ServerStatus | null> {
  const current = cache.probe(url, run);
  let status: ServerStatus;
  try {
    status = await current.pending;
  } catch {
    return null;
  }
  try {
    check(status);
  } catch (err) {
    if (current.fresh || !isVersionVerdict(err)) {
      throw err;
    }
    cache.forget(url, current.pending);
    return reprobeAndCheck(cache, url, run, check);
  }
  return status;
}

/**
 * Probes url (reusing a cached answer) and throws the verdict when the
 * answer fails check. A probe that fails is no verdict and resolves null.
 */
export async function reprobeAndCheck(
  cache: ProbeCache,
  url: string,
  run: () => Promise<ServerStatus>,
  check: (status: ServerStatus) => void
): Promise<ServerStatus | null> {
  let status: ServerStatus;
  try {
    status = await cache.probe(url, run).pending;
  } catch {
    return null;
  }
  check(status);
  return status;
}
