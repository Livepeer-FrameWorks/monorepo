import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import MediaCapacityConsentEditor from "$lib/components/placement/MediaCapacityConsentEditor.svelte";
import { consentAPI } from "$lib/placement/consent-api";
import {
  ConsentSession,
  consentDirty,
  type Consent,
  type ConsentAPI,
  type ConsentChange,
} from "$lib/placement/consent-session";
import type { Review } from "$lib/placement/model";
import { auth } from "$lib/stores/auth";
import { beforeNavigate } from "$app/navigation";

vi.mock("$lib/placement/consent-api", () => ({
  consentAPI: {
    consent: vi.fn(),
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

function consent(overrides: Partial<Consent> = {}): Consent {
  return {
    __typename: "MediaCapacityConsent",
    clusterId: "eu",
    revision: "9007199254740992",
    allowIngest: true,
    allowServe: true,
    allowExternalSource: true,
    canManage: true,
    rollout: {
      status: "PENDING",
      requiredRecipients: 0,
      appliedRecipients: 0,
      pendingRecipients: [],
      updatedAt: null,
      existingSessionsRetained: true,
    },
    ...overrides,
  };
}
function review(): Review {
  return {
    __typename: "MediaPlacementReview",
    reviewToken: "review-token",
    digest: "digest",
    expiresAt: "2030-01-01T00:00:00Z",
    differences: [
      { path: "allowServe", label: "Serve viewers", before: "Allowed", after: "Denied" },
    ],
    warnings: [
      {
        id: "deny-serve",
        severity: "WARNING",
        message: "New viewers may lose this destination.",
        acknowledgementRequired: true,
      },
    ],
    impact: {
      affectedStreams: 2,
      activePublishers: 1,
      complete: false,
      existingSessionsRetained: true,
    },
  };
}
function change(overrides: Partial<ConsentChange> = {}): ConsentChange {
  return {
    __typename: "MediaCapacityConsentChange",
    clusterId: "eu",
    idempotencyKey: "request-key",
    revision: "9007199254740993",
    digest: "digest",
    createdAt: "2026-09-08T00:00:00Z",
    rollout: consent().rollout,
    ...overrides,
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => {
    resolve = accept;
  });
  return { promise, resolve };
}
function fixture() {
  const api = {
    consent: vi.fn<ConsentAPI["consent"]>().mockResolvedValue(consent()),
    review: vi.fn<ConsentAPI["review"]>().mockResolvedValue(review()),
    apply: vi.fn<ConsentAPI["apply"]>().mockResolvedValue(change()),
    change: vi.fn<ConsentAPI["change"]>().mockResolvedValue(change()),
  };
  const uuid = vi.fn(() => "request-key");
  const session = new ConsentSession(api, vi.fn(), uuid, () => Date.parse("2026-09-08T00:00:00Z"));
  return { session, api, uuid };
}
async function reviewed(session: ConsentSession) {
  await session.bind("tenant-a:actor-a", "eu");
  session.edit("allowServe", false);
  await session.review();
  session.acknowledge("deny-serve", true);
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(consentAPI.consent).mockResolvedValue(consent());
  vi.mocked(consentAPI.review).mockResolvedValue(review());
  (auth as unknown as { set(value: unknown): void }).set({
    isAuthenticated: true,
    user: { id: "actor-a", tenant_id: "tenant-a", role: "owner" },
  });
});
afterEach(cleanup);

describe("capacity consent state machine", () => {
  it("does not regress a committed revision when a refresh returns stale state", async () => {
    const { session, api } = fixture();
    await reviewed(session);
    await session.apply();
    expect(session.state.consent?.revision).toBe("9007199254740993");
    expect(session.state.consent?.allowServe).toBe(false);
    expect(consentDirty(session.state)).toBe(false);
    expect(session.state.error).toMatch(/saved revision is not yet available/);
    expect(api.consent).toHaveBeenCalledTimes(2);
  });

  it("requires review and acknowledgements before applying the exact revision", async () => {
    const { session, api } = fixture();
    await session.bind("actor", "eu");
    session.edit("allowServe", false);
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
    await session.review();
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
    session.acknowledge("deny-serve", true);
    api.consent.mockResolvedValue(consent({ revision: "9007199254740993", allowServe: false }));
    await session.apply();
    expect(api.apply).toHaveBeenCalledWith({
      clusterId: "eu",
      expectedRevision: "9007199254740992",
      allowIngest: true,
      allowServe: false,
      allowExternalSource: true,
      reviewToken: "review-token",
      idempotencyKey: "request-key",
      acknowledgedWarningIds: ["deny-serve"],
    });
    expect(session.state.change?.revision).toBe("9007199254740993");
    expect(consentDirty(session.state)).toBe(false);
  });

  it("invalidates a pending review when the draft changes", async () => {
    const { session, api } = fixture();
    await session.bind("actor", "eu");
    session.edit("allowServe", false);
    const pending = deferred<Review>();
    api.review.mockReturnValue(pending.promise);
    const request = session.review();
    session.edit("allowIngest", false);
    pending.resolve(review());
    await request;
    expect(session.state.review).toBeNull();
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
  });

  it("rejects an expired review without a mutation", async () => {
    const { session, api } = fixture();
    api.review.mockResolvedValue({ ...review(), expiresAt: "2026-09-01T00:00:00Z" });
    await reviewed(session);
    await session.apply();
    expect(api.apply).not.toHaveBeenCalled();
    expect(session.state.review).toBeNull();
  });

  it("recovers a committed write after losing the mutation response", async () => {
    const { session, api, uuid } = fixture();
    await reviewed(session);
    api.apply.mockRejectedValue(new Error("network"));
    api.consent.mockResolvedValue(consent({ revision: "9007199254740993", allowServe: false }));
    await session.apply();
    expect(api.change).toHaveBeenCalledWith("eu", "request-key");
    expect(session.state.pending).toBeNull();
    expect(session.state.change).toEqual(change());
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it("freezes uncertain commands and retries only that command after a missing receipt", async () => {
    const { session, api, uuid } = fixture();
    await reviewed(session);
    api.apply.mockResolvedValue(undefined);
    api.change.mockResolvedValue({ __typename: "NotFoundError", message: "Not found" });
    await session.apply();
    expect(session.state.phase).toBe("uncertain");
    const command = structuredClone(session.state.pending);
    session.edit("allowIngest", false);
    session.discard();
    await session.load();
    await session.review();
    await session.apply();
    expect(api.apply).toHaveBeenCalledTimes(1);
    expect(session.state.draft?.allowIngest).toBe(true);
    api.apply.mockResolvedValue(change());
    await session.recover(true);
    expect(api.apply.mock.calls[1][0]).toEqual(command);
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it.each([
    { clusterId: "us" },
    { idempotencyKey: "another" },
    { digest: "another" },
    { revision: "9007199254740992" },
    { revision: "09007199254740993" },
  ])("does not accept a receipt with mismatched binding %j", async (override) => {
    const { session, api } = fixture();
    await reviewed(session);
    api.apply.mockResolvedValue(change(override));
    await session.apply();
    expect(session.state.phase).toBe("uncertain");
    expect(session.state.pending?.idempotencyKey).toBe("request-key");
    expect(session.state.change).toBeNull();
  });

  it("keeps a conflicted draft but requires a reload and new review", async () => {
    const { session, api } = fixture();
    await reviewed(session);
    api.apply.mockResolvedValue({
      __typename: "MediaPlacementError",
      code: "REVISION_CONFLICT",
      message: "Changed",
      fields: [],
      currentRevision: "9007199254740993",
      conflictParentRevision: null,
      retryAfterSeconds: null,
    });
    await session.apply();
    expect(session.state.conflict).toBe(true);
    expect(session.state.pending).toBeNull();
    expect(session.state.draft?.allowServe).toBe(false);
    api.consent.mockResolvedValue(consent({ revision: "9007199254740993" }));
    await session.load(true);
    expect(session.state.conflict).toBe(false);
    expect(session.state.draft?.allowServe).toBe(false);
    expect(session.state.review).toBeNull();
    await session.review();
    expect(api.review.mock.lastCall?.[0].expectedRevision).toBe("9007199254740993");
  });

  it("ignores previous-identity reads and rejects another cluster's response", async () => {
    const { session, api } = fixture();
    const pending = deferred<Consent>();
    api.consent.mockReturnValueOnce(pending.promise);
    const old = session.bind("old", "eu");
    await session.bind("new", "us");
    expect(session.state.consent).toBeNull();
    pending.resolve(consent());
    await old;
    expect(session.state.consent).toBeNull();
    expect(session.state.error).toMatch(/does not identify this cluster/);
  });

  it("does not accept an old actor's completed write after a tenant switch", async () => {
    const { session, api } = fixture();
    await reviewed(session);
    const pending = deferred<ConsentChange>();
    api.apply.mockReturnValue(pending.promise);
    const request = session.apply();
    await session.bind("other-tenant", "us");
    pending.resolve(change());
    await request;
    expect(session.state.change).toBeNull();
    expect(session.state.pending).toBeNull();
    expect(api.change).not.toHaveBeenCalled();
  });

  it("keeps controls read-only after losing authorization", async () => {
    const { session, api } = fixture();
    await reviewed(session);
    api.apply.mockResolvedValue({ __typename: "AuthError", message: "Owner required" });
    await session.apply();
    expect(session.state.readOnly).toBe(true);
    session.edit("allowIngest", false);
    expect(session.state.draft?.allowIngest).toBe(true);
    expect(session.state.pending).toBeNull();
  });
});

describe("capacity consent controls", () => {
  it("shows permissions, impact, and mandatory warning acknowledgement", async () => {
    render(MediaCapacityConsentEditor, { clusterId: "eu" });
    const serve = await screen.findByRole("checkbox", { name: /^Serve viewers/ });
    await fireEvent.click(serve);
    await fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await screen.findByRole("region", { name: "Capacity permission review" });
    expect(screen.getByText(/Impact coverage is incomplete/)).toBeTruthy();
    const apply = screen.getByRole("button", { name: "Apply permissions" }) as HTMLButtonElement;
    expect(apply.disabled).toBe(true);
    await fireEvent.click(
      screen.getByRole("checkbox", { name: "New viewers may lose this destination." })
    );
    expect(apply.disabled).toBe(false);
    expect(consentAPI.apply).not.toHaveBeenCalled();
    expect(screen.getByText(/active revision not confirmed/)).toBeTruthy();
  });

  it("uses API canManage rather than assuming an owner role can edit", async () => {
    vi.mocked(consentAPI.consent).mockResolvedValue(consent({ canManage: false }));
    render(MediaCapacityConsentEditor, { clusterId: "eu" });
    await screen.findByText(/Read-only/);
    const checkbox = screen.getByRole("checkbox", {
      name: /^Accept publishers/,
    }) as HTMLInputElement;
    expect(checkbox.closest("fieldset")?.disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
  });

  it("guards dirty navigation and clears the draft on logout", async () => {
    render(MediaCapacityConsentEditor, { clusterId: "eu" });
    await fireEvent.click(await screen.findByRole("checkbox", { name: /^Serve viewers/ }));
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const cancel = vi.fn();
    vi.mocked(beforeNavigate).mock.calls[0][0]({ willUnload: false, cancel } as never);
    expect(cancel).toHaveBeenCalled();
    (auth as unknown as { set(value: unknown): void }).set({ isAuthenticated: false, user: null });
    await screen.findByText("Sign in to view capacity permissions.");
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("retains the recovery key and freezes controls on an uncertain save", async () => {
    vi.mocked(consentAPI.apply).mockResolvedValue(undefined);
    vi.mocked(consentAPI.change).mockResolvedValue(undefined);
    render(MediaCapacityConsentEditor, { clusterId: "eu" });
    await fireEvent.click(await screen.findByRole("checkbox", { name: /^Serve viewers/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await fireEvent.click(
      await screen.findByRole("checkbox", { name: "New viewers may lose this destination." })
    );
    await fireEvent.click(screen.getByRole("button", { name: "Apply permissions" }));
    await screen.findByText(/Save outcome is not confirmed/);
    expect(screen.getByText(/Recovery request:/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Apply permissions" })).toBeNull();
    expect(
      (screen.getByRole("button", { name: "Retry same change" }) as HTMLButtonElement).disabled
    ).toBe(false);
  });
});
