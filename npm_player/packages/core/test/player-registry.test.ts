import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const createCapability = (shortname: string) => ({
  name: shortname,
  shortname,
  priority: 1,
  mimes: [],
});

const constructorSpies = {
  native: vi.fn(),
  webcodecs: vi.fn(),
  mistWebrtc: vi.fn(),
  videojs: vi.fn(),
  hlsjs: vi.fn(),
  dashjs: vi.fn(),
  mistLegacy: vi.fn(),
  mews: vi.fn(),
};

class NativePlayerImpl {
  capability = createCapability("native");
  constructor() {
    constructorSpies.native();
  }
}
class WebCodecsPlayerImpl {
  capability = createCapability("webcodecs");
  constructor() {
    constructorSpies.webcodecs();
  }
}
class MistWebRTCPlayerImpl {
  capability = createCapability("mist-webrtc");
  constructor() {
    constructorSpies.mistWebrtc();
  }
}
class VideoJsPlayerImpl {
  capability = createCapability("videojs");
  constructor() {
    constructorSpies.videojs();
  }
}
class HlsJsPlayerImpl {
  capability = createCapability("hlsjs");
  constructor() {
    constructorSpies.hlsjs();
  }
}
class DashJsPlayerImpl {
  capability = createCapability("dashjs");
  constructor() {
    constructorSpies.dashjs();
  }
}
class MistPlayerImpl {
  capability = createCapability("mist-legacy");
  constructor() {
    constructorSpies.mistLegacy();
  }
}
class MewsWsPlayerImpl {
  capability = createCapability("mews");
  constructor() {
    constructorSpies.mews();
  }
}

vi.mock("../src/players/NativePlayer", () => ({ NativePlayerImpl }));
vi.mock("../src/players/WebCodecsPlayer", () => ({ WebCodecsPlayerImpl }));
vi.mock("../src/players/MistWebRTCPlayer", () => ({ MistWebRTCPlayerImpl }));
vi.mock("../src/players/VideoJsPlayer", () => ({ VideoJsPlayerImpl }));
vi.mock("../src/players/HlsJsPlayer", () => ({ HlsJsPlayerImpl }));
vi.mock("../src/players/DashJsPlayer", () => ({ DashJsPlayerImpl }));
vi.mock("../src/players/MistPlayer", () => ({ MistPlayerImpl }));
vi.mock("../src/players/MewsWsPlayer", () => ({ MewsWsPlayerImpl }));

vi.mock("../src/core/PlayerManager", () => {
  class PlayerManager {
    registered: any[] = [];
    initializeCalls: any[] = [];
    options?: any;

    constructor(options?: any) {
      this.options = options;
    }

    registerPlayer(player: any) {
      this.registered.push(player);
    }

    getRegisteredPlayers() {
      return this.registered;
    }

    async initializePlayer(...args: any[]) {
      this.initializeCalls.push(args);
      return "initialized";
    }
  }

  return { PlayerManager };
});

describe("PlayerRegistry", () => {
  let registry: typeof import("../src/core/PlayerRegistry");
  let PlayerManager: typeof import("../src/core/PlayerManager").PlayerManager;

  beforeAll(async () => {
    registry = await import("../src/core/PlayerRegistry");
    ({ PlayerManager } = await import("../src/core/PlayerManager"));
  });

  beforeEach(() => {
    vi.clearAllMocks();
  });
  it("loads matching players before initializePlayer", async () => {
    const manager = new PlayerManager();
    const streamInfo = {
      source: [{ url: "u", type: "html5/video/mp4" }],
      meta: { tracks: [] },
    };

    await manager.initializePlayer({}, streamInfo, {}, {});

    expect(constructorSpies.native).toHaveBeenCalledTimes(1);
    expect(manager.getRegisteredPlayers()).toHaveLength(1);

    await manager.initializePlayer({}, streamInfo, {}, {});
    expect(constructorSpies.native).toHaveBeenCalledTimes(1);
    expect(manager.initializeCalls).toHaveLength(2);

    await registry.ensurePlayersRegistered(manager);
  });

  it("loads forced player by shortname", async () => {
    const manager = new PlayerManager();
    const streamInfo = {
      source: [{ url: "u", type: "mist/legacy" }],
      meta: { tracks: [] },
    };

    await manager.initializePlayer({}, streamInfo, {}, { forcePlayer: "hlsjs" });

    expect(constructorSpies.mistLegacy).toHaveBeenCalledTimes(1);
    expect(constructorSpies.hlsjs).toHaveBeenCalledTimes(1);
  });

  it("deduplicates ensurePlayersRegistered promises", async () => {
    const manager = new PlayerManager();
    const p1 = registry.ensurePlayersRegistered(manager);
    const p2 = registry.ensurePlayersRegistered(manager);

    expect(p1).toBe(p2);
    await p1;
  });

  it("creates managers and exposes capabilities", async () => {
    const manager = registry.createPlayerManager({ debug: true });
    expect(manager).toBeInstanceOf(PlayerManager);
    expect((manager as any).options).toEqual({ debug: true });

    const capabilities = registry.getAvailablePlayerCapabilities();
    expect(capabilities.length).toBeGreaterThan(0);
  });
});
