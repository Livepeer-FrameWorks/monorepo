import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";
import StreamCard from "$lib/components/cards/StreamCard.svelte";

afterEach(cleanup);

it("shows live ingest when analytics are unavailable to the token", () => {
  const stream = {
    id: "stream",
    streamId: "stream",
    name: "Broadcast",
    metrics: null,
    ingest: {
      state: "LIVE" as const,
      isLive: true,
      sessionId: "session",
      connectedAt: null,
      liveSince: null,
      endedAt: null,
      resumableUntil: null,
    },
  };
  render(StreamCard, { stream });
  expect(screen.getByText("Live")).toBeTruthy();
  expect(screen.queryByText("Offline")).toBeNull();
});

it("shows unavailable rather than offline when its cell cannot answer", () => {
  const stream = {
    id: "stream",
    streamId: "stream",
    name: "Broadcast",
    metrics: null,
    ingest: null,
  };
  render(StreamCard, { stream });
  expect(screen.getByText("Unavailable")).toBeTruthy();
  expect(screen.queryByText("Offline")).toBeNull();
});
