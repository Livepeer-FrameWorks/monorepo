import type { MistStreamSource } from "../types";
import type { StreamSource } from "./PlayerInterface";

export function normalizeMistSourceUrls<T extends MistStreamSource | StreamSource>(
  sources: T[] | undefined,
  mistBaseUrl: string
): T[] | undefined {
  if (!sources) return undefined;
  return sources.map((source) => ({
    ...source,
    url: normalizeMistSourceUrl(source.url, source.type, mistBaseUrl),
  }));
}

export function normalizeMistSourceUrl(
  url: string,
  sourceType: string,
  mistBaseUrl: string
): string {
  if (!url) return url;

  let base: URL;
  try {
    base = new URL(mistBaseUrl);
  } catch {
    return url;
  }

  const type = String(sourceType ?? "");
  const wantsWebSocket = type.startsWith("ws/") || type.startsWith("wss/");
  const baseHttpProtocol = base.protocol === "https:" ? "https:" : "http:";
  const baseWsProtocol = base.protocol === "https:" || base.protocol === "wss:" ? "wss:" : "ws:";
  const targetProtocol = wantsWebSocket ? baseWsProtocol : baseHttpProtocol;

  try {
    const parsed = new URL(url);
    if (parsed.hostname === base.hostname && !parsed.port && base.port) {
      parsed.port = base.port;
    }
    if (wantsWebSocket && (parsed.protocol === "http:" || parsed.protocol === "https:")) {
      parsed.protocol = targetProtocol;
    }
    return parsed.toString();
  } catch {
    try {
      const absolute = new URL(url, `${targetProtocol}//${base.host}`);
      return absolute.toString();
    } catch {
      return url;
    }
  }
}
