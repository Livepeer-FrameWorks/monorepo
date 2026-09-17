import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
import PlacementRulesEditor from "$lib/components/placement/PlacementRulesEditor.svelte";
import PlacementRollout from "$lib/components/placement/PlacementRollout.svelte";
import { placementAPI } from "$lib/placement/api";
import { beforeNavigate } from "$app/navigation";
import { auth } from "$lib/stores/auth";
import { presetRules, type Policy, type Review } from "$lib/placement/model";

vi.mock("$houdini", () => ({
  GetMediaPlacementOptionsStore: class {
    fetch = vi.fn();
  },
}));
vi.mock("$lib/placement/api", () => ({
  placementAPI: {
    policy: vi.fn(),
    preview: vi.fn(),
    review: vi.fn(),
    apply: vi.fn(),
    change: vi.fn(),
  },
}));
vi.mock("$lib/stores/auth", async () => {
  const { writable } = await import("svelte/store");
  return {
    auth: writable({
      isAuthenticated: true,
      user: { id: "actor-a", tenant_id: "tenant-a", role: "owner" },
    }),
  };
});

function policy(): Policy {
  return {
    __typename: "MediaPlacementPolicyState",
    scope: { kind: "TENANT", streamId: null },
    revision: "0",
    parentRevision: "0",
    activeRevision: null,
    activeParentRevision: null,
    verbs: (["INGEST", "SERVE"] as const).map((verb) => ({
      verb,
      ownRules: null,
      inheritedRules: null,
      requestedEffective: { schemaVersion: 1, digest: "draft", layers: [], groups: [] },
    })),
    rollout: {
      status: "PENDING",
      appliedRecipients: 0,
      requiredRecipients: 0,
      pendingRecipients: [],
      updatedAt: null,
      existingSessionsRetained: true,
    },
    actions: {
      canRead: true,
      canManage: true,
      canPreview: true,
      canInspectPrivateCandidates: false,
    },
    features: {
      schemaVersion: 1,
      geographicSpillover: true,
      priceOrdering: false,
      supportedPresets: [
        "closest_available",
        "my_clusters_first",
        "my_clusters_only",
        "no_official",
      ],
    },
  };
}

function review(): Review {
  return {
    __typename: "MediaPlacementReview",
    reviewToken: "review",
    digest: "digest",
    expiresAt: "2030-01-01T00:00:00Z",
    differences: [
      {
        path: "serve.constraints.deny",
        label: "Viewer restrictions",
        before: "No deny",
        after: "Official denied",
      },
    ],
    warnings: [
      {
        id: "deny",
        severity: "WARNING",
        message: "New viewers may have no permitted destination.",
        acknowledgementRequired: true,
      },
    ],
    impact: {
      affectedStreams: 0,
      activePublishers: 0,
      complete: false,
      existingSessionsRetained: true,
    },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(placementAPI.policy).mockResolvedValue(policy());
  vi.mocked(placementAPI.review).mockResolvedValue(review());
  // The production store is read-only to consumers; this test replaces it with a writable fixture.
  (auth as unknown as { set: (value: unknown) => void }).set({
    isAuthenticated: true,
    user: { id: "actor-a", tenant_id: "tenant-a", role: "owner" },
  });
});
afterEach(() => {
  cleanup();
  Object.defineProperty(navigator, "geolocation", { configurable: true, value: undefined });
});

