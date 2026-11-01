const DIRECT_FORMATS = [
  [/\.m3u8$/i, "HLS"],
  [/\.mpd$/i, "DASH"],
  [/\.mp4$/i, "MP4"],
  [/\.webm$/i, "WEBM"],
];

const parseUrl = (input, allowedProtocols) => {
  let url;
  try {
    url = new URL(input.trim());
  } catch {
    return { error: "Paste a complete URL, starting with https://." };
  }
  if (!allowedProtocols.includes(url.protocol)) {
    return { error: "Use an http:// or https:// URL." };
  }
  return { url };
};

const insecureOnSecurePage = (url, pageProtocol) =>
  pageProtocol === "https:" && url.protocol === "http:"
    ? { error: "This page runs on https, so the browser blocks http:// media. Use https://." }
    : null;

const directEndpoints = (url, protocol) => {
  const href = url.toString();
  return {
    primary: {
      nodeId: "pasted-url",
      protocol,
      url: href,
      outputs: { [protocol]: { protocol, url: href } },
    },
    fallbacks: [],
  };
};

const directFormat = (pathname) => {
  const format = DIRECT_FORMATS.find(([pattern]) => pattern.test(pathname));
  if (format) return format[1];
  return /\/(webrtc|whep)\//i.test(pathname) ? "WHEP" : null;
};

// Accepts a direct media URL, a WHEP URL, or a MistServer stream page or embed
// URL (https://host:8080/stream, /stream.html or /json_stream.js).
export function parsePlaybackUrl(input, pageProtocol = "https:") {
  const { url, error } = parseUrl(input, ["http:", "https:"]);
  if (error) return { error };

  const blocked = insecureOnSecurePage(url, pageProtocol);
  if (blocked) return blocked;

  const segments = url.pathname.split("/").filter(Boolean);
  const name = segments.at(-1);
  if (!name) {
    return { error: "Add the stream name to the URL, like https://host:8080/my-stream." };
  }

  const protocol = directFormat(url.pathname);
  if (protocol) {
    return {
      contentId: "pasted-url",
      contentType: protocol === "WHEP" ? "live" : "vod",
      endpoints: directEndpoints(url, protocol),
    };
  }

  const prefix = segments.slice(0, -1).join("/");
  const stream = name.replace(/\.html?$/i, "").replace(/^json_(.+)\.js$/i, "$1");
  return {
    contentId: decodeURIComponent(stream),
    contentType: "live",
    mistUrl: prefix ? `${url.origin}/${prefix}` : url.origin,
  };
}

export function parseWhipUrl(input, pageProtocol = "https:") {
  const { url, error } = parseUrl(input, ["http:", "https:"]);
  if (error) return { error };

  const blocked = insecureOnSecurePage(url, pageProtocol);
  if (blocked) return blocked;

  return { whipUrl: url.toString() };
}
