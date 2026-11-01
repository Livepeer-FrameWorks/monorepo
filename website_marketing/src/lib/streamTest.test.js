import assert from "node:assert/strict";
import { test } from "node:test";

import { parsePlaybackUrl, parseWhipUrl } from "./streamTest.js";

const expectDirect = (url, protocol) => {
  const parsed = parsePlaybackUrl(url);
  assert.equal(parsed.mistUrl, undefined);
  assert.equal(parsed.endpoints.primary.protocol, protocol);
  assert.equal(parsed.endpoints.primary.url, url);
  assert.deepEqual(parsed.endpoints.primary.outputs[protocol], { protocol, url });
  return parsed;
};

test("recognises direct media URLs by extension", () => {
  expectDirect("https://cdn.example.com/live/index.m3u8", "HLS");
  expectDirect("https://cdn.example.com/live/manifest.mpd", "DASH");
  expectDirect("https://cdn.example.com/clip.mp4", "MP4");
  expectDirect("https://cdn.example.com/clip.WEBM", "WEBM");
});

test("ignores the query string when matching a media extension", () => {
  const parsed = parsePlaybackUrl("https://cdn.example.com/live/index.m3u8?token=abc");
  assert.equal(parsed.endpoints.primary.protocol, "HLS");
  assert.match(parsed.endpoints.primary.url, /token=abc$/);
});

test("treats WHEP URLs as live", () => {
  const parsed = expectDirect("https://edge.example.com/webrtc/my-stream", "WHEP");
  assert.equal(parsed.contentType, "live");
});

test("reads a stream name and base from MistServer URLs", () => {
  for (const path of ["my-stream", "my-stream.html", "json_my-stream.js"]) {
    const parsed = parsePlaybackUrl(`https://mist.example.com:8080/${path}`);
    assert.equal(parsed.mistUrl, "https://mist.example.com:8080");
    assert.equal(parsed.contentId, "my-stream");
    assert.equal(parsed.endpoints, undefined);
  }
});

test("keeps a path prefix on a proxied MistServer URL", () => {
  const parsed = parsePlaybackUrl("https://example.com/mist/my-stream.html");
  assert.equal(parsed.mistUrl, "https://example.com/mist");
  assert.equal(parsed.contentId, "my-stream");
});

test("decodes a wildcard stream name", () => {
  const parsed = parsePlaybackUrl("https://mist.example.com/live%2Bcam1");
  assert.equal(parsed.contentId, "live+cam1");
});

test("explains URLs that cannot play", () => {
  assert.match(parsePlaybackUrl("not a url").error, /complete URL/);
  assert.match(parsePlaybackUrl("rtmp://host/app/key").error, /http/);
  assert.match(parsePlaybackUrl("https://mist.example.com:8080/").error, /stream name/);
});

test("blocks http media on an https page only", () => {
  assert.match(parsePlaybackUrl("http://host/index.m3u8", "https:").error, /https/);
  assert.equal(parsePlaybackUrl("http://host/index.m3u8", "http:").error, undefined);
});

test("validates a WHIP URL", () => {
  const url = "https://edge.example.com/webrtc/key";
  assert.deepEqual(parseWhipUrl(url), { whipUrl: url });
  assert.match(parseWhipUrl("rtmp://host/app").error, /http/);
  assert.match(parseWhipUrl("http://host/webrtc/key", "https:").error, /https/);
});
