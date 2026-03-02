import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { mockRegisterPlayerFn, mockEnsurePlayersRegistered } = vi.hoisted(() => ({
  mockRegisterPlayerFn: vi.fn(),
  mockEnsurePlayersRegistered: vi.fn(),
}));

vi.mock("../src/core/PlayerRegistry", () => ({
  globalPlayerManager: {
    registerPlayer: mockRegisterPlayerFn,
  },
  ensurePlayersRegistered: mockEnsurePlayersRegistered,
}));

vi.mock("../src/core/PlayerInterface", () => {
  class BasePlayer {}
  return { BasePlayer };
});

import { registerPlayer, type SimplePlayerDefinition } from "../src/vanilla/registerPlayer";

describe("registerPlayer", () => {
  let origDocument: any;

  beforeEach(() => {
    origDocument = (globalThis as any).document;
    (globalThis as any).document = {
      createElement: vi.fn((tag: string) => ({
        tagName: tag.toUpperCase(),
        style: { width: "", height: "", objectFit: "" },
        pause: vi.fn(),
        removeAttribute: vi.fn(),
        load: vi.fn(),
        remove: vi.fn(),
      })),
    };
    mockRegisterPlayerFn.mockClear();
    mockEnsurePlayersRegistered.mockClear();
  });

  afterEach(() => {
    (globalThis as any).document = origDocument;
    vi.restoreAllMocks();
  });

  it("calls ensurePlayersRegistered", () => {
    const def: SimplePlayerDefinition = {
      name: "TestPlayer",
      mimeTypes: ["application/x-test"],
      build: vi.fn(),
    };
    registerPlayer("test", def);
    expect(mockEnsurePlayersRegistered).toHaveBeenCalled();
  });

  it("registers adapter with globalPlayerManager", () => {
    const def: SimplePlayerDefinition = {
      name: "TestPlayer",
      mimeTypes: ["application/x-test"],
      build: vi.fn(),
    };
    registerPlayer("test", def);
    expect(mockRegisterPlayerFn).toHaveBeenCalledTimes(1);
  });

  describe("SimplePlayerAdapter", () => {
    function getAdapter(shortname: string, def: SimplePlayerDefinition) {
      registerPlayer(shortname, def);
      return mockRegisterPlayerFn.mock.calls[mockRegisterPlayerFn.mock.calls.length - 1][0];
    }

    it("has correct capability properties", () => {
      const adapter = getAdapter("myproto", {
        name: "My Protocol Player",
        priority: 5,
        mimeTypes: ["video/x-myproto", "video/x-myproto2"],
        build: vi.fn(),
      });

      expect(adapter.capability.name).toBe("My Protocol Player");
      expect(adapter.capability.shortname).toBe("myproto");
      expect(adapter.capability.priority).toBe(5);
      expect(adapter.capability.mimes).toEqual(["video/x-myproto", "video/x-myproto2"]);
    });

    it("defaults priority to 10", () => {
      const adapter = getAdapter("noprio", {
        name: "NoPrio",
        mimeTypes: ["video/test"],
        build: vi.fn(),
      });

      expect(adapter.capability.priority).toBe(10);
    });

    it("isMimeSupported checks mimeTypes array", () => {
      const adapter = getAdapter("mime", {
        name: "MimeTest",
        mimeTypes: ["video/mp4", "video/webm"],
        build: vi.fn(),
      });

      expect(adapter.isMimeSupported("video/mp4")).toBe(true);
      expect(adapter.isMimeSupported("video/webm")).toBe(true);
      expect(adapter.isMimeSupported("audio/mp3")).toBe(false);
    });

    it("isBrowserSupported delegates to definition", () => {
      const supportCheck = vi.fn().mockReturnValue(false);
      const adapter = getAdapter("support", {
        name: "SupportTest",
        mimeTypes: ["video/test"],
        isBrowserSupported: supportCheck,
        build: vi.fn(),
      });

      expect(adapter.isBrowserSupported("video/test", {}, {})).toBe(false);
      expect(supportCheck).toHaveBeenCalled();
    });

    it("isBrowserSupported returns true when no check provided", () => {
      const adapter = getAdapter("nocheck", {
        name: "NoCheck",
        mimeTypes: ["video/test"],
        build: vi.fn(),
      });

      expect(adapter.isBrowserSupported("video/test", {}, {})).toBe(true);
    });

    it("initialize creates video element and calls build", async () => {
      const buildFn = vi.fn();
      const adapter = getAdapter("init", {
        name: "InitTest",
        mimeTypes: ["video/test"],
        build: buildFn,
      });

      const container = {
        appendChild: vi.fn(),
      };
      const source = { url: "test://stream" };

      const video = await adapter.initialize(container, source, {});

      expect((globalThis as any).document.createElement).toHaveBeenCalledWith("video");
      expect(container.appendChild).toHaveBeenCalled();
      expect(buildFn).toHaveBeenCalledWith(source, video, container);
      expect(video.style.width).toBe("100%");
      expect(video.style.height).toBe("100%");
      expect(video.style.objectFit).toBe("contain");
    });

    it("destroy calls definition.destroy and cleans up video", async () => {
      const destroyFn = vi.fn();
      const adapter = getAdapter("cleanup", {
        name: "CleanupTest",
        mimeTypes: ["video/test"],
        build: vi.fn(),
        destroy: destroyFn,
      });

      const container = { appendChild: vi.fn() };
      const video = await adapter.initialize(container, { url: "test" }, {});

      await adapter.destroy();

      expect(destroyFn).toHaveBeenCalled();
      expect(video.pause).toHaveBeenCalled();
      expect(video.removeAttribute).toHaveBeenCalledWith("src");
      expect(video.load).toHaveBeenCalled();
      expect(video.remove).toHaveBeenCalled();
    });

    it("destroy handles missing destroy function", async () => {
      const adapter = getAdapter("nodestroy", {
        name: "NoDestroy",
        mimeTypes: ["video/test"],
        build: vi.fn(),
      });

      const container = { appendChild: vi.fn() };
      await adapter.initialize(container, { url: "test" }, {});
      await expect(adapter.destroy()).resolves.not.toThrow();
    });

    it("destroy handles no video element", async () => {
      const adapter = getAdapter("novideo", {
        name: "NoVideo",
        mimeTypes: ["video/test"],
        build: vi.fn(),
      });

      // destroy without initialize
      await expect(adapter.destroy()).resolves.not.toThrow();
    });
  });
});
