import { afterEach, describe, expect, it, vi } from "vitest";
import { WhipClient } from "../src/core/WhipClient";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function peer() {
  return {
    close: vi.fn(),
    addTrack: vi.fn(),
    getTransceivers: () => [],
    createOffer: vi.fn(async () => ({ type: "offer", sdp: "offer" })),
    setLocalDescription: vi.fn(async () => {}),
    setRemoteDescription: vi.fn(async () => {}),
  };
}

describe("WHIP disposal", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("closes locally before a DELETE response and only deletes once", async () => {
    const pending = deferred<Response>();
    const fetcher = vi.fn(() => pending.promise);
    vi.stubGlobal("fetch", fetcher);
    const client = new WhipClient({ whipUrl: "https://whip.example/ingest" });
    const pc = peer();
    Object.assign(client, { peerConnection: pc, resourceUrl: "https://whip.example/session" });
    client.destroy();
    client.destroy();
    expect(pc.close).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledWith("https://whip.example/session", { method: "DELETE" });
    pending.resolve(new Response(null, { status: 204 }));
    await pending.promise;
  });

  it("deletes a late POST resource without reviving a destroyed client", async () => {
    const post = deferred<Response>();
    const posted = deferred<void>();
    const pc = peer();
    vi.stubGlobal(
      "RTCPeerConnection",
      vi.fn(function () {
        return pc;
      })
    );
    const fetcher = vi.fn((_: string, options: RequestInit) => {
      if (options.method === "POST") {
        posted.resolve();
        return post.promise;
      }
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetcher);
    const client = new WhipClient({ whipUrl: "https://whip.example/ingest" });
    const events = vi.fn();
    client.on("stateChange", events);
    const connecting = client.connect({ getTracks: () => [] } as unknown as MediaStream);
    const outcome = expect(connecting).rejects.toMatchObject({ name: "AbortError" });
    await posted.promise;
    client.destroy();
    const count = events.mock.calls.length;
    post.resolve(new Response("answer", { status: 201, headers: { Location: "/sessions/late" } }));
    await outcome;
    expect(fetcher).toHaveBeenLastCalledWith("https://whip.example/sessions/late", {
      method: "DELETE",
    });
    expect(pc.setRemoteDescription).not.toHaveBeenCalled();
    expect(events).toHaveBeenCalledTimes(count);
    expect(client.getPeerConnection()).toBeNull();
  });
});
