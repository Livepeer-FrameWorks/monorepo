import "virtual:react-router/inject-hmr-runtime";
import { createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router";
import StreamTest from "../../src/components/pages/StreamTest.jsx";

const report = document.querySelector("output");
const host = document.querySelector("#fixture");
const sleep = (ms) => new Promise((resolve) => originalTimeout(resolve, ms));
const originalTimeout = window.setTimeout.bind(window);
const originalClearTimeout = window.clearTimeout.bind(window);
const originalInterval = window.setInterval.bind(window);
const originalClearInterval = window.clearInterval.bind(window);
const timers = new Set();
const timerOrigins = new Map();
const watchTimer = (id, origin) => {
  // The dev server's HMR heartbeat belongs to the document, not the mounted page.
  if (origin.includes("/@vite/client")) return;
  timers.add(id);
  timerOrigins.set(id, origin);
};
window.setTimeout = (callback, delay, ...args) => {
  const id = originalTimeout(() => {
    timers.delete(id);
    timerOrigins.delete(id);
    callback(...args);
  }, delay);
  watchTimer(id, `timeout ${delay}: ${new Error().stack}`);
  return id;
};
window.clearTimeout = (id) => {
  timers.delete(id);
  timerOrigins.delete(id);
  originalClearTimeout(id);
};
window.setInterval = (callback, delay, ...args) => {
  const id = originalInterval(callback, delay, ...args);
  watchTimer(id, `interval ${delay}: ${new Error().stack}`);
  return id;
};
window.clearInterval = (id) => {
  timers.delete(id);
  timerOrigins.delete(id);
  originalClearInterval(id);
};

const listeners = new Set();
const media = navigator.mediaDevices;
const add = media.addEventListener.bind(media);
const remove = media.removeEventListener.bind(media);
media.addEventListener = (type, listener, options) => {
  if (type === "devicechange") listeners.add(listener);
  add(type, listener, options);
};
media.removeEventListener = (type, listener, options) => {
  if (type === "devicechange") listeners.delete(listener);
  remove(type, listener, options);
};
media.enumerateDevices = async () => [
  {
    kind: "videoinput",
    deviceId: "fixture-camera",
    groupId: "fixture",
    label: "Fixture camera",
  },
];
const tracks = new Set();
const knownTracks = new WeakSet();
const watchStream = (stream) => {
  for (const track of stream.getTracks()) {
    if (knownTracks.has(track)) continue;
    knownTracks.add(track);
    tracks.add(track);
    const stop = track.stop.bind(track);
    track.stop = () => {
      tracks.delete(track);
      stop();
    };
    track.addEventListener("ended", () => tracks.delete(track), { once: true });
  }
  return stream;
};
const captureCanvas = HTMLCanvasElement.prototype.captureStream;
HTMLCanvasElement.prototype.captureStream = function (...args) {
  return watchStream(captureCanvas.apply(this, args));
};
const audioDestination = AudioContext.prototype.createMediaStreamDestination;
AudioContext.prototype.createMediaStreamDestination = function (...args) {
  const destination = audioDestination.apply(this, args);
  watchStream(destination.stream);
  return destination;
};
let pendingPermission;
let deferPermission = false;
const makeStream = () => {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 2;
  canvas.getContext("2d").fillRect(0, 0, 2, 2);
  return canvas.captureStream(1);
};
media.getUserMedia = () =>
  deferPermission
    ? new Promise((resolve) => {
        pendingPermission = resolve;
      })
    : Promise.resolve(makeStream());
Object.defineProperty(window, "VideoEncoder", { configurable: true, value: undefined });
Object.defineProperty(window, "AudioEncoder", { configurable: true, value: undefined });

const peers = new Set();
window.RTCPeerConnection = class {
  connectionState = "new";
  iceConnectionState = "new";
  senders = [];
  constructor() {
    peers.add(this);
  }
  addTrack(track) {
    this.senders.push({
      track,
      getParameters: () => ({ encodings: [] }),
      setParameters: async () => {},
      replaceTrack: async () => {},
    });
  }
  getTransceivers() {
    return [];
  }
  getSenders() {
    return this.senders;
  }
  async createOffer() {
    return { type: "offer", sdp: "fixture-offer" };
  }
  async setLocalDescription() {}
  async setRemoteDescription() {
    this.connectionState = "connected";
    this.iceConnectionState = "connected";
    this.onconnectionstatechange?.();
  }
  async getStats() {
    return new Map();
  }
  close() {
    peers.delete(this);
    this.connectionState = "closed";
  }
};
const originalFetch = window.fetch.bind(window);
const sessions = new Set();
let nextSession = 0;
window.fetch = async (input, options = {}) => {
  const url = new URL(typeof input === "string" ? input : input.url, location.href);
  if (url.pathname.startsWith("/fixture-whip")) {
    if (options.method === "DELETE") {
      sessions.delete(url.href);
      return new Response(null, { status: 204 });
    }
    const session = new URL(`/fixture-whip/session-${++nextSession}`, location.href).href;
    sessions.add(session);
    return new Response("fixture-answer", { status: 201, headers: { Location: session } });
  }
  return originalFetch(input, options);
};
const until = async (check, message) => {
  const end = Date.now() + 5000;
  while (!check()) {
    if (Date.now() > end) throw new Error(typeof message === "function" ? message() : message);
    await sleep(10);
  }
};
const button = (text) =>
  [...host.querySelectorAll("button")].find(
    (element) =>
      element.textContent.trim().toLowerCase() === text.toLowerCase() ||
      element.title.toLowerCase() === text.toLowerCase()
  );
const fillWhip = (value) => {
  const input = host.querySelector('.fwt-publish input[type="url"]');
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
let root;
try {
  await sleep(50);
  const baselineTimers = timers.size;
  for (let cycle = 0; cycle < 10; cycle++) {
    root = createRoot(host);
    root.render(createElement(MemoryRouter, null, createElement(StreamTest)));
    await until(() => button("Publish"), "real tester did not mount");
    button("Publish").click();
    await until(() => host.querySelector('.fwt-publish input[type="url"]'), "publish form missing");
    fillWhip(`${location.origin}/fixture-whip/${cycle}`);
    const renderer = host.querySelector(".fwt-publish select");
    renderer.value = "canvas2d";
    renderer.dispatchEvent(new Event("change", { bubbles: true }));
    await sleep(0);
    button("Open studio").click();
    await until(() => button("Add Camera"), "studio did not mount");
    deferPermission = cycle < 5;
    button("Add Camera").click();
    if (deferPermission) {
      await until(() => pendingPermission, "camera permission did not start");
      root.unmount();
      root = null;
      pendingPermission(makeStream());
      pendingPermission = null;
    } else {
      await until(
        () => button("Go Live") && !button("Go Live").disabled,
        "camera did not become available"
      );
      button("Go Live").click();
      await until(() => sessions.size === 1, "WHIP resource did not open");
      fillWhip(`${location.origin}/fixture-whip/replacement-${cycle}`);
      await sleep(0);
      button("Open studio").click();
      await until(
        () => sessions.size === 0,
        "endpoint replacement did not DELETE old WHIP resource"
      );
      root.unmount();
      root = null;
    }
    await until(
      () =>
        listeners.size === 0 &&
        tracks.size === 0 &&
        peers.size === 0 &&
        sessions.size === 0 &&
        timers.size <= baselineTimers,
      () =>
        `cycle ${cycle}: leaked listeners=${listeners.size} tracks=${tracks.size} peers=${peers.size} sessions=${sessions.size} timers=${timers.size - baselineTimers}`
    );
  }
  report.textContent =
    "10 real StreamTest mount/unmount cycles: late permission grants and active WHIP endpoint replacement returned listeners, tracks, peers, sessions and timers to baseline.";
  report.dataset.status = "pass";
} catch (error) {
  const content = host.textContent;
  root?.unmount();
  report.textContent = `${error.stack}\n${[...timerOrigins.values()].join("\n")}\n${content.slice(-4000)}`;
  report.dataset.status = "fail";
}
