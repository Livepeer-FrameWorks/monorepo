const MASK = "•••";

/**
 * The last path segment of a WHIP URL is the stream key, so anything drawn on
 * screen uses this form. A shared screen must not reveal the credential.
 */
export function maskWhipUrl(url: string | null | undefined): string {
  if (!url) return "";
  try {
    const { origin, pathname } = new URL(url);
    const segments = pathname.split("/").filter(Boolean);
    return [origin, ...segments.slice(0, -1), MASK].join("/");
  } catch {
    return MASK;
  }
}