async function choosePreset(label = "no_official") {
  const names: Record<string, string> = {
    closest_available: "Closest available",
    my_clusters_first: "My clusters first",
    my_clusters_only: "My clusters only",
    no_official: "No official capacity",
  };
  await fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${names[label]}`) }));
}

describe("placement editor interactions", () => {
  it.each([
    "5eedfeed-11fe-ca57-feed-11feca570001",
    "U3RyZWFtOjVlZWRmZWVkLTExZmUtY2E1Ny1mZWVkLTExZmVjYTU3MDAwMQ==",
  ])("normalizes an entered stream ID for preview: %s", async (id) => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Preview this draft" });
    await fireEvent.input(screen.getByLabelText("Owned stream ID (optional)"), {
      target: { value: ` ${id} ` },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    expect(placementAPI.preview).toHaveBeenCalledWith(
      expect.objectContaining({ streamId: "5eedfeed-11fe-ca57-feed-11feca570001" })
    );
  });

  it("rejects an invalid stream ID instead of silently doing a capacity-only preview", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Preview this draft" });
    await fireEvent.input(screen.getByLabelText("Owned stream ID (optional)"), {
      target: { value: "not-a-stream-id" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    expect(screen.getByText(/Enter a stream UUID or its Stream ID/)).toBeTruthy();
    expect(placementAPI.preview).not.toHaveBeenCalled();
  });

  it("preserves both work tabs and gates one atomic apply on required acknowledgements", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: /^No official capacity/ });
    await choosePreset();
    await fireEvent.click(screen.getByRole("button", { name: "Publishing" }));
    await choosePreset("my_clusters_only");
    await fireEvent.click(screen.getByRole("button", { name: "Viewers" }));
    expect(screen.getByText(/Deny 1: official clusters/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await screen.findByText("Viewer restrictions");
    expect(
      vi.mocked(placementAPI.review).mock.calls[0][0].updates.map((item) => item.verb)
    ).toEqual(["INGEST", "SERVE"]);
    expect(
      (screen.getByRole("button", { name: "Apply rules" }) as HTMLButtonElement).disabled
    ).toBe(true);
    await fireEvent.click(
      screen.getByRole("checkbox", { name: "New viewers may have no permitted destination." })
    );
    expect(
      (screen.getByRole("button", { name: "Apply rules" }) as HTMLButtonElement).disabled
    ).toBe(false);
    expect(placementAPI.apply).not.toHaveBeenCalled();
    expect(screen.getByText(/Impact assessment is partial/)).toBeTruthy();
  });

  it("shows stale preview after editing and never auto-previews keystrokes", async () => {
    vi.mocked(placementAPI.preview).mockResolvedValue({
      __typename: "MediaPlacementPreview",
      scope: { kind: "TENANT", streamId: null },
      verb: "SERVE",
      revision: "0",
      parentRevision: "0",
      digest: "digest",
      reason: "unknown preferred cell",
      selected: null,
      candidates: [],
      transitions: [],
      observedAt: "2026-09-07T00:00:00Z",
      expiresAt: "2030-01-01T00:00:00Z",
      complete: false,
      sourceEvaluated: false,
      activeIngestClusterId: null,
    });
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Preview this draft" });
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    await screen.findByText("Cannot determine a destination");
    expect(screen.getByText(/Partial observations/)).toBeTruthy();
    expect(screen.getByText(/Source path was not evaluated/)).toBeTruthy();
    expect(vi.mocked(placementAPI.preview).mock.calls[0][0].coordinates).toBeNull();
    expect(screen.queryByLabelText("Latitude")).toBeNull();
    expect(screen.queryByLabelText("Longitude")).toBeNull();
    await fireEvent.change(screen.getByLabelText("Protocol"), { target: { value: "hls" } });
    expect(screen.getByText("Inputs changed. This preview is stale.")).toBeTruthy();
    expect(placementAPI.preview).toHaveBeenCalledTimes(1);
  });

  it("uses browser location for optional distance without exposing coordinate fields", async () => {
    const getCurrentPosition = vi.fn((success: PositionCallback) =>
      success({ coords: { latitude: 52.37, longitude: 4.89 } } as GeolocationPosition)
    );
    Object.defineProperty(navigator, "geolocation", {
      configurable: true,
      value: { getCurrentPosition },
    });
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Use this device" });
    await fireEvent.click(screen.getByRole("button", { name: "Use this device" }));
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    expect(getCurrentPosition).toHaveBeenCalledWith(expect.any(Function), expect.any(Function), {
      enableHighAccuracy: false,
      timeout: 10000,
      maximumAge: 300000,
    });
    expect(vi.mocked(placementAPI.preview).mock.calls[0][0].coordinates).toEqual({
      latitude: 52.37,
      longitude: 4.89,
    });
    expect(screen.queryByLabelText("Latitude")).toBeNull();
    expect(screen.queryByLabelText("Longitude")).toBeNull();
  });

  it("asks before discarding a dirty draft on navigation", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: /^No official capacity/ });
    await choosePreset();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const cancel = vi.fn();
    const handler = vi.mocked(beforeNavigate).mock.calls[0][0];
    handler({ willUnload: false, cancel } as never);
    expect(confirm).toHaveBeenCalled();
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("distinguishes an observed source path from media preparation", async () => {
    vi.mocked(placementAPI.preview).mockResolvedValue({
      __typename: "MediaPlacementPreview",
      scope: { kind: "TENANT", streamId: null },
      verb: "SERVE",
      revision: "0",
      parentRevision: "0",
      digest: "digest",
      reason: "selected",
      selected: {
        clusterId: "us",
        clusterName: "US",
        region: "us",
        nodeId: null,
        groupId: "entitled",
        reason: "selected",
        distanceKm: 0,
        requiresSourcePull: true,
        price: null,
      },
      candidates: [],
      transitions: [],
      observedAt: "2026-09-07T00:00:00Z",
      expiresAt: "2030-01-01T00:00:00Z",
      complete: true,
      sourceEvaluated: true,
      activeIngestClusterId: null,
    });
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Preview this draft" });
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    await screen.findByText("Selected: US");
    expect(screen.getByText(/Source path observed/)).toBeTruthy();
    expect(screen.getByText(/did not reserve capacity or start media/)).toBeTruthy();
    expect(screen.getByText(/Preview did not start one/)).toBeTruthy();
    expect(screen.queryByText(/Source path was not evaluated/)).toBeNull();
    expect(placementAPI.apply).not.toHaveBeenCalled();
  });

  it("does not render prior tenant rules after logout", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: /^No official capacity/ });
    await choosePreset();
    (auth as unknown as { set: (value: unknown) => void }).set({
      isAuthenticated: false,
      user: null,
    });
    await screen.findByText("Sign in to a tenant account to manage placement.");
    await waitFor(() => expect(screen.queryByText(/Deny 1: official clusters/)).toBeNull());
  });

  it("displays read-only rules without an enabled mutation path", async () => {
    const base = policy();
    vi.mocked(placementAPI.policy).mockResolvedValue({
      ...base,
      actions: { ...base.actions, canManage: false },
    });
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByText(/current permissions do not allow changes/);
    const platformDefault = screen.getByRole("button", { name: /^Platform default/ });
    expect(platformDefault.closest("fieldset")?.disabled).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Review changes" }) as HTMLButtonElement).disabled
    ).toBe(true);
  });

  it("requires explicit preset replacement and offers keyboard-operable move buttons", async () => {
    const onchange = vi.fn();
    const customRules = presetRules("my_clusters_first");
    customRules.preferences!.groups[0].maxDistanceKm = 123;
    render(PlacementRulesEditor, {
      rules: customRules,
      scope: { kind: "TENANT" },
      features: policy().features,
      onchange,
    });
    await fireEvent.click(screen.getByText("Advanced rules · Custom"));
    const move = screen.getByRole("button", { name: "Move group 1 down" });
    move.focus();
    expect(document.activeElement).toBe(move);
    await fireEvent.click(move);
    expect(
      onchange.mock.calls[0][0].preferences.groups.map((group: { id: string }) => group.id)
    ).toEqual(["connected-fallback", "my-clusters"]);
    expect(screen.getByText(/Group moved to position 2/)).toBeTruthy();
    await choosePreset("closest_available");
    expect(onchange).toHaveBeenCalledTimes(1);
    await fireEvent.click(screen.getByRole("button", { name: "Replace advanced draft" }));
    expect(onchange).toHaveBeenCalledTimes(2);
  });

  it("keeps policy machinery behind advanced rules", () => {
    render(PlacementRulesEditor, {
      rules: presetRules("my_clusters_first"),
      scope: { kind: "TENANT" },
      features: policy().features,
      onchange: vi.fn(),
    });
    expect(
      screen.getByRole("button", { name: /^My clusters first/ }).getAttribute("aria-pressed")
    ).toBe("true");
    expect(screen.getByText("Advanced rules").closest("details")?.open).toBe(false);
    expect(screen.queryByLabelText("Latitude")).toBeNull();
    expect(screen.queryByLabelText("Longitude")).toBeNull();
  });

  it("keeps managed streams on viewer policy and hides false publisher/preview paths", async () => {
    const base = policy();
    vi.mocked(placementAPI.policy).mockResolvedValue({
      ...base,
      scope: { kind: "STREAM", streamId: "5eedfeed-11fe-ca57-feed-11feca570001" },
    });
    render(MediaPlacementEditor, {
      scope: { kind: "STREAM", streamId: "5eedfeed-11fe-ca57-feed-11feca570001" },
      sourceMode: "MANAGED",
      initialVerb: "INGEST",
    });
    await screen.findByText("Viewer preview is not available for this source yet");
    expect(screen.getByRole("button", { name: "Viewers" }).getAttribute("aria-pressed")).toBe(
      "true"
    );
    expect(screen.queryByRole("button", { name: "Publishing" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Source" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Preview this draft" })).toBeNull();
    expect(screen.getByText(/Saving viewer rules still affects live routing/)).toBeTruthy();
  });

  it("offers pull streams their source rules without a publisher preview", async () => {
    const base = policy();
    vi.mocked(placementAPI.policy).mockResolvedValue({
      ...base,
      scope: { kind: "STREAM", streamId: "5eedfeed-11fe-ca57-feed-11feca570001" },
    });
    render(MediaPlacementEditor, {
      scope: { kind: "STREAM", streamId: "5eedfeed-11fe-ca57-feed-11feca570001" },
      sourceMode: "PULL",
      initialVerb: "INGEST",
    });
    await screen.findByText("Viewer preview is not available for this source yet");
    expect(screen.getByRole("button", { name: "Source" }).getAttribute("aria-pressed")).toBe(
      "true"
    );
    expect(screen.queryByRole("button", { name: "Publishing" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Preview this draft" })).toBeNull();
  });

  it("does not present zero-recipient pending rollout as active", async () => {
    render(PlacementRollout, { rollout: policy().rollout, revision: "7" });
    expect(screen.getByText("Saved · waiting for enforcement")).toBeTruthy();
    await fireEvent.click(screen.getByText("Deployment details"));
    expect(screen.getByText("Enforcement coverage has not been confirmed.")).toBeTruthy();
    expect(screen.queryByText("Effective for new decisions")).toBeNull();
  });

  it("distinguishes inherited account policy from confirmed stream enforcement", () => {
    render(PlacementRollout, {
      rollout: { ...policy().rollout, status: "NOT_CONFIGURED" },
      revision: "0",
      parentRevision: "9007199254740993",
      activeParentRevision: "9007199254740992",
    });
    expect(screen.getByText("No custom rules at this scope")).toBeTruthy();
    expect(screen.getByText(/This stream follows your account policy/)).toBeTruthy();
    expect(screen.getByText(/The newest account change is still rolling out/)).toBeTruthy();
    expect(screen.queryByText("Using default placement")).toBeNull();
    expect(screen.queryByText("Effective for new decisions")).toBeNull();
  });

  it("retains confirmed inherited revision without inventing a stream override", () => {
    render(PlacementRollout, {
      rollout: { ...policy().rollout, status: "NOT_CONFIGURED" },
      revision: "0",
      activeRevision: "0",
      parentRevision: "3",
      activeParentRevision: "3",
    });
    expect(screen.getByText(/This stream follows your account policy/)).toBeTruthy();
    expect(screen.queryByText(/The newest account change is still rolling out/)).toBeNull();
  });
});
