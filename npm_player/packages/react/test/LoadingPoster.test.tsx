import { beforeEach, describe, expect, it } from "vitest";
import React from "react";
import { render, waitFor } from "@testing-library/react";
import { LoadingPoster } from "../src/components/LoadingPoster";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

function installImageStub(opts: { naturalWidth: number; naturalHeight: number; fail?: boolean }) {
  class FakeImage {
    naturalWidth = opts.naturalWidth;
    naturalHeight = opts.naturalHeight;
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(v: string) {
      this._src = v;
      Promise.resolve().then(() => {
        if (opts.fail) this.onerror?.();
        else this.onload?.();
      });
    }
    get src() {
      return this._src;
    }
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).Image = FakeImage as any;
}

beforeEach(() => {
  installImageStub({ naturalWidth: 320, naturalHeight: 180 });
});

const measured10x10: LoadingPosterInfo = {
  mode: "animate",
  geometry: "measured",
  spriteJpgUrl: "https://chandler.example/assets/k/sprite.jpg",
  staticUrl: "https://chandler.example/assets/k/poster.jpg",
  staticSource: "chandler-poster",
  generation: 1,
  cues: Array.from({ length: 4 }, (_, i) => ({
    x: (i % 2) * 160,
    y: Math.floor(i / 2) * 90,
    width: 160,
    height: 90,
    startTime: i,
    endTime: i + 1,
  })),
  tileWidth: 160,
  tileHeight: 90,
  columns: 2,
  rows: 2,
  spriteWidth: 320,
  spriteHeight: 180,
};

const syntheticNoGeometry: LoadingPosterInfo = {
  mode: "animate",
  geometry: "synthetic",
  spriteJpgUrl: "https://chandler.example/assets/k/sprite.jpg",
  staticUrl: "https://chandler.example/assets/k/poster.jpg",
  staticSource: "chandler-poster",
  generation: 1,
  cues: [],
  tileWidth: 0,
  tileHeight: 0,
  columns: 0,
  rows: 0,
  spriteWidth: 0,
  spriteHeight: 0,
};

const staticOnly: LoadingPosterInfo = {
  mode: "static",
  generation: 1,
  cues: [],
  columns: 0,
  rows: 0,
  tileWidth: 0,
  tileHeight: 0,
  spriteWidth: 0,
  spriteHeight: 0,
  staticUrl: "https://chandler.example/assets/k/poster.jpg",
  staticSource: "chandler-poster",
};

