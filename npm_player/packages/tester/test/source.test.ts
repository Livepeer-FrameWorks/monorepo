import { describe, expect, it, vi } from "vitest";
import {
  mistIngestUris,
  parsePlaybackUrl,
  parseWhipUrl,
  probeMist,
  readShareQuery,
  resolvePlaySource,
  shareQuery,
} from "../src/source";

const mistReply = (body: unknown) =>
  vi.fn().mockImplementation(async () => new Response(JSON.stringify(body)));
const notMist = () => vi.fn().mockResolvedValue(new Response("", { status: 404 }));
const https = { pageProtocol: "https:" };

describe("parsePlaybackUrl", () => {
  it("recognises direct media URLs by extension", () => {
    for (const [url, protocol] of [
      ["https://cdn.example.com/live/index.m3u8", "HLS"],
      ["https://cdn.example.com/live/manifest.mpd", "DASH"],
      ["https://cdn.example.com/clip.mp4", "MP4"],
      ["https://cdn.example.com/clip.WEBM", "WEBM"],
    ]) {
      expect(parsePlaybackUrl(url)).toMatchObject({ kind: "direct", protocol, url });
    }
  });

  it("ignores the query string when matching an extension", () => {
    expect(parsePlaybackUrl("https://cdn.example.com/live/index.m3u8?token=abc")).toMatchObject({
      kind: "direct",
      protocol: "HLS",
    });
  });

  it("reads the stream name and edge from Mist output URLs", () => {
    const cases: Array<[string, string, string]> = [
      ["https://edge.example.com/hls/real-name/index.m3u8", "HLS", "https://edge.example.com"],
      ["https://edge.example.com/cmaf/real-name/index.mpd", "DASH", "https://edge.example.com"],
      ["https://edge.example.com/cmaf/real-name/index.m3u8", "HLS", "https://edge.example.com"],
      ["https://edge.example.com/dash/real-name/index.mpd", "DASH", "https://edge.example.com"],
      ["https://edge.example.com/webrtc/real-name", "WHEP", "https://edge.example.com"],
      ["https://edge.example.com/real-name.mp4", "MP4", "https://edge.example.com"],
      ["https://example.com/mist/hls/real-name/index.m3u8", "HLS", "https://example.com/mist"],
    ];
    for (const [url, protocol, baseUrl] of cases) {
      expect(parsePlaybackUrl(url)).toEqual({
        kind: "direct",
        url,
        protocol,
        mist: { baseUrl, streamName: "real-name" },
      });
    }
  });

  it("reads Mist stream pages as Mist sources", () => {
    for (const path of ["my-stream", "my-stream.html", "json_my-stream.js", "embed_my-stream.js"]) {
      expect(parsePlaybackUrl(`https://mist.example.com:8080/${path}`)).toEqual({
        kind: "mist",
        baseUrl: "https://mist.example.com:8080",
        streamName: "my-stream",
      });
    }
  });

  it("decodes a wildcard stream name", () => {
    expect(parsePlaybackUrl("https://mist.example.com/live%2Bcam1")).toMatchObject({
      streamName: "live+cam1",
    });
  });

  it("uses the chosen protocol for a URL without an extension", () => {
    expect(parsePlaybackUrl("https://cdn.example.com/playlist", "HLS")).toEqual({
      kind: "direct",
      url: "https://cdn.example.com/playlist",
      protocol: "HLS",
    });
  });

  it("explains URLs that cannot play", () => {
    expect(parsePlaybackUrl("not a url")).toEqual({ error: expect.stringMatching(/complete URL/) });
    expect(parsePlaybackUrl("rtmp://host/app/key")).toEqual({
      error: expect.stringMatching(/http/),
    });
    expect(parsePlaybackUrl("https://mist.example.com:8080/")).toEqual({
      error: expect.stringMatching(/stream name/),
    });
  });
});

