import {
  copyRules,
  errorMessage,
  savedDrafts,
  updatesFor,
  validateDraft,
  type ApplyInput,
  type Change,
  type ChangeResult,
  type Drafts,
  type Policy,
  type PolicyResult,
  type Preview,
  type PreviewInput,
  type PreviewResult,
  type Review,
  type ReviewInput,
  type ReviewResult,
  type Rules,
  type Scope,
  type Verb,
} from "./model";

// Session storage, not local: an unconfirmed apply belongs to this tab's work,
// and every access is guarded because a private window or blocked site data
// makes the accessor itself throw. Losing the handle is bad; failing the editor
// because storage is unavailable would be worse.
//
// The reviewed digest is stored with the command. Accepting a change verifies
// the response against it, and a restored session no longer holds the review,
// so without it every restored apply would fail that check forever and leave
// the editor locked on a command it could never resolve.
interface CarriedApply {
  command: ApplyInput;
  reviewDigest: string;
}

function readPendingApply(key: string): CarriedApply | null {
  try {
    const raw = sessionStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as CarriedApply;
    // The digest is as load-bearing as the key: without it the restored apply
    // could never be verified, and acceptChange would reject every response
    // forever. An entry missing one is treated as no entry at all.
    return parsed?.command &&
      typeof parsed.command.idempotencyKey === "string" &&
      typeof parsed.reviewDigest === "string" &&
      parsed.reviewDigest.length > 0
      ? parsed
      : null;
  } catch {
    return null;
  }
}

// Reports whether the handle is actually stored. A private window, blocked site
// data or an exhausted quota makes this fail, and the operator is told which of
// the two situations they are in rather than promised a reload will be safe.
function writePendingApply(key: string, carried: CarriedApply | null): boolean {
  try {
    if (carried) sessionStorage.setItem(key, JSON.stringify(carried));
    else sessionStorage.removeItem(key);
    return true;
  } catch {
    // Storage is a convenience here; the in-memory command still drives recover.
    return false;
  }
}

export interface PlacementAPI {
  policy(scope: Scope): Promise<PolicyResult | undefined>;
  preview(input: PreviewInput): Promise<PreviewResult | undefined>;
  review(input: ReviewInput): Promise<ReviewResult | undefined>;
  apply(input: ApplyInput): Promise<ChangeResult | undefined>;
  change(scope: Scope, key: string): Promise<ChangeResult | undefined>;
}

export interface EditorState {
  policy: Policy | null;
  drafts: Drafts;
  preview: Preview | null;
  previewStale: boolean;
  review: Review | null;
  acknowledgements: string[];
  change: Change | null;
  pending: ApplyInput | null;
  // The digest of the review that authorised `pending`, kept separately so it
  // survives a reload after the review itself is gone.
  pendingDigest: string | null;
  // Whether the pending apply actually reached storage. False means the handle
  // lives only in this page, so the UI must not promise it survives a reload.
  pendingPersisted: boolean;
  phase: "idle" | "loading" | "reviewing" | "applying" | "recovering" | "uncertain";
  previewing: boolean;
  error: string;
  conflict: boolean;
  readOnly: boolean;
}

function emptyState(): EditorState {
  return {
    policy: null,
    drafts: { INGEST: null, SERVE: null },
    preview: null,
    previewStale: false,
    review: null,
    acknowledgements: [],
    change: null,
    pending: null,
    pendingDigest: null,
    pendingPersisted: true,
    phase: "idle",
    previewing: false,
    error: "",
    conflict: false,
    readOnly: false,
  };
}

// Requests are scoped to both the authenticated identity and the current draft.
// Mutation uncertainty freezes that exact command until its outcome is checked.
export class PlacementSession {
  state: EditorState = emptyState();
  private epoch = 0;
  private draftVersion = 0;
  private previewSequence = 0;
  private loadSequence = 0;
  private scope: Scope = { kind: "TENANT" };
  private identity = "";
  private account = "";

  constructor(
    private readonly api: PlacementAPI,
    private readonly changed: (state: EditorState) => void,
    private readonly uuid: () => string = () => crypto.randomUUID(),
    private readonly now: () => number = Date.now
  ) {}

