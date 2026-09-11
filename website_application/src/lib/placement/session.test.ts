import { beforeEach, describe, expect, it, vi } from "vitest";
import { PlacementSession, type PlacementAPI } from "./session";

// The session persists an unresolved apply to sessionStorage, and these tests
// assert that behaviour. Vitest runs this suite in the node environment, which
// has no Web Storage: newer Node exposes sessionStorage as a global, older Node
// does not, so relying on the runtime makes the suite pass or fail on the
// developer's Node version rather than on the code. Stubbing it follows the
// convention the auth-store tests already use for localStorage.
function installSessionStorage() {
  const values = new Map<string, string>();
  vi.stubGlobal("sessionStorage", {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value);
    },
    removeItem: (key: string) => {
      values.delete(key);
    },
    clear: () => {
      values.clear();
    },
  });
}

beforeEach(installSessionStorage);
import {
  copyRules,
  newGroup,
  presetRules,
  rolloutLabel,
  rolloutNeedsRefresh,
  savedDrafts,
  updatesFor,
  validateDraft,
  type ApplyInput,
  type Change,
  type Policy,
  type Preview,
  type Review,
} from "./model";

function policy(revision = "0", own: ReturnType<typeof presetRules> | null = null): Policy {
  return {
    __typename: "MediaPlacementPolicyState",
    scope: { kind: "TENANT", streamId: null },
    revision,
    parentRevision: "0",
    activeRevision: null,
    activeParentRevision: null,
    verbs: (["INGEST", "SERVE"] as const).map((verb) => ({
      verb,
      ownRules: own,
      inheritedRules: null,
      requestedEffective: { schemaVersion: 1, digest: "digest", layers: [], groups: [] },
    })),
    rollout: {
      status: "PENDING",
      requiredRecipients: 0,
      appliedRecipients: 0,
      pendingRecipients: [],
      existingSessionsRetained: true,
      updatedAt: null,
    },
    actions: {
      canRead: true,
      canPreview: true,
      canManage: true,
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
  } as Policy;
}

function review(): Review {
  return {
    __typename: "MediaPlacementReview",
    reviewToken: "review-token",
    digest: "digest",
    expiresAt: "2030-01-01T00:00:00Z",
    differences: [],
    warnings: [],
    impact: {
      affectedStreams: 0,
      activePublishers: 0,
      complete: false,
      existingSessionsRetained: true,
    },
  } as Review;
}

function change(input: ApplyInput): Change {
  return {
    __typename: "MediaPlacementChange",
    scope: { ...input.scope, streamId: input.scope.streamId ?? null },
    idempotencyKey: input.idempotencyKey,
    revision: "1",
    parentRevision: "0",
    digest: "digest",
    createdAt: "2026-09-07T00:00:00Z",
    rollout: policy().rollout,
  } as Change;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => (resolve = accept));
  return { promise, resolve };
}

function setup(overrides: Partial<PlacementAPI> = {}) {
  const api: PlacementAPI = {
    policy: vi.fn(async () => policy()),
    preview: vi.fn(async () => undefined),
    review: vi.fn(async () => review()),
    apply: vi.fn(async (input) => change(input)),
    change: vi.fn(async () => undefined),
    ...overrides,
  };
  const notify = vi.fn();
  const uuid = vi.fn(() => "stable-key");
  const session = new PlacementSession(api, notify, uuid, () => Date.parse("2026-09-07T00:00:00Z"));
  return { api, session, uuid, notify };
}

async function draft(session: PlacementSession) {
  await session.bind("tenant-a:actor-a", { kind: "TENANT" });
  session.edit("SERVE", presetRules("no_official"));
}