describe("resolvePlaySource", () => {
  it("leaves the content type of a live HLS manifest to the player", async () => {
    const resolved = await resolvePlaySource(
      { mode: "url", url: "https://cdn.example.com/live/index.m3u8" },
      { ...https, fetchImpl: notMist() }
    );
    expect(resolved).toMatchObject({ input: { contentId: "pasted-url" } });
    expect("input" in resolved && resolved.input.contentType).toBeUndefined();
    expect("input" in resolved && resolved.input.endpoints?.primary.protocol).toBe("HLS");
  });

  it("leaves the content type of DASH and MP4 to the player and marks WHEP live", async () => {
    const play = (url: string) =>
      resolvePlaySource({ mode: "url", url }, { ...https, fetchImpl: notMist() });
    const dash = await play("https://cdn.example.com/live/manifest.mpd");
    const mp4 = await play("https://cdn.example.com/clip.mp4");
    const whep = await play("https://whep.example.com/webrtc/stream");
    expect("input" in dash && dash.input.contentType).toBeUndefined();
    expect("input" in mp4 && mp4.input.contentType).toBeUndefined();
    expect("input" in whep && whep.input.contentType).toBe("live");
  });

  it("plays a Mist output URL in Mist mode under the stream's real name", async () => {
    const fetchImpl = mistReply({ error: "Stream is offline" });
    const resolved = await resolvePlaySource(
      { mode: "url", url: "https://edge.example.com/hls/real-name/index.m3u8" },
      { ...https, fetchImpl }
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      "https://edge.example.com/json_real-name.js",
      expect.anything()
    );
    expect(resolved).toEqual({
      source: { mode: "url", url: "https://edge.example.com/hls/real-name/index.m3u8" },
      input: {
        contentId: "real-name",
        mistUrl: "https://edge.example.com",
        forceType: "html5/application/vnd.apple.mpegurl",
      },
    });
  });

  it("plays a Mist-looking URL on a host without Mist as plain media named after its path", async () => {
    const resolved = await resolvePlaySource(
      { mode: "url", url: "https://cdn.example.com/hls/show/index.m3u8" },
      { ...https, fetchImpl: notMist() }
    );
    expect(resolved).toMatchObject({
      input: {
        contentId: "show",
        endpoints: {
          primary: { nodeId: "show", url: "https://cdn.example.com/hls/show/index.m3u8" },
        },
      },
    });
    expect("input" in resolved && resolved.input.mistUrl).toBeUndefined();
  });

  it("names a missing Mist stream page instead of playing it", async () => {
    const resolved = await resolvePlaySource(
      { mode: "url", url: "https://example.com/watch" },
      { ...https, fetchImpl: notMist() }
    );
    expect(resolved).toEqual({ error: expect.stringMatching(/No MistServer stream answered/) });
  });

  it("explains that http:// media cannot play on an https page", async () => {
    const resolved = await resolvePlaySource(
      { mode: "url", url: "http://host/index.m3u8" },
      { ...https, fetchImpl: notMist() }
    );
    expect(resolved).toEqual({ error: expect.stringMatching(/HTTPS/), tls: true });
    const mist = await resolvePlaySource(
      { mode: "mist", baseUrl: "mist.local:8080", streamName: "live" },
      https
    );
    expect(mist).toMatchObject({ tls: true });
    const local = await resolvePlaySource(
      { mode: "mist", baseUrl: "localhost:8080", streamName: "live" },
      { pageProtocol: "http:" }
    );
    expect(local).toMatchObject({
      input: { contentId: "live", mistUrl: "http://localhost:8080" },
    });
  });

  it("resolves a playback ID through the gateway with the chosen protocol", async () => {
    const resolved = await resolvePlaySource(
      { mode: "playback", playbackId: " abc ", protocol: "WHEP" },
      { ...https, gatewayUrl: "https://gw.example/graphql" }
    );
    expect(resolved).toMatchObject({
      input: { contentId: "abc", gatewayUrl: "https://gw.example/graphql", viewerProtocol: "WHEP" },
    });
  });
});

describe("probeMist", () => {
  it("accepts Mist JSON, JSONP and Mist errors, and nothing else", async () => {
    expect(await probeMist("https://m", "s", mistReply({ source: [] }))).toBe(true);
    expect(
      await probeMist("https://m", "s", mistReply({ error: "Could not retrieve stream. Sorry." }))
    ).toBe(true);
    const jsonp = vi.fn().mockResolvedValue(new Response('mistCallback({"source":[]});'));
    expect(await probeMist("https://m", "s", jsonp)).toBe(true);
    expect(await probeMist("https://m", "s", notMist())).toBe(false);
    expect(await probeMist("https://m", "s", mistReply({ hello: "world" }))).toBe(false);
    expect(
      await probeMist("https://m", "s", vi.fn().mockResolvedValue(new Response("<html>")))
    ).toBe(false);
    expect(
      await probeMist("https://m", "s", vi.fn().mockRejectedValue(new TypeError("CORS")))
    ).toBe(false);
  });
});

describe("shared links", () => {
  it("round-trips URL, Mist and playback-ID sources", () => {
    const url = {
      mode: "url",
      url: "https://cdn.example.com/live/index.m3u8",
      protocol: "HLS",
    } as const;
    expect(readShareQuery(`?${shareQuery(url)}`)).toEqual(url);
    expect(
      readShareQuery(
        `?${shareQuery({ mode: "mist", baseUrl: "https://mist.example.com/", streamName: "live+a" })}`
      )
    ).toEqual({ mode: "url", url: "https://mist.example.com/live%2Ba", protocol: undefined });
    expect(readShareQuery(`?${shareQuery({ mode: "playback", playbackId: "abc" })}`)).toEqual({
      mode: "playback",
      playbackId: "abc",
      protocol: undefined,
    });
  });

  it("never shares or loads a WHIP URL", () => {
    expect(shareQuery({ mode: "url", url: "https://edge.example.com/whip/secret-key" })).toBeNull();
    expect(readShareQuery("?url=https%3A%2F%2Fedge.example.com%2Fwhip%2Fsecret-key")).toBeNull();
  });

  it("ignores an unknown protocol parameter", () => {
    expect(readShareQuery("?url=https%3A%2F%2Fa%2Fb.m3u8&protocol=FTP")).toEqual({
      mode: "url",
      url: "https://a/b.m3u8",
      protocol: undefined,
    });
  });
});

describe("publish helpers", () => {
  it("validates a WHIP URL", () => {
    expect(parseWhipUrl("https://edge.example.com/webrtc/key")).toEqual({
      whipUrl: "https://edge.example.com/webrtc/key",
    });
    expect(parseWhipUrl("rtmp://host/app")).toEqual({ error: expect.stringMatching(/http/) });
    expect(parseWhipUrl("http://host/webrtc/key", "https:")).toMatchObject({ tls: true });
  });

  it("lists MistServer's default ingest URIs", () => {
    expect(mistIngestUris("localhost:8080/", "live")).toEqual({
      rtmp: "rtmp://localhost:1935/live/live",
      srt: "srt://localhost:8889?streamid=live",
      whip: "http://localhost:8080/webrtc/live",
    });
  });
});