  private patch(next: Partial<EditorState>) {
    this.state = { ...this.state, ...next };
    if ("pending" in next) {
      const command = this.state.pending;
      const stored = writePendingApply(
        this.pendingKey(),
        command ? { command, reviewDigest: this.state.pendingDigest ?? "" } : null
      );
      this.state = { ...this.state, pendingPersisted: !command || stored };
    }
    this.changed(this.state);
  }

  get dirty(): boolean {
    return !!this.state.policy && updatesFor(this.state.policy, this.state.drafts).length > 0;
  }

  get locked(): boolean {
    return !!this.state.pending || this.state.phase === "loading" || this.state.readOnly;
  }

  // account identifies the operator for storage purposes and defaults to
  // identity. They differ where identity carries attributes that can change
  // under a live session, such as role — a recovery handle must not be keyed on
  // one of those, or it is lost the moment the attribute changes.
  async bind(identity: string, scope: Scope, account = identity) {
    this.epoch++;
    this.draftVersion++;
    this.previewSequence++;
    this.identity = identity;
    this.account = account;
    this.scope = { ...scope };
    this.state = emptyState();
    // An apply whose outcome was never confirmed outlives the page. The stored
    // command is the only handle on the recover flow, and telling the operator
    // to keep the tab open is not a mechanism, so it is restored here and the
    // change is presented as unresolved until recover settles it.
    const carried = readPendingApply(this.pendingKey());
    if (carried) {
      this.state = {
        ...this.state,
        pending: carried.command,
        pendingDigest: carried.reviewDigest,
        phase: "uncertain",
      };
    }
    this.changed(this.state);
    if (identity) await this.load();
  }

  private pendingKey(): string {
    return `placement.pending.${this.account}.${this.scope?.kind ?? ""}.${this.scope?.streamId ?? ""}`;
  }

  dispose() {
    this.epoch++;
    this.identity = "";
  }

  async load(retainDraft = false) {
    if (!this.identity || this.state.pending) return;
    const epoch = this.epoch;
    const request = ++this.loadSequence;
    const hadDraft = retainDraft && this.dirty;
    this.patch({ phase: "loading", error: "" });
    try {
      const result = await this.api.policy({ ...this.scope });
      if (epoch !== this.epoch || request !== this.loadSequence) return;
      if (result?.__typename !== "MediaPlacementPolicyState") {
        this.fail(result);
        return;
      }
      if (!sameScope(result.scope, this.scope)) {
        this.patch({
          phase: "idle",
          error: "The response does not identify this policy scope. Reload before editing.",
        });
        return;
      }
      this.draftVersion++;
      this.previewSequence++;
      this.patch({
        policy: result,
        drafts: hadDraft ? this.state.drafts : savedDrafts(result),
        review: null,
        acknowledgements: [],
        previewStale: !!this.state.preview,
        previewing: false,
        conflict: false,
        readOnly: !result.actions.canManage,
        phase: "idle",
      });
    } catch {
      if (epoch === this.epoch && request === this.loadSequence)
        this.patch({
          phase: "idle",
          error: "Could not load placement rules. Your draft has not been discarded.",
        });
    }
  }

  edit(verb: Verb, rules: Rules | null) {
    if (this.locked || !this.state.policy?.actions.canManage) return;
    this.invalidateDraft();
    this.patch({ drafts: { ...this.state.drafts, [verb]: copyRules(rules) }, error: "" });
  }

  discard() {
    // Refuse while a conflict stands. The held policy is the pre-conflict
    // snapshot, so rebasing drafts onto it and clearing the flag would present
    // stale rules and rollout state as current and in sync. Reloading is the
    // only way out of a conflict.
    if (this.locked || this.state.conflict || !this.state.policy) return;
    this.invalidateDraft();
    this.patch({ drafts: savedDrafts(this.state.policy), error: "" });
  }

  // abandon releases a recovery handle the operator has decided not to pursue.
  // Every other exit from `pending` requires the server to identify the change,
  // and a response that never matches — a mismatched digest, a receipt for a
  // different scope — would otherwise leave the editor locked on a command it
  // can never resolve, now across reloads as well. The change's outcome stays
  // unknown; this only stops the editor from insisting on resolving it here.
  abandon() {
    if (!this.state.pending) return;
    writePendingApply(this.pendingKey(), null);
    this.patch({
      pending: null,
      pendingDigest: null,
      review: null,
      acknowledgements: [],
      phase: "idle",
      error:
        "Recovery key abandoned. Whether that change was saved is still unknown — reload and compare before applying another.",
    });
  }