describe("LoadingPoster", () => {
  it("renders nothing when spec is null", () => {
    const { container } = render(<LoadingPoster loadingPoster={null} />);
    expect(container.firstChild).toBeNull();
  });

  it("animate-measured: renders sprite svg with viewBox per current cue", async () => {
    const { container } = render(<LoadingPoster loadingPoster={measured10x10} />);
    await waitFor(() => {
      expect(container.querySelector("svg")).toBeTruthy();
    });
    expect((container.firstChild as HTMLElement).style.backgroundColor).toBe("rgb(0, 0, 0)");
    const svg = container.querySelector("svg") as SVGSVGElement;
    expect(svg).toBeTruthy();
    expect(svg.getAttribute("preserveAspectRatio")).toBe("xMidYMid meet");
    // First cue (0,0,160,90)
    expect(svg.getAttribute("viewBox")).toBe("0 0 160 90");
    const image = svg.querySelector("image") as SVGImageElement;
    expect(image.getAttribute("href")).toContain("sprite.jpg");
    expect(image.getAttribute("preserveAspectRatio")).toBe("none");
    expect(image.getAttribute("width")).toBe("320");
    expect(image.getAttribute("height")).toBe("180");
    expect(svg.querySelector("clipPath rect")?.getAttribute("width")).toBe("160");
    expect(svg.querySelector("g")?.getAttribute("clip-path")).toContain("fw-loading-poster-clip-");
  });

  it("animate-measured: crops non-zero tiles by translating the image behind a tile viewBox", async () => {
    const secondTile: LoadingPosterInfo = {
      ...measured10x10,
      cues: [measured10x10.cues[1]],
    };

    const { container } = render(<LoadingPoster loadingPoster={secondTile} />);
    await waitFor(() => {
      expect(container.querySelector("svg")).toBeTruthy();
    });
    const svg = container.querySelector("svg") as SVGSVGElement;
    expect(svg.getAttribute("viewBox")).toBe("0 0 160 90");

    const image = svg.querySelector("image") as SVGImageElement;
    expect(image.getAttribute("x")).toBe("-160");
    expect(image.getAttribute("y")).toBe("0");
    expect(image.getAttribute("width")).toBe("320");
    expect(image.getAttribute("height")).toBe("180");
  });

  it("animate-measured: sizes the sheet from natural image dimensions, not cue extents", async () => {
    installImageStub({ naturalWidth: 1200, naturalHeight: 900 });
    const partialGrid: LoadingPosterInfo = {
      ...measured10x10,
      spriteWidth: 120,
      spriteHeight: 90,
      columns: 1,
      rows: 1,
      cues: [{ x: 0, y: 0, width: 120, height: 90, startTime: 0, endTime: 1 }],
    };

    const { container } = render(<LoadingPoster loadingPoster={partialGrid} />);
    await waitFor(() => {
      expect(container.querySelector("svg")).toBeTruthy();
    });
    const image = container.querySelector("image") as SVGImageElement;
    expect(image.getAttribute("width")).toBe("1200");
    expect(image.getAttribute("height")).toBe("900");
  });

  it("animate-synthetic: renders static fallback while sprite is loading", () => {
    // Image stub fires onload on a microtask; first synchronous render shows fallback.
    const { container } = render(<LoadingPoster loadingPoster={syntheticNoGeometry} />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.src).toContain("poster.jpg");
    expect(container.querySelector("svg")).toBeNull();
  });

  it("animate-synthetic: does not invent sprite crop geometry without VTT cues", () => {
    const { container } = render(<LoadingPoster loadingPoster={syntheticNoGeometry} />);
    expect(container.querySelector("svg")).toBeNull();
    expect((container.querySelector("img") as HTMLImageElement).src).toContain("poster.jpg");
  });

  it("animate-synthetic: uses the static fallback while waiting for VTT cues", () => {
    const { container } = render(<LoadingPoster loadingPoster={syntheticNoGeometry} />);
    expect(container.querySelector("svg")).toBeNull();
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.src).toContain("poster.jpg");
  });

  it("static: renders <img> with object-fit:contain and cache-bust _g param", () => {
    const { container } = render(<LoadingPoster loadingPoster={staticOnly} />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.style.objectFit).toBe("contain");
    expect((container.firstChild as HTMLElement).style.backgroundColor).toBe("rgb(0, 0, 0)");
    expect(img.src).toMatch(/_g=1/);
  });

  it("static: skips _g cache-bust for data: URLs", () => {
    const dataSpec: LoadingPosterInfo = {
      ...staticOnly,
      staticUrl: "data:image/jpeg;base64,abc",
    };
    const { container } = render(<LoadingPoster loadingPoster={dataSpec} />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img.src).not.toMatch(/_g=/);
  });

  it("static: skips _g cache-bust for blob: URLs", () => {
    const blobSpec: LoadingPosterInfo = {
      ...staticOnly,
      staticUrl: "blob:https://example.com/abc",
    };
    const { container } = render(<LoadingPoster loadingPoster={blobSpec} />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img.src).not.toMatch(/_g=/);
  });

  it("static: skips _g cache-bust for thumbnail-prop staticSource (user URL)", () => {
    const userSpec: LoadingPosterInfo = {
      ...staticOnly,
      staticUrl: "https://user.example/thumb.jpg",
      staticSource: "thumbnail-prop",
    };
    const { container } = render(<LoadingPoster loadingPoster={userSpec} />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img.src).not.toMatch(/_g=/);
  });

  it("static: cache-bust _g param updates when generation changes", () => {
    const { container, rerender } = render(<LoadingPoster loadingPoster={staticOnly} />);
    const initialSrc = (container.querySelector("img") as HTMLImageElement).src;
    expect(initialSrc).toMatch(/_g=1/);

    const next: LoadingPosterInfo = { ...staticOnly, generation: 2 };
    rerender(<LoadingPoster loadingPoster={next} />);
    const updatedSrc = (container.querySelector("img") as HTMLImageElement).src;
    expect(updatedSrc).toMatch(/_g=2/);
    expect(updatedSrc).not.toBe(initialSrc);
  });
});