describe("placement form semantics", () => {
  it("distinguishes inherit, allow-empty, and preference-empty", () => {
    const base = policy();
    const drafts = savedDrafts(base);
    expect(updatesFor(base, drafts)).toEqual([]);
    drafts.SERVE = {
      schemaVersion: 1,
      constraints: { allow: { any: [] }, deny: [] },
      preferences: { groups: [] },
    };
    const update = updatesFor(base, drafts)[0];
    expect(update.kind).toBe("SET");
    expect(update.rules?.constraints.allow?.any).toEqual([]);
    expect(update.rules?.preferences?.groups).toEqual([]);
    expect(validateDraft(drafts.SERVE)).toEqual([]);
  });

  it("creates one atomic update list and retains the untouched work tab", () => {
    const base = policy("10");
    const drafts = savedDrafts(base);
    drafts.INGEST = presetRules("my_clusters_only");
    expect(updatesFor(base, drafts).map((update) => update.verb)).toEqual(["INGEST"]);
    drafts.SERVE = presetRules("no_official");
    expect(updatesFor(base, drafts).map((update) => update.verb)).toEqual(["INGEST", "SERVE"]);
  });

  it("strips generated fragment metadata and isolates nested draft arrays", () => {
    const original = presetRules("my_clusters_first");
    const copied = copyRules({ ...original, " $fragments": {} } as typeof original)!;
    copied.preferences!.groups[0].match.classes!.push("PLATFORM_OFFICIAL");
    expect(original.preferences!.groups[0].match.classes).toEqual(["TENANT_PRIVATE"]);
    expect(copied).not.toHaveProperty(" $fragments");
  });

  it("does not invent geography thresholds or implicit paid fallback", () => {
    const first = presetRules("my_clusters_first").preferences!.groups;
    expect(first[0].spillover).toBe("CAPACITY_ONLY");
    expect(first[1].spillover).toBe("NEVER");
    const only = presetRules("my_clusters_only");
    expect(only.constraints.allow?.any[0].classes).toEqual(["TENANT_PRIVATE"]);
    expect(only.preferences!.groups).toHaveLength(1);
  });

  it("checks finite distances, geographic spill limits, duplicate IDs and price basis", () => {
    const rules = presetRules("closest_available");
    const group = rules.preferences!.groups[0];
    group.maxDistanceKm = NaN;
    group.spillover = "GEO_HOLE";
    group.order = "PRICE";
    rules.preferences!.groups.push(newGroup(group.id));
    expect(validateDraft(rules)).toHaveLength(4);
    expect(rolloutLabel("PENDING")).toContain("waiting for enforcement");
  });

  it("refreshes blocked and inherited-unconfirmed rollout without rounding revisions", () => {
    let current: Policy = { ...policy(), rollout: { ...policy().rollout, status: "BLOCKED" } };
    expect(rolloutNeedsRefresh(current)).toBe(true);
    current = { ...current, rollout: { ...current.rollout, status: "NOT_CONFIGURED" } };
    expect(rolloutNeedsRefresh(current)).toBe(false);
    current = {
      ...current,
      scope: { kind: "STREAM", streamId: "stream" },
      parentRevision: "9007199254740993",
      activeParentRevision: "9007199254740992",
    };
    expect(rolloutNeedsRefresh(current)).toBe(true);
    current = { ...current, activeParentRevision: current.parentRevision };
    expect(rolloutNeedsRefresh(current)).toBe(false);
    current = { ...current, rollout: { ...current.rollout, status: "EFFECTIVE" } };
    expect(rolloutNeedsRefresh(current)).toBe(false);
  });
});