  invalidatePreview() {
    this.previewSequence++;
    this.patch({ previewStale: !!this.state.preview, previewing: false });
  }

  private invalidateDraft() {
    this.draftVersion++;
    this.invalidatePreview();
    this.patch({ review: null, acknowledgements: [], phase: "idle" });
  }

  async preview(
    input: Omit<
      PreviewInput,
      "scope" | "expectedRevision" | "expectedParentRevision" | "draftUpdate"
    >
  ) {
    const policy = this.state.policy;
    if (!policy?.actions.canPreview || this.state.pending) return;
    const errors = validateDraft(this.state.drafts[input.verb]);
    if (errors.length) {
      this.patch({ error: errors.join(" ") });
      return;
    }
    const epoch = this.epoch;
    const version = this.draftVersion;
    const sequence = ++this.previewSequence;
    const rules = copyRules(this.state.drafts[input.verb]);
    this.patch({ previewing: true, previewStale: !!this.state.preview, error: "" });
    try {
      const result = await this.api.preview({
        ...input,
        scope: { ...this.scope },
        expectedRevision: policy.revision,
        expectedParentRevision: policy.parentRevision,
        draftUpdate: { verb: input.verb, kind: rules ? "SET" : "CLEAR", rules },
      });
      if (
        epoch !== this.epoch ||
        version !== this.draftVersion ||
        sequence !== this.previewSequence
      )
        return;
      if (
        result?.__typename === "MediaPlacementPreview" &&
        sameScope(result.scope, this.scope) &&
        result.verb === input.verb &&
        result.revision === policy.revision &&
        result.parentRevision === policy.parentRevision
      ) {
        this.patch({ preview: result, previewStale: false, previewing: false });
      } else {
        this.fail(result);
        this.patch({ previewing: false });
      }
    } catch {
      if (
        epoch === this.epoch &&
        version === this.draftVersion &&
        sequence === this.previewSequence
      ) {
        this.patch({
          previewing: false,
          error: "Preview unavailable. No destination was reserved.",
        });
      }
    }
  }

  async review() {
    const policy = this.state.policy;
    if (
      !policy ||
      this.locked ||
      !this.dirty ||
      this.state.phase === "reviewing" ||
      this.state.conflict
    )
      return;
    const errors = Object.values(this.state.drafts).flatMap(validateDraft);
    if (errors.length) {
      this.patch({ error: errors.join(" ") });
      return;
    }
    const epoch = this.epoch;
    const version = this.draftVersion;
    this.patch({ phase: "reviewing", review: null, acknowledgements: [], error: "" });
    try {
      const result = await this.api.review({
        scope: { ...this.scope },
        expectedRevision: policy.revision,
        expectedParentRevision: policy.parentRevision,
        updates: updatesFor(policy, this.state.drafts),
      });
      if (epoch !== this.epoch || version !== this.draftVersion) return;
      if (result?.__typename === "MediaPlacementReview")
        this.patch({ review: result, phase: "idle" });
      else this.fail(result);
    } catch {
      if (epoch === this.epoch && version === this.draftVersion)
        this.patch({ phase: "idle", error: "Could not review changes. Nothing was applied." });
    }
  }

  acknowledge(id: string, checked: boolean) {
    if (
      this.state.pending ||
      !this.state.review?.warnings.some(
        (warning) => warning.id === id && warning.acknowledgementRequired
      )
    )
      return;
    this.patch({
      acknowledgements: checked
        ? [...new Set([...this.state.acknowledgements, id])]
        : this.state.acknowledgements.filter((value) => value !== id),
    });
  }

