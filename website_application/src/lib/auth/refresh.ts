import { browser } from "$app/environment";

const AUTH_URL = import.meta.env.VITE_AUTH_URL || "/auth";

/**
 * Outcome of a refresh attempt. Only "unauthorized" means the session is
 * definitively dead; "transient" (network failure, 5xx) must never trigger a
 * logout because the cookies in the jar may still be perfectly valid.
 */
export type RefreshResult = "ok" | "unauthorized" | "transient";

let inflight: Promise<RefreshResult> | null = null;

async function postRefresh(fetchFn: typeof globalThis.fetch): Promise<RefreshResult> {
  let response: Response;
  try {
    response = await fetchFn(`${AUTH_URL}/refresh`, {
      method: "POST",
      credentials: "include",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
    });
  } catch {
    return "transient";
  }

  if (response.status === 400 || response.status === 401) {
    return "unauthorized";
  }
  if (!response.ok) {
    return "transient";
  }

  try {
    const payload = (await response.json()) as { user?: unknown };
    if (payload.user && typeof payload.user === "object") {
      localStorage.setItem("user", JSON.stringify(payload.user));
    }
  } catch {
    // The rotated cookies are already set; a body parse failure is not fatal.
  }
  return "ok";
}

/**
 * Single-flight session refresh shared by the auth store and the Houdini
 * client. Within a tab, concurrent callers share one request; across tabs,
 * the Web Locks API serializes refreshes so two tabs can't race the rotation
 * of the shared refresh-token cookie.
 */
export async function refreshAuthSession(
  fetchFn: typeof globalThis.fetch = globalThis.fetch
): Promise<RefreshResult> {
  if (!browser) {
    return "transient";
  }

  inflight ??= (async () => {
    try {
      if (typeof navigator !== "undefined" && navigator.locks?.request) {
        return await navigator.locks.request("fw-auth-refresh", () => postRefresh(fetchFn));
      }
      return await postRefresh(fetchFn);
    } catch {
      return "transient";
    } finally {
      inflight = null;
    }
  })();

  return inflight;
}
