import { describe, expect, it } from "vitest";
import React from "react";
import { render } from "@testing-library/react";
import { LoadingPoster } from "../src/components/LoadingPoster";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

const cuesGrid10x10: LoadingPosterInfo = {
  spriteJpgUrl: "https://chandler.example/assets/k/sprite.jpg",
  posterUrl: "https://chandler.example/assets/k/poster.jpg",
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
};

describe("LoadingPoster", () => {
  it("renders sprite-backed div in animate mode", () => {
    const { container } = render(<LoadingPoster loadingPoster={cuesGrid10x10} mode="animate" />);
    const div = container.querySelector("div") as HTMLDivElement;
    expect(div).toBeTruthy();
    expect(div.style.backgroundImage).toContain("sprite.jpg");
    expect(div.style.backgroundSize).toBe("200% 200%");
  });

  it("never references the sprite in latest mode", () => {
    const { container } = render(<LoadingPoster loadingPoster={cuesGrid10x10} mode="latest" />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.src).toContain("poster.jpg");
    expect(container.querySelector("div")).toBeNull();
  });

  it("falls back to img when cues are empty in animate mode", () => {
    const noCues: LoadingPosterInfo = {
      ...cuesGrid10x10,
      cues: [],
      columns: 0,
      rows: 0,
    };
    const { container } = render(<LoadingPoster loadingPoster={noCues} mode="animate" />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.src).toContain("poster.jpg");
  });

  it("uses live sprite URL from cues when present (not the static one)", () => {
    // Simulate what PlayerController.buildLoadingPosterInfo emits when Mist push
    // is the source: spriteJpgUrl is the blob URL from the latest regen.
    const liveBlob = "blob:https://example/abc-123";
    const info: LoadingPosterInfo = { ...cuesGrid10x10, spriteJpgUrl: liveBlob };
    const { container } = render(<LoadingPoster loadingPoster={info} mode="animate" />);
    const div = container.querySelector("div") as HTMLDivElement;
    expect(div.style.backgroundImage).toContain(liveBlob);
  });

  it("falls back to mistPreviewUrl when no Chandler poster", () => {
    const onlyMist: LoadingPosterInfo = {
      mistPreviewUrl: "https://mist.example/stream.jpg?video=pre",
      generation: 1,
      cues: [],
      tileWidth: 0,
      tileHeight: 0,
      columns: 0,
      rows: 0,
    };
    const { container } = render(<LoadingPoster loadingPoster={onlyMist} mode="latest" />);
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img.src).toContain("video=pre");
  });

  it("cache-busts the static img URL via the generation field", () => {
    const { container, rerender } = render(
      <LoadingPoster loadingPoster={cuesGrid10x10} mode="latest" />
    );
    const initialSrc = (container.querySelector("img") as HTMLImageElement).src;
    expect(initialSrc).toMatch(/_g=1/);

    const next: LoadingPosterInfo = { ...cuesGrid10x10, generation: 2 };
    rerender(<LoadingPoster loadingPoster={next} mode="latest" />);
    const updatedSrc = (container.querySelector("img") as HTMLImageElement).src;
    expect(updatedSrc).toMatch(/_g=2/);
    expect(updatedSrc).not.toBe(initialSrc);
  });

  it("uses fallbackPosterUrl when nothing else is available", () => {
    const { container } = render(
      <LoadingPoster loadingPoster={null} fallbackPosterUrl="https://example.com/fallback.jpg" />
    );
    const img = container.querySelector("img") as HTMLImageElement;
    expect(img.src).toBe("https://example.com/fallback.jpg");
  });

  it("renders nothing when no source", () => {
    const { container } = render(<LoadingPoster loadingPoster={null} />);
    expect(container.firstChild).toBeNull();
  });
});
