import { browser } from "$app/environment";
import { get } from "svelte/store";

/** WebSocket close reason the gateway sends when an operator deactivated the account. */
export const ACCOUNT_DEACTIVATED_CLOSE_REASON = "account deactivated";

/** graphql-transport-ws Forbidden: the connection's credential no longer authenticates. */
export const CLOSE_FORBIDDEN = 4403;

/**
 * Why the gateway ended a signed-in session: "deactivated" when an operator
 * deactivated the account, "expired" when the session could not be refreshed.
 */
export type SessionEnd = "deactivated" | "expired";

const SESSION_END_KEY = "fw-session-end";

let ending: Promise<void> | null = null;

/**
 * Signs the tab out after the gateway refused its session for good, so
 * realtime and HTTP clients stop retrying with a dead credential. The layout
 * then sends the visitor to the login page, which reads the reason.
 * Anonymous visitors are left alone: they have no session to end.
 */
export function endSession(reason: SessionEnd): Promise<void> {
  if (!browser) {
    return Promise.resolve();
  }
  ending ??= (async () => {
    // A dynamic import: the auth store imports the realtime client, which imports this module.
    const { auth } = await import("$lib/stores/auth");
    if (!get(auth).isAuthenticated) {
      return;
    }
    try {
      sessionStorage.setItem(SESSION_END_KEY, reason);
    } catch {
      // Storage can be unavailable; the login page then shows no reason.
    }
    await auth.logout();
  })().finally(() => {
    ending = null;
  });
  return ending;
}

/** Reads and clears the reason the last session ended, if one was recorded. */
export function takeSessionEnd(): SessionEnd | null {
  if (!browser) {
    return null;
  }
  try {
    const reason = sessionStorage.getItem(SESSION_END_KEY);
    sessionStorage.removeItem(SESSION_END_KEY);
    return reason === "deactivated" || reason === "expired" ? reason : null;
  } catch {
    return null;
  }
}