describe("placement editor request boundaries", () => {
  // An unconfirmed apply survives a reload on purpose, so it also survives from
  // one test to the next unless the tab's storage is cleared between them.
  beforeEach(() => {
    try {
      sessionStorage.clear();
    } catch {
      // No storage in this environment; nothing to isolate.
    }
  });

  it("discards an old tenant load and clears drafts on identity change", async () => {
    const late = deferred<Policy>();
    const { session, api } = setup();
    vi.mocked(api.policy).mockImplementationOnce(() => late.promise);
    const oldLoad = session.bind("tenant-a:actor-a", { kind: "TENANT" });
    await session.bind("tenant-b:actor-b", { kind: "TENANT" });
    late.resolve(policy("old-tenant"));
    await oldLoad;
    expect(session.state.policy?.revision).toBe("0");
    session.edit("SERVE", presetRules("no_official"));
    await session.bind("", { kind: "TENANT" });
    expect(session.state.policy).toBeNull();
    expect(session.state.drafts.SERVE).toBeNull();
  });

  it("retains both drafts during explicit revision refresh", async () => {
    const { session, api } = setup();
    await draft(session);
    session.edit("INGEST", presetRules("my_clusters_only"));
    vi.mocked(api.policy).mockResolvedValue(policy("2"));
    await session.load(true);
    expect(session.state.policy?.revision).toBe("2");
    expect(session.state.drafts.SERVE?.constraints.deny).toHaveLength(1);
    expect(session.state.drafts.INGEST?.constraints.allow?.any).toHaveLength(1);
    expect(session.dirty).toBe(true);
  });

  it("drops late preview and review responses after editing", async () => {
    const previewWait = deferred<Preview>();
    const reviewWait = deferred<Review>();
    const { session } = setup({
      preview: () => previewWait.promise,
      review: () => reviewWait.promise,
    });
    await draft(session);
    const previewing = session.preview({ verb: "SERVE" });
    const reviewing = session.review();
    session.edit("SERVE", presetRules("my_clusters_only"));
    previewWait.resolve({ __typename: "MediaPlacementPreview" } as Preview);
    reviewWait.resolve(review());
    await Promise.all([previewing, reviewing]);
    expect(session.state.preview).toBeNull();
    expect(session.state.review).toBeNull();
    expect(session.state.phase).toBe("idle");
  });

  it("invalidates preview when coordinates change but retains a rule review", async () => {
    const late = deferred<Preview>();
    const { session } = setup({ preview: () => late.promise });
    await draft(session);
    await session.review();
    const pending = session.preview({ verb: "SERVE" });
    session.invalidatePreview();
    late.resolve({ __typename: "MediaPlacementPreview" } as Preview);
    await pending;
    expect(session.state.preview).toBeNull();
    expect(session.state.review).not.toBeNull();
  });

  it("requires warnings and rejects expired reviews without a write", async () => {
    const reviewed = review();
    const { session, api } = setup({
      review: vi.fn(async () => ({
        ...reviewed,
        warnings: [
          {
            id: "spill",
            severity: "WARNING" as const,
            message: "Costs may change",
            acknowledgementRequired: true,
          },
        ],
      })),
    });
    await draft(session);
    await session.review();
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
    expect(session.state.error).toContain("Acknowledge");
    session.acknowledge("invented", true);
    expect(session.state.acknowledgements).toEqual([]);
    vi.mocked(api.review).mockResolvedValueOnce({ ...reviewed, expiresAt: "2020-01-01T00:00:00Z" });
    await session.review();
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
    expect(session.state.error).toContain("expired");
  });

  it("recovers a lost response with the original key, never a second write", async () => {
    let committed: ApplyInput;
    const { session, api, uuid } = setup({
      apply: vi.fn(async (input) => {
        committed = input;
        throw new Error("lost");
      }),
      change: vi.fn(async () => change(committed)),
    });
    await draft(session);
    await session.review();
    await session.apply();
    expect(api.apply).toHaveBeenCalledTimes(1);
    expect(api.change).toHaveBeenCalledWith({ kind: "TENANT" }, "stable-key");
    expect(uuid).toHaveBeenCalledTimes(1);
    expect(session.state.change?.revision).toBe("1");
    expect(session.state.change?.rollout.status).toBe("PENDING");
    expect(session.state.pending).toBeNull();
  });

  it("freezes uncertain intent and checks before retrying identical bytes", async () => {
    const { session, api, uuid } = setup({ apply: vi.fn(async () => undefined) });
    await draft(session);
    await session.review();
    await session.apply();
    expect(session.state.phase).toBe("uncertain");
    const command = structuredClone(session.state.pending);
    session.edit("SERVE", presetRules("closest_available"));
    session.discard();
    await session.apply();
    expect(session.state.pending).toEqual(command);
    expect(api.apply).toHaveBeenCalledTimes(1);
    vi.mocked(api.change).mockResolvedValueOnce({
      __typename: "NotFoundError",
      message: "Not saved",
    } as never);
    vi.mocked(api.apply).mockImplementationOnce(async (input) => change(input));
    await session.recover(true);
    expect(api.apply).toHaveBeenNthCalledWith(2, command);
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it("blocks repeated click submissions and drops completion after tenant switch", async () => {
    const pending = deferred<Change>();
    const { session, api } = setup({ apply: vi.fn(() => pending.promise) });
    await draft(session);
    await session.review();
    const applying = session.apply();
    await session.apply();
    const command = session.state.pending!;
    expect(api.apply).toHaveBeenCalledTimes(1);
    await session.bind("tenant-b:actor-b", { kind: "TENANT" });
    pending.resolve(change(command));
    await applying;
    expect(session.state.change).toBeNull();
    expect(session.state.drafts.SERVE).toBeNull();
  });

  it("preserves a draft on conflict and requires explicit rebase", async () => {
    const { session, api } = setup({
      review: vi.fn(
        async () =>
          ({
            __typename: "MediaPlacementError",
            code: "REVISION_CONFLICT",
            message: "Changed",
            fields: [],
          }) as never
      ),
    });
    await draft(session);
    await session.review();
    expect(session.state.conflict).toBe(true);
    expect(session.dirty).toBe(true);
    await session.review();
    expect(api.review).toHaveBeenCalledTimes(1);
    await session.load(true);
    expect(session.state.conflict).toBe(false);
    expect(session.dirty).toBe(true);
  });

  it("carries an unconfirmed apply across a reload so the recover handle is not lost", async () => {
    const { session, api } = setup({ apply: async () => undefined });
    await draft(session);
    await session.review();
    session.acknowledge("warn-1", true);
    await session.apply();
    const key = session.state.pending?.idempotencyKey;
    if (!key) throw new Error("apply did not record a pending command");
    expect(session.state.phase).toBe("uncertain");

    // A fresh session is the page after a refresh: same identity, same scope,
    // no memory of the submit.
    const reloaded = new PlacementSession(
      api,
      () => {},
      vi.fn(() => "stable-key"),
      () => Date.parse("2026-09-07T00:00:00Z")
    );
    await reloaded.bind("tenant-a:actor-a", { kind: "TENANT" });
    expect(reloaded.state.pending?.idempotencyKey).toBe(key);
    expect(reloaded.state.phase).toBe("uncertain");

    // And it must be resolvable. The reviewed digest travels with the command,
    // because a restored session holds no review to compare the response
    // against; without it every recover is rejected and the editor stays locked
    // on a change it can never clear, with no way out but closing the tab.
    vi.mocked(api.change).mockResolvedValueOnce(change(reloaded.state.pending!));
    await reloaded.recover();
    expect(reloaded.state.pending).toBeNull();
    expect(reloaded.state.phase).toBe("idle");
    expect(reloaded.locked).toBe(false);
  });

  it("refuses to discard while a conflict stands, so stale rules are never shown as synced", async () => {
    const { session } = setup({
      review: vi.fn(
        async () =>
          ({
            __typename: "MediaPlacementError",
            code: "REVISION_CONFLICT",
            message: "Changed",
            fields: [],
          }) as never
      ),
    });
    await draft(session);
    await session.review();
    expect(session.state.conflict).toBe(true);

    // Discarding here would rebase the draft onto the pre-conflict snapshot and
    // clear the banner, leaving the page claiming to be in sync with a revision
    // it never fetched.
    session.discard();
    expect(session.state.conflict).toBe(true);
    expect(session.dirty).toBe(true);

    // Reloading is the way out; discarding works again afterwards.
    await session.load(true);
    expect(session.state.conflict).toBe(false);
    session.discard();
    expect(session.dirty).toBe(false);
  });

  it("makes a revoked permission read-only without discarding the draft", async () => {
    const { session } = setup({
      review: async () => ({ __typename: "AuthError", message: "Forbidden" }) as never,
    });
    await draft(session);
    await session.review();
    expect(session.state.readOnly).toBe(true);
    const before = session.state.drafts.SERVE;
    session.edit("SERVE", null);
    expect(session.state.drafts.SERVE).toBe(before);
  });
});

describe("recovery handle durability", () => {
  beforeEach(() => sessionStorage.clear());

  it("keeps the handle when the operator's role changes under a live session", async () => {
    const { session, api } = setup({ apply: async () => undefined });
    // Role is part of the bind identity so a permission change re-reads policy,
    // but the recovery handle belongs to the account, not to a mutable attribute.
    await session.bind("tenant-a:actor-a:admin", { kind: "TENANT" }, "tenant-a:actor-a");
    session.edit("SERVE", presetRules("no_official"));
    await session.review();
    session.acknowledge("warn-1", true);
    await session.apply();
    const key = session.state.pending?.idempotencyKey;
    expect(key).toBeTruthy();

    const rerolled = new PlacementSession(
      api,
      () => {},
      vi.fn(() => "stable-key"),
      () => Date.parse("2026-09-07T00:00:00Z")
    );
    await rerolled.bind("tenant-a:actor-a:viewer", { kind: "TENANT" }, "tenant-a:actor-a");
    expect(rerolled.state.pending?.idempotencyKey).toBe(key);
    expect(rerolled.state.phase).toBe("uncertain");
  });

  it("lets the operator abandon a handle no response can ever resolve", async () => {
    const { session, api } = setup({ apply: async () => undefined });
    await draft(session);
    await session.review();
    session.acknowledge("warn-1", true);
    await session.apply();
    expect(session.state.phase).toBe("uncertain");

    // A receipt whose digest never matches: recover can only re-reject it, so
    // without an escape the editor stays locked on it forever, across reloads.
    vi.mocked(api.change).mockResolvedValue({
      ...change(session.state.pending!),
      digest: "some-other-digest",
    } as never);
    await session.recover();
    expect(session.state.pending).not.toBeNull();
    expect(session.locked).toBe(true);

    session.abandon();
    expect(session.state.pending).toBeNull();
    expect(session.locked).toBe(false);
    expect(session.state.error).toContain("still unknown");

    // And it is gone from storage, so a reload does not restore the lock.
    const reloaded = new PlacementSession(
      api,
      () => {},
      vi.fn(() => "stable-key"),
      () => Date.parse("2026-09-07T00:00:00Z")
    );
    await reloaded.bind("tenant-a:actor-a", { kind: "TENANT" });
    expect(reloaded.state.pending).toBeNull();
  });

  it("ignores a stored handle with no reviewed digest instead of locking on it", async () => {
    const { session } = setup();
    sessionStorage.setItem(
      "placement.pending.tenant-a:actor-a.TENANT.",
      JSON.stringify({ command: { idempotencyKey: "orphan", scope: { kind: "TENANT" } } })
    );
    await session.bind("tenant-a:actor-a", { kind: "TENANT" });
    expect(session.state.pending).toBeNull();
    expect(session.locked).toBe(false);
  });
});
