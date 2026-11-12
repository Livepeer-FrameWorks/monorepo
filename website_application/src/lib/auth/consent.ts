export interface AuthorizationPreview {
  client_id: string;
  scope: string;
  device_label?: string;
  client_version?: string;
  request_ip?: string;
  user_agent?: string;
  location?: string;
  requested_at?: string | null;
  expires_at?: string | null;
  redirect_uri?: string;
  code_challenge?: string;
  code_challenge_method?: string;
  state?: string;
}
export function authorizationClientName(client: string): string {
  return client === "cli"
    ? "FrameWorks CLI"
    : client === "tray-mac"
      ? "FrameWorks macOS tray"
      : client;
}
export function normalizeDeviceCode(value: string): string {
  return value
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .slice(0, 8)
    .replace(/^(.{4})(.+)$/, "$1-$2");
}
export function authorizationRemainingSeconds(
  expiresAt: string | null | undefined,
  now: number
): number | null {
  const deadline = expiresAt ? Date.parse(expiresAt) : Number.NaN;
  return Number.isNaN(deadline) ? null : Math.max(0, Math.ceil((deadline - now) / 1000));
}
export function authorizationError(error: unknown, fallback: string): string {
  if (
    error &&
    typeof error === "object" &&
    "response" in error &&
    error.response &&
    typeof error.response === "object" &&
    "data" in error.response &&
    error.response.data &&
    typeof error.response.data === "object" &&
    "error" in error.response.data &&
    typeof error.response.data.error === "string"
  )
    return error.response.data.error;
  return error instanceof Error ? error.message : fallback;
}
export function loopbackAuthorizationRedirect(uri: string): boolean {
  try {
    const url = new URL(uri);
    return (
      url.protocol === "http:" &&
      ["127.0.0.1", "[::1]", "::1"].includes(url.hostname) &&
      url.pathname === "/callback" &&
      !url.username &&
      !url.password
    );
  } catch {
    return false;
  }
}
export function returnToAuthorizationClient(url: URL): void {
  window.location.assign(url.toString());
}
