import type {
  ApplyClusterMediaConsentChange$input,
  ApplyClusterMediaConsentChange$result,
  GetClusterMediaConsent$result,
} from "$houdini";
import { errorMessage, type Review, type ReviewResult } from "./model";

type Unmasked<T> = T extends (infer Item)[]
  ? Unmasked<Item>[]
  : T extends object
    ? { [Key in keyof T as Key extends " $fragments" ? never : Key]: Unmasked<T[Key]> }
    : T;
export type ConsentResult = Unmasked<GetClusterMediaConsent$result["clusterMediaConsent"]>;
export type Consent = Extract<ConsentResult, { __typename: "MediaCapacityConsent" }>;
export type ConsentChangeResult = Unmasked<
  ApplyClusterMediaConsentChange$result["applyClusterMediaConsentChange"]
>;
export type ConsentChange = Extract<
  ConsentChangeResult,
  { __typename: "MediaCapacityConsentChange" }
>;
export type ConsentApply = Omit<ApplyClusterMediaConsentChange$input["input"], "clusterId"> & {
  clusterId: string;
};
export type ConsentDraft = Pick<ConsentApply, "allowIngest" | "allowServe" | "allowExternalSource">;
export type ConsentReview = Omit<
  ConsentApply,
  "reviewToken" | "idempotencyKey" | "acknowledgedWarningIds"
>;

export interface ConsentAPI {
  consent(clusterId: string): Promise<ConsentResult | undefined>;
  review(input: ConsentReview): Promise<ReviewResult | undefined>;
  apply(input: ConsentApply): Promise<ConsentChangeResult | undefined>;
  change(clusterId: string, key: string): Promise<ConsentChangeResult | undefined>;
}

export interface ConsentState {
  consent: Consent | null;
  draft: ConsentDraft | null;
  review: Review | null;
  acknowledgements: string[];
  pending: ConsentApply | null;
  change: ConsentChange | null;
  phase: "idle" | "loading" | "reviewing" | "applying" | "recovering" | "uncertain";
  error: string;
  conflict: boolean;
  readOnly: boolean;
}

function emptyState(): ConsentState {
  return {
    consent: null,
    draft: null,
    review: null,
    acknowledgements: [],
    pending: null,
    change: null,
    phase: "idle",
    error: "",
    conflict: false,
    readOnly: false,
  };
}

export function consentDraft(value: ConsentDraft): ConsentDraft {
  return {
    allowIngest: value.allowIngest,
    allowServe: value.allowServe,
    allowExternalSource: value.allowExternalSource,
  };
}

export function consentDirty(state: ConsentState): boolean {
  return (
    !!state.consent &&
    !!state.draft &&
    (Object.keys(consentDraft(state.consent)) as (keyof ConsentDraft)[]).some(
      (key) => state.consent?.[key] !== state.draft?.[key]
    )
  );
}

// Uncertain writes retain the reviewed command, not just its idempotency key.
// Identity and cluster changes invalidate every outstanding asynchronous response.
export class ConsentSession {
  state = emptyState();
  private epoch = 0;
  private version = 0;
  private loadSequence = 0;
  private identity = "";
  private clusterId = "";

  constructor(
    private readonly api: ConsentAPI,
    private readonly changed: (state: ConsentState) => void,
    private readonly uuid: () => string = () => crypto.randomUUID(),
    private readonly now: () => number = Date.now
  ) {}

  private patch(next: Partial<ConsentState>) {
    this.state = { ...this.state, ...next };
    this.changed(this.state);
  }

  get locked() {
    return !!this.state.pending || this.state.phase === "loading" || this.state.readOnly;
  }

  async bind(identity: string, clusterId: string) {
    this.epoch++;
    this.version++;
    this.identity = identity;
    this.clusterId = clusterId;
    this.state = emptyState();
    this.changed(this.state);
    if (identity && clusterId) await this.load();
  }

  dispose() {
    this.epoch++;
    this.identity = "";
  }

  async load(retainDraft = false) {
    if (!this.identity || this.state.pending) return;
    const epoch = this.epoch,
      sequence = ++this.loadSequence;
    const retain = retainDraft && consentDirty(this.state);
    this.version++;
    this.patch({ phase: "loading", review: null, acknowledgements: [], error: "" });
    try {
      const result = await this.api.consent(this.clusterId);
      if (epoch !== this.epoch || sequence !== this.loadSequence) return;
      if (result?.__typename !== "MediaCapacityConsent") {
        this.fail(result);
        return;
      }
      if (result.clusterId !== this.clusterId) {
        this.patch({
          phase: "idle",
          error: "The response does not identify this cluster. Reload before editing.",
        });
        return;
      }
      if (
        !validRevision(result.revision) ||
        (this.state.consent && BigInt(result.revision) < BigInt(this.state.consent.revision))
      ) {
        this.patch({
          phase: "idle",
          error:
            "The saved revision is not yet available from this read. Refresh status again; your current state is retained.",
        });
        return;
      }
      this.patch({
        consent: result,
        draft: retain ? this.state.draft : consentDraft(result),
        phase: "idle",
        conflict: false,
        readOnly: !result.canManage,
      });
    } catch {
      if (epoch === this.epoch && sequence === this.loadSequence)
        this.patch({
          phase: "idle",
          error: "Could not load capacity permissions. Your draft has not been discarded.",
        });
    }
  }

