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

/**
 * The text "Copy Stream Info" puts on the clipboard. It is meant for pasting
 * into a support chat or an issue, so the WHIP URL in it is masked.
 */
export function formatStreamInfo(info: {
  state: string;
  quality: string;
  sourceCount: number;
  whipUrl?: string | null;
}): string {
  return [
    `Status: ${info.state}`,
    `Quality: ${info.quality}`,
    `Sources: ${info.sourceCount}`,
    info.whipUrl ? `WHIP: ${maskWhipUrl(info.whipUrl)}` : null,
  ]
    .filter(Boolean)
    .join("\n");
}
