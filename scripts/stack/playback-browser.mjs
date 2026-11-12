import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";

const [base, playbackId] = process.argv.slice(2);
assert(base && playbackId, "usage: playback-browser.mjs <Foghorn origin> <playback id>");
const require = createRequire(new URL("../../website_marketing/package.json", import.meta.url));
const { chromium } = require("playwright");
const hlsPath = require.resolve("hls.js", {
  paths: [new URL("../../npm_player/packages/core/", import.meta.url).pathname],
});
const browser = await chromium.launch({
  headless: true,
  args: ["--autoplay-policy=no-user-gesture-required"],
});
try {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.route("http://customer-player.frameworks.test/", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: '<!doctype html><video id="video" muted autoplay playsinline></video>',
    })
  );
  await page.goto("http://customer-player.frameworks.test/");
  await page.addScriptTag({ content: await readFile(hlsPath, "utf8") });
  await page.evaluate(
    (url) => {
      const video = document.querySelector("video");
      const player = new window.Hls({ maxBufferLength: 6 });
      window.playbackFixture = { player, errors: [], frames: 0 };
      player.on(window.Hls.Events.ERROR, (_event, data) => {
        if (data.fatal) window.playbackFixture.errors.push(data.details);
      });
      const frame = (_now, metadata) => {
        window.playbackFixture.frames = metadata.presentedFrames;
        video.requestVideoFrameCallback(frame);
      };
      video.requestVideoFrameCallback(frame);
      player.attachMedia(video);
      player.loadSource(url);
      player.on(window.Hls.Events.MANIFEST_PARSED, () => video.play());
    },
    `${base}/play/${encodeURIComponent(playbackId)}/hls/index.m3u8`
  );
  await page.waitForFunction(
    () => window.playbackFixture.frames >= 2 || window.playbackFixture.errors.length > 0,
    undefined,
    { timeout: 35_000 }
  );
  const result = await page.evaluate(() => ({
    frames: window.playbackFixture.frames,
    errors: window.playbackFixture.errors,
    currentTime: document.querySelector("video").currentTime,
  }));
  assert.deepEqual(result.errors, [], "external HLS browser reported a fatal network/media error");
  assert(
    result.frames >= 2 && result.currentTime > 0,
    "external browser did not decode live frames"
  );
  console.log(
    JSON.stringify({ origin: "http://customer-player.frameworks.test", base, ...result })
  );
  await page.evaluate(() => {
    window.playbackFixture.player.destroy();
    const video = document.querySelector("video");
    video.pause();
    video.removeAttribute("src");
    video.load();
  });
  await context.close();
} finally {
  await browser.close();
}
