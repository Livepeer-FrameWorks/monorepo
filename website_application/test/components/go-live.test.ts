import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { readable, writable } from "svelte/store";
import { page } from "$app/state";
import { studioMounts, studioProps } from "./fixtures/streamcrafter-stub";

const streamA = {
  id: "relay-a",
  streamId: "10000000-0000-4000-8000-00000000000a",
  name: "Stream A",
  streamKey: "key-a",
  record: true,
  ingestMode: "PUSH",
};
const streamB = {
  id: "relay-b",
  streamId: "10000000-0000-4000-8000-00000000000b",
  name: "Stream B",
  streamKey: "key-b",
  record: false,
  ingestMode: "PUSH",
};
const pull = { ...streamA, id: "relay-p", streamId: "p", name: "Pull", ingestMode: "PULL" };

const { resolveFetch, replaceState } = vi.hoisted(() => ({
  resolveFetch: vi.fn(),
  replaceState: vi.fn(),
}));

function destinationFor(streamKey: string) {
  const stream = [streamA, streamB].find((s) => s.streamKey === streamKey)!;
  return {
    data: {
      resolveIngestEndpoint: {
        primary: {
          nodeId: `node-${streamKey}`,
          clusterId: "cluster",
          kind: "INGEST_ENDPOINT_KIND_NODE_SPECIFIC",
          baseUrl: `https://${streamKey}.node.example`,
          whipUrl: `https://${streamKey}.node.example/webrtc/${streamKey}`,
          region: "eu",
        },
        metadata: { streamId: stream.streamId },
      },
    },
  };
}

vi.mock("$houdini", () => ({
  GetStreamsConnectionStore: class {
    private store = writable({
      fetching: false,
      data: {
        streamsConnection: { edges: [streamA, streamB, pull].map((node) => ({ node })) },
      },
    });
    subscribe = this.store.subscribe;
    fetch = async () => undefined;
  },
  StreamCoreFieldsStore: class {},
  fragment: (node: unknown) => readable(node),
  ResolveIngestDestinationStore: class {
    fetch = resolveFetch;
  },
}));
vi.mock("@livepeer-frameworks/streamcrafter-svelte", async () => ({
  StreamCrafter: (await import("./fixtures/StreamCrafterStub.svelte")).default,
}));
vi.mock("$app/navigation", () => ({ replaceState, beforeNavigate: vi.fn() }));
vi.mock("$lib/stores/auth", () => ({
  auth: readable({ isAuthenticated: true, user: { tenant_id: "tenant", id: "user" } }),
}));
vi.mock("$lib/stores/theme.svelte", () => ({ themeStore: { playerTheme: "default" } }));
vi.mock("$lib/stores/toast.js", () => ({
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));
vi.mock("$lib/config", () => ({
  getDocsSiteUrl: () => "https://docs.example",
  getMarketingSiteUrl: () => "https://frameworks.example",
}));

const { default: GoLivePage } = await import("../../src/routes/go-live/+page.svelte");

// jsdom lacks the pointer-capture and scrolling calls bits-ui's Select makes.
Element.prototype.hasPointerCapture ??= () => false;
Element.prototype.releasePointerCapture ??= () => undefined;
Element.prototype.scrollIntoView ??= () => undefined;

beforeEach(() => {
  studioMounts.count = 0;
  studioProps.current = {};
  resolveFetch.mockImplementation(async ({ variables }: { variables: { streamKey: string } }) =>
    destinationFor(variables.streamKey)
  );
  setPageUrl("http://localhost/go-live");
});

function setPageUrl(href: string) {
  (page as { url: URL }).url = new URL(href);
}
afterEach(() => cleanup());

async function chooseStream(name: string) {
  const trigger = screen.getByLabelText("Stream to broadcast to");
  await fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
  await fireEvent.click(trigger);
  const option = await screen.findByRole("option", { name });
  await fireEvent.pointerUp(option, { button: 0, pointerType: "mouse" });
  await fireEvent.click(option);
}

describe("Go Live page", () => {
  it("opens the studio for the selected stream without a prepare step", async () => {
    render(GoLivePage);
    await screen.findByTestId("studio");
    expect(screen.queryByText(/Prepare connection/)).toBeNull();
    expect(studioProps.current.whipUrl).toBe("https://key-a.node.example/webrtc/key-a");
    expect(studioProps.current.showProducerPanel).toBeUndefined();
    expect(screen.getByTestId("recording-status").textContent).toContain("Recording on");
  });

  it("selects the stream named by ?stream= and lists only pushed streams", async () => {
    setPageUrl(`http://localhost/go-live?stream=${streamB.streamId}`);
    render(GoLivePage);
    await screen.findByTestId("studio");
    expect(studioProps.current.whipUrl).toBe("https://key-b.node.example/webrtc/key-b");
    expect(screen.getByTestId("recording-status").textContent).toContain("Recording off");
  });

  it("explains an unusable ?stream= link", async () => {
    setPageUrl("http://localhost/go-live?stream=p");
    render(GoLivePage);
    expect(await screen.findByText(/linked stream is not available here/)).toBeTruthy();
  });

  it("keeps the studio mounted across stream switches and connects to the new stream", async () => {
    render(GoLivePage);
    await screen.findByTestId("studio");
    await chooseStream("Stream B");
    await waitFor(() =>
      expect(screen.getByTestId("recording-status").textContent).toContain("Recording off")
    );
    expect(studioMounts.count).toBe(1);
    const resolveWhipUrl = studioProps.current.resolveWhipUrl as (
      signal: AbortSignal
    ) => Promise<string>;
    await expect(resolveWhipUrl(new AbortController().signal)).resolves.toBe(
      "https://key-b.node.example/webrtc/key-b"
    );
    expect(replaceState).toHaveBeenCalledWith(`/go-live?stream=${streamB.streamId}`, undefined);
  });

  it("links a refused key to the stream's keys", async () => {
    resolveFetch.mockResolvedValue({
      data: null,
      errors: [{ message: "permission denied", extensions: { code: "FORBIDDEN" } }],
    });
    render(GoLivePage);
    expect(
      await screen.findByText("This stream or account cannot broadcast right now")
    ).toBeTruthy();
    const link = screen.getByText("Open stream keys").closest("a");
    expect(link?.getAttribute("href")).toBe("/streams/relay-a");
    expect(screen.queryByTestId("studio")).toBeNull();
  });

  it("links a spent prepaid balance to billing", async () => {
    // Foghorn refuses a negative balance with gRPC FailedPrecondition, which
    // the gateway presents as FAILED_PRECONDITION.
    resolveFetch.mockResolvedValue({
      data: null,
      errors: [{ message: "payment required", extensions: { code: "FAILED_PRECONDITION" } }],
    });
    render(GoLivePage);
    expect(await screen.findByText("Your prepaid balance is spent")).toBeTruthy();
    const link = screen.getByText("Open billing").closest("a");
    expect(link?.getAttribute("href")).toBe("/account/billing");
  });

  it("explains a blocked camera from the studio's error", async () => {
    render(GoLivePage);
    await screen.findByTestId("studio");
    const onError = studioProps.current.onError as (message: string) => void;
    onError("Permission request failed: NotAllowedError");
    expect(await screen.findByText("The browser blocked your camera or microphone")).toBeTruthy();
  });

  it("links the Go Live guide", async () => {
    render(GoLivePage);
    await screen.findByTestId("studio");
    const guide = screen.getByText("Go Live guide").closest("a");
    expect(guide?.getAttribute("href")).toBe("https://docs.example/builders/go-live-browser");
  });
});
