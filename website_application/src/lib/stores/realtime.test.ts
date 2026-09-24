import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";

type Result = { data?: { tenantEvents?: unknown } | null; errors?: unknown[] | null };

// Minimal stand-in for Houdini's generated subscription store: records listen
// variables and lets the test push results to subscribers.
class FakeTenantEventsStore {
  static instances: FakeTenantEventsStore[] = [];
  listened: Record<string, unknown>[] = [];
  unlistened = 0;
  private subscribers = new Set<(result: Result) => void>();

  constructor() {
    FakeTenantEventsStore.instances.push(this);
  }

  listen(variables: Record<string, unknown>) {
    this.listened.push(variables);
  }

  unlisten() {
    this.unlistened++;
  }

  subscribe(fn: (result: Result) => void) {
    this.subscribers.add(fn);
    return () => this.subscribers.delete(fn);
  }

  emit(result: Result) {
    for (const fn of this.subscribers) fn(result);
  }
}

function clipEvent(id: string, type: string, artifactId: string, extra: Record<string, unknown>) {
  return {
    id,
    type,
    time: "2026-09-19T10:00:00Z",
    subject: `artifacts/${artifactId}`,
    data: {
      __typename: "ClipReady",
      artifact: {
        artifactId,
        kind: "ARTIFACT_KIND_CLIP",
        streamId: "stream-1",
        playbackId: "pb-1",
      },
      ...extra,
    },
  };
}

async function loadRealtime() {
  vi.resetModules();
  FakeTenantEventsStore.instances = [];
  vi.stubGlobal("window", { addEventListener: vi.fn() });
  vi.doMock("$app/environment", () => ({ browser: true }));
  vi.doMock("$houdini", () => ({
    StreamEventsStore: class {},
    ViewerMetricsStreamStore: class {},
    SystemHealthStore: class {},
    TrackListUpdatesStore: class {},
    TenantEventsStore: FakeTenantEventsStore,
  }));
  return import("./realtime");
}

describe("realtime artifact events", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
  });

  it("subscribes to the stream's clip events through tenantEvents with a types filter", async () => {
    const realtime = await loadRealtime();
    const stop = realtime.subscribeToClipEvents("stream-1");

    const store = FakeTenantEventsStore.instances[0];
    expect(store.listened).toEqual([
      { types: ["clip.requested", "clip.ready", "clip.failed"], streamId: "stream-1" },
    ]);

    store.emit({
      data: { tenantEvents: clipEvent("e1", "clip.requested", "clip-a", { durationMs: 30000 }) },
    });
    store.emit({
      data: {
        tenantEvents: clipEvent("e2", "clip.ready", "clip-a", {
          durationMs: 30000,
          sizeBytes: 1234,
        }),
      },
    });

    const events = get(realtime.clipEvents);
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      id: "e2",
      stage: "ready",
      terminal: true,
      artifactId: "clip-a",
      kind: "clips",
      streamId: "stream-1",
      sizeBytes: 1234,
    });
    expect(Object.keys(events[0])).not.toContain("filePath");
    expect(Object.keys(events[0])).not.toContain("nodeId");

    stop();
    expect(store.unlistened).toBe(1);
  });

  it("keeps recording events apart from clip events and ignores other payloads", async () => {
    const realtime = await loadRealtime();
    realtime.subscribeToRecordingEvents("stream-1");
    const store = FakeTenantEventsStore.instances[0];
    expect(store.listened[0]).toEqual({
      types: ["recording.started", "recording.stopped", "recording.ready", "recording.failed"],
      streamId: "stream-1",
    });

    store.emit({
      data: {
        tenantEvents: {
          id: "r1",
          type: "recording.failed",
          time: "2026-09-19T10:00:00Z",
          subject: "artifacts/dvr-a",
          data: {
            __typename: "RecordingFailed",
            artifact: {
              artifactId: "dvr-a",
              kind: "ARTIFACT_KIND_RECORDING",
              streamId: "stream-1",
              playbackId: "",
            },
            reason: "MEDIA_FAILURE_REASON_PROCESSING_FAILED",
          },
        },
      },
    });
    store.emit({
      data: {
        tenantEvents: {
          id: "s1",
          type: "stream.live",
          time: "2026-09-19T10:00:00Z",
          subject: "streams/stream-1",
          data: { __typename: "StreamLive", streamId: "stream-1" },
        },
      },
    });
    store.emit({ errors: [{ message: "boom" }] });

    expect(get(realtime.clipEvents)).toEqual([]);
    const recordings = get(realtime.recordingEvents);
    expect(recordings).toHaveLength(1);
    expect(recordings[0]).toMatchObject({
      artifactId: "dvr-a",
      kind: "dvr",
      stage: "failed",
      terminal: true,
      playbackId: null,
      reason: "MEDIA_FAILURE_REASON_PROCESSING_FAILED",
    });
  });

  it("disconnect unlistens every artifact event subscription", async () => {
    const realtime = await loadRealtime();
    realtime.subscribeToClipEvents("stream-1");
    realtime.subscribeToRecordingEvents("stream-2");
    realtime.disconnectWebSocket();
    expect(FakeTenantEventsStore.instances.map((s) => s.unlistened)).toEqual([1, 1]);
  });
});
