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
afterEach(cleanup);

async function choosePreset(label = "no_official") {
  await fireEvent.change(screen.getByLabelText("Start from a preset"), {
    target: { value: label },
  });
  await fireEvent.click(screen.getByRole("button", { name: "Use preset" }));
}

describe("placement editor interactions", () => {
  it("preserves both work tabs and gates one atomic apply on required acknowledgements", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Use preset" });
    await choosePreset();
    await fireEvent.click(screen.getByRole("button", { name: "Ingest" }));
    await choosePreset("my_clusters_only");
    await fireEvent.click(screen.getByRole("button", { name: "Viewer delivery" }));
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
    await fireEvent.input(screen.getByLabelText("Latitude"), { target: { value: "0" } });
    expect(placementAPI.preview).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    expect(screen.getByText(/Enter both coordinates/)).toBeTruthy();
    await fireEvent.input(screen.getByLabelText("Longitude"), { target: { value: "0" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview this draft" }));
    await screen.findByText("Cannot determine a destination");
    expect(screen.getByText(/Partial observations/)).toBeTruthy();
    expect(screen.getByText(/Source path was not evaluated/)).toBeTruthy();
    expect(vi.mocked(placementAPI.preview).mock.calls[0][0].coordinates).toEqual({
      latitude: 0,
      longitude: 0,
    });
    await fireEvent.input(screen.getByLabelText("Longitude"), { target: { value: "1" } });
    expect(screen.getByText("Inputs changed. This preview is stale.")).toBeTruthy();
    expect(placementAPI.preview).toHaveBeenCalledTimes(1);
  });

  it("asks before discarding a dirty draft on navigation", async () => {
    render(MediaPlacementEditor, { scope: { kind: "TENANT" } });
    await screen.findByRole("button", { name: "Use preset" });
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
    await screen.findByRole("button", { name: "Use preset" });
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
    const customize = screen.getByRole("button", { name: "Customize account rules" });
    expect(customize.closest("fieldset")?.disabled).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Review changes" }) as HTMLButtonElement).disabled
    ).toBe(true);
  });

  it("requires explicit preset replacement and offers keyboard-operable move buttons", async () => {
    const onchange = vi.fn();
    render(PlacementRulesEditor, {
      rules: presetRules("my_clusters_first"),
      scope: { kind: "TENANT" },
      features: policy().features,
      onchange,
    });
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
    await fireEvent.click(screen.getByRole("button", { name: "Replace this tab’s draft" }));
    expect(onchange).toHaveBeenCalledTimes(2);
  });

  it("does not present zero-recipient pending rollout as active", () => {
    render(PlacementRollout, { rollout: policy().rollout, revision: "7" });
    expect(screen.getByText("Saved · waiting for enforcement")).toBeTruthy();
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
    expect(screen.getByText(/This stream inherits the requested account policy/)).toBeTruthy();
    expect(
      screen.getByText(/Enforcement of that account revision on this stream has not been confirmed/)
    ).toBeTruthy();
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
    expect(
      screen.getByText(
        /This stream inherits the requested account policy; it has no stream override/
      )
    ).toBeTruthy();
    expect(
      screen.queryByText(
        /Enforcement of that account revision on this stream has not been confirmed/
      )
    ).toBeNull();
  });
});