  async apply() {
    const { policy, review } = this.state;
    if (!policy || !review || this.locked || this.state.conflict || !this.dirty) return;
    if (
      Date.parse(review.expiresAt) <= this.now() ||
      !Number.isFinite(Date.parse(review.expiresAt))
    ) {
      this.patch({
        review: null,
        error: "This review has expired. Review the current draft again.",
      });
      return;
    }
    if (
      review.warnings.some(
        (warning) =>
          warning.acknowledgementRequired && !this.state.acknowledgements.includes(warning.id)
      )
    ) {
      this.patch({ error: "Acknowledge the required warnings before applying." });
      return;
    }
    // The digest binds what the operator reviewed to what the server reports
    // back. Applying without one would leave the response's rule content
    // unchecked, so a review that carries no digest is not applyable.
    if (!review.digest) {
      this.patch({ error: "This review cannot be verified. Review the current draft again." });
      return;
    }
    const command: ApplyInput = {
      scope: { ...this.scope },
      expectedRevision: policy.revision,
      expectedParentRevision: policy.parentRevision,
      updates: updatesFor(policy, this.state.drafts),
      reviewToken: review.reviewToken,
      idempotencyKey: this.uuid(),
      acknowledgedWarningIds: [...this.state.acknowledgements].sort(),
    };
    this.patch({ pendingDigest: review.digest, pending: command });
    await this.submit(command);
  }

  private async submit(command: ApplyInput) {
    const epoch = this.epoch;
    this.patch({ phase: "applying", error: "" });
    try {
      const result = await this.api.apply(structuredClone(command));
      if (epoch !== this.epoch) return;
      if (result?.__typename === "MediaPlacementChange") {
        await this.acceptChange(result, command);
      } else if (
        result &&
        (result.__typename === "AuthError" ||
          result.__typename === "NotFoundError" ||
          (result.__typename === "MediaPlacementError" && result.code !== "UNAVAILABLE"))
      ) {
        this.patch({ pending: null, pendingDigest: null, review: null });
        this.fail(result);
      } else {
        await this.recover();
      }
    } catch {
      if (epoch === this.epoch) await this.recover();
    }
  }

  async recover(retryIfMissing = false) {
    const command = this.state.pending;
    if (!command || this.state.phase === "recovering") return;
    const epoch = this.epoch;
    this.patch({ phase: "recovering", error: "" });
    try {
      const result = await this.api.change({ ...command.scope }, command.idempotencyKey);
      if (epoch !== this.epoch) return;
      if (result?.__typename === "MediaPlacementChange") {
        await this.acceptChange(result, command);
        return;
      }
      if (result?.__typename === "NotFoundError" && retryIfMissing) {
        await this.submit(command);
        return;
      }
      this.patch({
        phase: "uncertain",
        error:
          "Save outcome is not confirmed. Check again or retry the same change — it is kept across a reload. Do not submit a different change yet.",
      });
    } catch {
      if (epoch === this.epoch)
        this.patch({
          phase: "uncertain",
          error:
            "Cannot check the saved change yet. The original request key is retained for recovery.",
        });
    }
  }

  private async acceptChange(result: Change, command: ApplyInput) {
    // The reviewed digest comes from the live review when there is one, and
    // otherwise from the digest carried with the command across a reload. A
    // restored session has no review, and comparing against nothing would
    // reject every response and strand the operator on an unresolvable change.
    const reviewedDigest = this.state.review?.digest ?? this.state.pendingDigest;
    if (
      result.idempotencyKey !== command.idempotencyKey ||
      !sameScope(result.scope, command.scope) ||
      result.parentRevision !== command.expectedParentRevision ||
      result.digest !== reviewedDigest
    ) {
      this.patch({
        phase: "uncertain",
        error:
          "The response does not identify this change. Check its status again, or abandon the recovery key and reload.",
      });
      return;
    }
    this.patch({
      change: result,
      pending: null,
      pendingDigest: null,
      review: null,
      acknowledgements: [],
      phase: "idle",
    });
    await this.load();
  }

  private fail(result: PolicyResult | ReviewResult | PreviewResult | ChangeResult | undefined) {
    this.patch({
      phase: "idle",
      error: errorMessage(result),
      conflict:
        this.state.conflict ||
        (result?.__typename === "MediaPlacementError" && result.code === "REVISION_CONFLICT"),
      readOnly: this.state.readOnly || result?.__typename === "AuthError",
    });
  }
}

function sameScope(left: Scope, right: Scope): boolean {
  return left.kind === right.kind && (left.streamId ?? null) === (right.streamId ?? null);
}