  edit(key: keyof ConsentDraft, value: boolean) {
    if (this.locked || !this.state.consent?.canManage || !this.state.draft) return;
    this.version++;
    this.patch({
      draft: { ...this.state.draft, [key]: value },
      review: null,
      acknowledgements: [],
      phase: "idle",
      error: "",
    });
  }

  discard() {
    // A standing conflict means the held consent is stale; rebasing the draft
    // onto it would silently discard the operator's work against rules that no
    // longer apply. Reloading is the only way out.
    if (this.locked || this.state.conflict || !this.state.consent) return;
    this.version++;
    this.patch({
      draft: consentDraft(this.state.consent),
      review: null,
      acknowledgements: [],
      phase: "idle",
      error: "",
    });
  }

  async review() {
    const { consent, draft } = this.state;
    if (
      !consent ||
      !draft ||
      this.locked ||
      !consentDirty(this.state) ||
      this.state.conflict ||
      this.state.phase === "reviewing"
    )
      return;
    const epoch = this.epoch,
      version = this.version;
    this.patch({ phase: "reviewing", review: null, acknowledgements: [], error: "" });
    try {
      const result = await this.api.review({
        clusterId: this.clusterId,
        expectedRevision: consent.revision,
        ...consentDraft(draft),
      });
      if (epoch !== this.epoch || version !== this.version) return;
      if (result?.__typename === "MediaPlacementReview")
        this.patch({ review: result, phase: "idle" });
      else this.fail(result);
    } catch {
      if (epoch === this.epoch && version === this.version)
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
    const { consent, draft, review } = this.state;
    if (
      !consent ||
      !draft ||
      !review ||
      this.locked ||
      this.state.conflict ||
      !consentDirty(this.state)
    )
      return;
    if (
      !Number.isFinite(Date.parse(review.expiresAt)) ||
      Date.parse(review.expiresAt) <= this.now()
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
    const command: ConsentApply = {
      clusterId: this.clusterId,
      expectedRevision: consent.revision,
      ...consentDraft(draft),
      reviewToken: review.reviewToken,
      idempotencyKey: this.uuid(),
      acknowledgedWarningIds: [...this.state.acknowledgements].sort(),
    };
    this.patch({ pending: command });
    await this.submit(command);
  }

  private async submit(command: ConsentApply) {
    const epoch = this.epoch;
    this.patch({ phase: "applying", error: "" });
    try {
      const result = await this.api.apply(structuredClone(command));
      if (epoch !== this.epoch) return;
      if (result?.__typename === "MediaCapacityConsentChange") await this.accept(result, command);
      else if (
        result &&
        (result.__typename === "AuthError" ||
          result.__typename === "NotFoundError" ||
          (result.__typename === "MediaPlacementError" && result.code !== "UNAVAILABLE"))
      ) {
        this.patch({ pending: null, review: null });
        this.fail(result);
      } else await this.recover();
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
      const result = await this.api.change(command.clusterId, command.idempotencyKey);
      if (epoch !== this.epoch) return;
      if (result?.__typename === "MediaCapacityConsentChange") {
        await this.accept(result, command);
        return;
      }
      if (result?.__typename === "NotFoundError" && retryIfMissing) {
        await this.submit(command);
        return;
      }
      this.patch({
        phase: "uncertain",
        error:
          "Save outcome is not confirmed. Keep this page open; check again or retry the same change.",
      });
    } catch {
      if (epoch === this.epoch)
        this.patch({
          phase: "uncertain",
          error:
            "Cannot check the saved change yet. The original request is retained for recovery.",
        });
    }
  }

  private async accept(result: ConsentChange, command: ConsentApply) {
    if (
      result.clusterId !== command.clusterId ||
      result.idempotencyKey !== command.idempotencyKey ||
      result.digest !== this.state.review?.digest ||
      !nextRevision(command.expectedRevision, result.revision)
    ) {
      this.patch({
        phase: "uncertain",
        error: "The response does not identify this change. Check its status again.",
      });
      return;
    }
    const consent = this.state.consent;
    this.patch({
      change: result,
      pending: null,
      review: null,
      acknowledgements: [],
      phase: "idle",
      consent: consent
        ? {
            ...consent,
            ...consentDraft(command),
            revision: result.revision,
            rollout: result.rollout,
          }
        : null,
      draft: consentDraft(command),
    });
    await this.load();
  }

  private fail(result: ConsentResult | ConsentChangeResult | ReviewResult | undefined) {
    this.patch({
      phase: "idle",
      error: errorMessage(result && "message" in result ? result : undefined),
      conflict:
        this.state.conflict ||
        (result?.__typename === "MediaPlacementError" && result.code === "REVISION_CONFLICT"),
      readOnly: this.state.readOnly || result?.__typename === "AuthError",
    });
  }
}

function nextRevision(before: string, after: string): boolean {
  return validRevision(before) && validRevision(after) && BigInt(after) === BigInt(before) + 1n;
}

function validRevision(value: string): boolean {
  return /^(0|[1-9][0-9]{0,18})$/.test(value) && BigInt(value) <= 9223372036854775807n;
}
