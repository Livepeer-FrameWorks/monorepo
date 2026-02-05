import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import {
  StreamCrafterProvider,
  useStreamCrafterContext,
} from "../src/context/StreamCrafterContext";

// Mock streamcrafter-core
vi.mock("@livepeer-frameworks/streamcrafter-core", () => ({
  IngestControllerV2: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      destroy: vi.fn(),
      on: vi.fn().mockReturnValue(() => {}),
      getMediaStream: vi.fn().mockReturnValue(null),
      getSources: vi.fn().mockReturnValue([]),
      isWebCodecsActive: vi.fn().mockReturnValue(false),
      getReconnectionManager: vi.fn().mockReturnValue({ getState: () => null }),
      getEncoderManager: vi.fn().mockReturnValue(null),
    });
  }),
  detectCapabilities: vi.fn().mockReturnValue({ recommended: "native" }),
  isWebCodecsEncodingPathSupported: vi.fn().mockReturnValue(false),
}));

describe("StreamCrafterContext", () => {
  it("useStreamCrafterContext throws when used outside provider", () => {
    const Consumer = () => {
      useStreamCrafterContext();
      return <div>should not render</div>;
    };

    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(() => {
      render(<Consumer />);
    }).toThrow("useStreamCrafterContext must be used within a StreamCrafterProvider");

    spy.mockRestore();
  });

  it("StreamCrafterProvider renders children", () => {
    render(
      <StreamCrafterProvider config={{ whipUrl: "https://example.com/whip" }}>
        <div data-testid="child">Hello</div>
      </StreamCrafterProvider>
    );

    expect(screen.getByTestId("child")).toBeTruthy();
    expect(screen.getByText("Hello")).toBeTruthy();
  });

  it("useStreamCrafterContext returns hook value within provider", () => {
    let contextValue: unknown = null;
    const Consumer = () => {
      contextValue = useStreamCrafterContext();
      return <div data-testid="consumer">got context</div>;
    };

    render(
      <StreamCrafterProvider config={{ whipUrl: "https://example.com/whip" }}>
        <Consumer />
      </StreamCrafterProvider>
    );

    expect(contextValue).not.toBeNull();
    const ctx = contextValue as Record<string, unknown>;
    expect(ctx.state).toBe("idle");
    expect(typeof ctx.startCamera).toBe("function");
    expect(typeof ctx.startStreaming).toBe("function");
    expect(typeof ctx.stopStreaming).toBe("function");
  });
});
