import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import {
  attemptOutcome,
  deliveryFilterActive,
  deliveryQueryVariables,
  emptyDeliveryFilter,
  endpointHealthSummary,
  endpointStatusBadge,
  eventTypesSummary,
  isoToLocalInput,
  localInputToIso,
  mutationOutcome,
  rangeReplayMessage,
  rangeReplayPlan,
  revealedSecret,
  toggleEventType,
  updateInput,
  draftProblem,
  createInput,
  type WebhookEndpointRow,
} from "./webhooks";

const endpoint = {
  id: "ep-1",
  url: "https://hooks.example.com/frameworks",
  description: "Production receiver",
  eventTypes: ["stream.live", "stream.idle"],
  apiVersion: "v1",
  status: "ENABLED",
  disabledReason: null,
  disabledAt: null,
  consecutiveFailures: 0,
  failingSince: null,
  lastSuccessAt: null,
  lastFailureAt: null,
  previousSecretExpiresAt: null,
  createdAt: "2026-09-18T10:00:00Z",
  updatedAt: "2026-09-18T10:00:00Z",
} as unknown as WebhookEndpointRow;

describe("event type selection", () => {
  it("makes '*' exclusive in both directions", () => {
    expect(toggleEventType(["stream.live", "clip.ready"], "*")).toEqual(["*"]);
    expect(toggleEventType(["*"], "stream.live")).toEqual(["stream.live"]);
    expect(toggleEventType(["stream.live"], "clip.ready")).toEqual(["stream.live", "clip.ready"]);
    expect(toggleEventType(["stream.live", "clip.ready"], "stream.live")).toEqual(["clip.ready"]);
  });

  it("summarises the subscribed types", () => {
    expect(eventTypesSummary(["*"])).toBe("All event types");
    expect(eventTypesSummary(["a", "b"])).toBe("a, b");
    expect(eventTypesSummary(["a", "b", "c", "d", "e"])).toBe("a, b, c +2 more");
  });
});

describe("delivery filters", () => {
  it("omits blank filters and keeps the endpoint", () => {
    expect(deliveryQueryVariables("ep-1", emptyDeliveryFilter)).toEqual({
      endpointId: "ep-1",
      statuses: null,
      eventType: null,
      eventId: null,
      createdAfter: null,
      createdBefore: null,
    });
    expect(deliveryFilterActive(emptyDeliveryFilter)).toBe(false);
  });

  it("maps statuses, trimmed strings, and the local time range to UTC", () => {
    const after = "2026-09-18T10:00";
    const variables = deliveryQueryVariables("ep-1", {
      statuses: ["FAILED", "SKIPPED"],
      eventType: " stream.live ",
      eventId: " evt_1 ",
      createdAfter: after,
      createdBefore: "",
    });
    expect(variables.statuses).toEqual(["FAILED", "SKIPPED"]);
    expect(variables.eventType).toBe("stream.live");
    expect(variables.eventId).toBe("evt_1");
    expect(variables.createdAfter).toBe(new Date(after).toISOString());
    expect(variables.createdBefore).toBeNull();
  });

  it("round-trips datetime-local values", () => {
    const iso = localInputToIso("2026-09-18T10:30");
    expect(iso).not.toBeNull();
    expect(isoToLocalInput(iso as string)).toBe("2026-09-18T10:30");
    expect(localInputToIso("not a date")).toBeNull();
    expect(localInputToIso("")).toBeNull();
  });
});

describe("range replay", () => {
  it("requires both ends in order", () => {
    expect(rangeReplayPlan("ep-1", "", "2026-09-18T10:00").ok).toBe(false);
    const reversed = rangeReplayPlan("ep-1", "2026-09-18T11:00", "2026-09-18T10:00");
    expect(reversed).toEqual({
      ok: false,
      message: "The start of the range must be before its end.",
    });
    const plan = rangeReplayPlan("ep-1", "2026-09-18T10:00", "2026-09-18T11:00");
    expect(plan).toEqual({
      ok: true,
      variables: {
        endpointId: "ep-1",
        createdAfter: new Date("2026-09-18T10:00").toISOString(),
        createdBefore: new Date("2026-09-18T11:00").toISOString(),
      },
    });
  });

  it("tells the user to continue while hasMore is set", () => {
    expect(rangeReplayMessage(1, false)).toBe("1 delivery queued again.");
    expect(rangeReplayMessage(1000, true)).toContain("replay again to continue");
  });
});

describe("endpoint edits", () => {
  it("sends only changed fields and compares event types as a set", () => {
    expect(
      updateInput(endpoint, {
        url: endpoint.url,
        description: endpoint.description,
        eventTypes: ["stream.idle", "stream.live"],
      })
    ).toBeNull();
    expect(
      updateInput(endpoint, {
        url: " https://hooks.example.com/v2 ",
        description: endpoint.description,
        eventTypes: ["*"],
      })
    ).toEqual({ url: "https://hooks.example.com/v2", eventTypes: ["*"] });
    expect(updateInput(endpoint, { ...endpoint, description: "" })).toEqual({ description: "" });
  });

  it("checks the draft before create", () => {
    expect(draftProblem({ url: "", description: "", eventTypes: ["*"] })).toMatch(/URL/);
    expect(draftProblem({ url: "http://x.test", description: "", eventTypes: ["*"] })).toMatch(
      /https/
    );
    expect(draftProblem({ url: "https://x.test", description: "", eventTypes: [] })).toMatch(
      /event type/
    );
    expect(createInput({ url: " https://x.test ", description: " ", eventTypes: ["*"] })).toEqual({
      url: "https://x.test",
      description: null,
      eventTypes: ["*"],
    });
  });
});

