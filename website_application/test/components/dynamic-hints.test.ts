import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";
import DynamicHints from "$lib/components/dashboard/DynamicHints.svelte";

afterEach(cleanup);

describe("dashboard hints", () => {
  it("leads with Go Live for a tenant with streams and nothing live", () => {
    render(DynamicHints, { hasStreams: true, hasLiveStreams: false });
    expect(screen.getByText("Go Live from Your Browser")).toBeTruthy();
    expect(
      screen
        .getByText(/Open Go Live/)
        .closest("a")
        ?.getAttribute("href")
    ).toBe("/go-live");
  });

  it("drops the Go Live hint while a stream is live", () => {
    render(DynamicHints, { hasStreams: true, hasLiveStreams: true });
    expect(screen.queryByText("Go Live from Your Browser")).toBeNull();
  });
});
