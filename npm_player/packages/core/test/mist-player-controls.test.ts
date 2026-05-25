// @vitest-environment jsdom

import { afterEach, describe, expect, test, vi } from "vitest";
import { MistPlayerImpl } from "../src/players/MistPlayer";

describe("MistPlayerImpl controls", () => {
  afterEach(() => {
    delete (window as any).mistplayers;
    delete (window as any).mistPlay;
    vi.restoreAllMocks();
  });

  test("passes headless controls state through to MistServer player.js", async () => {
    const player = new MistPlayerImpl();
    const container = document.createElement("div");
    const mistPlay = vi.fn();
    (window as any).mistplayers = {};
    (window as any).mistPlay = mistPlay;

    await player.initialize(
      container,
      {
        type: "mist/legacy",
        url: "https://edge.example/view/demo.html",
        streamName: "demo",
      },
      { controls: false, autoplay: true, muted: true }
    );

    expect(mistPlay).toHaveBeenCalledWith(
      "demo",
      expect.objectContaining({
        controls: false,
        autoplay: true,
        muted: true,
      })
    );
  });
});