describe("endpoint status", () => {
  it("distinguishes auto-disabled from user-disabled", () => {
    expect(endpointStatusBadge({ status: "ENABLED", disabledReason: null }).label).toBe("Enabled");
    expect(endpointStatusBadge({ status: "DISABLED", disabledReason: "FAILING" }).label).toBe(
      "Disabled: failing"
    );
    expect(endpointStatusBadge({ status: "DISABLED", disabledReason: "USER" }).label).toBe(
      "Disabled"
    );
  });

  it("reports the failure streak before the last success", () => {
    expect(endpointHealthSummary(endpoint)).toBe("No deliveries yet");
    expect(
      endpointHealthSummary({
        ...endpoint,
        consecutiveFailures: 3,
        lastSuccessAt: "2026-09-17T10:00:00Z",
        failingSince: null,
      })
    ).toBe("3 failed attempts");
  });

  it("describes attempt outcomes", () => {
    expect(attemptOutcome(200, "")).toBe("HTTP 200");
    expect(attemptOutcome(503, "http_status")).toBe("Non-2xx response (HTTP 503)");
    expect(attemptOutcome(0, "timeout")).toBe("Timed out");
    expect(attemptOutcome(0, "something_new")).toBe("something_new");
  });
});

describe("mutation results", () => {
  it("returns the success member", () => {
    const value = { __typename: "WebhookEndpoint", id: "ep-1" };
    expect(mutationOutcome(value, null, "WebhookEndpoint", "failed")).toEqual({
      ok: true,
      value,
    });
  });

  it("classifies every error member with its message", () => {
    const cases: [string, string][] = [
      ["ValidationError", "validation"],
      ["NotFoundError", "not_found"],
      ["RateLimitError", "rate_limit"],
      ["AuthError", "auth"],
    ];
    for (const [typename, kind] of cases) {
      expect(
        mutationOutcome({ __typename: typename, message: "nope" }, null, "WebhookEndpoint", "x")
      ).toEqual({ ok: false, kind, message: "nope" });
    }
  });

  it("falls back to GraphQL errors, then the fallback", () => {
    expect(mutationOutcome(null, [{ message: "boom" }], "WebhookEndpoint", "x")).toEqual({
      ok: false,
      kind: "error",
      message: "boom",
    });
    expect(mutationOutcome(undefined, [], "WebhookEndpoint", "fallback").ok).toBe(false);
    expect(
      mutationOutcome({ __typename: "ValidationError", message: "" }, null, "WebhookEndpoint", "fb")
    ).toEqual({ ok: false, kind: "validation", message: "fb" });
  });
});

describe("one-time secret", () => {
  it("is taken only from the WebhookEndpointSecret member", () => {
    expect(
      revealedSecret(
        {
          __typename: "WebhookEndpointSecret",
          secret: "whsec_abc",
          endpoint: { id: "ep-1", url: "https://x.test" },
        },
        "created"
      )
    ).toEqual({
      endpointId: "ep-1",
      url: "https://x.test",
      secret: "whsec_abc",
      reason: "created",
    });
    expect(revealedSecret({ __typename: "ValidationError", message: "bad" }, "created")).toBeNull();
    expect(
      revealedSecret(
        { __typename: "WebhookEndpointSecret", secret: "", endpoint: { id: "ep-1" } },
        "rotated"
      )
    ).toBeNull();
    expect(revealedSecret(null, "rotated")).toBeNull();
  });

  it("is selected only by the create and rotate operations", () => {
    const repoRoot = existsSync(path.resolve(process.cwd(), "pkg/graphql"))
      ? process.cwd()
      : path.resolve(process.cwd(), "..");
    const operationsRoot = path.join(repoRoot, "pkg/graphql/operations");
    const selecting: string[] = [];
    for (const dir of ["queries", "mutations", "fragments", "subscriptions"]) {
      const full = path.join(operationsRoot, dir);
      if (!existsSync(full)) continue;
      for (const name of readdirSync(full)) {
        if (!name.includes("Webhook")) continue;
        const source = readFileSync(path.join(full, name), "utf8")
          .split("\n")
          .filter((line) => !line.trim().startsWith("#"))
          .join("\n");
        if (/^\s*secret\s*$/m.test(source)) selecting.push(`${dir}/${name}`);
      }
    }
    expect(selecting.sort()).toEqual([
      "mutations/CreateWebhookEndpoint.gql",
      "mutations/RotateWebhookEndpointSecret.gql",
    ]);
  });
});
