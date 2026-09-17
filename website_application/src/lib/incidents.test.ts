import { describe, expect, it } from "vitest";
import { applyIncidentUpdates, type IncidentRow, type IncidentUpdate } from "./incidents";

const row = {
  id: "inc-1",
  scope: "TENANT",
  tenantId: "tenant-a",
  clusterId: "home",
  region: "eu-west",
  alertname: "EdgeNodeHeartbeatMissing",
  severity: "critical",
  status: "FIRING",
  resolution: null,
  title: "Edge node heartbeat missing",
  summary: null,
  firingAlertCount: 1,
  startedAt: "2026-09-14T09:30:00Z",
  lastAlertAt: "2026-09-14T09:35:00Z",
  acknowledgedAt: null,
  acknowledgedBy: null,
  assignedTo: null,
  resolvedAt: null,
  resolvedBy: null,
  createdAt: "2026-09-14T09:30:00Z",
  updatedAt: "2026-09-14T09:35:00Z",
} as unknown as IncidentRow;

function update(overrides: Partial<IncidentUpdate> = {}): IncidentUpdate {
  return {
    incidentId: "inc-1",
    clusterId: "home",
    status: "ACKNOWLEDGED",
    severity: "warning",
    title: "Heartbeat missing",
    change: "acknowledged",
    updatedAt: "2026-09-14T10:00:00Z",
    ...overrides,
  } as IncidentUpdate;
}

const noFilter = { statuses: [], clusterId: "" };

describe("applyIncidentUpdates", () => {
  it("updates status, severity, and title in place", () => {
    const result = applyIncidentUpdates([row], [update()], noFilter, false);
    expect(result.refetch).toBe(false);
    expect(result.rows[0]).toMatchObject({
      status: "ACKNOWLEDGED",
      severity: "warning",
      title: "Heartbeat missing",
      updatedAt: "2026-09-14T10:00:00Z",
      firingAlertCount: 1,
    });
  });

  it("ignores an event older than the loaded row", () => {
    const result = applyIncidentUpdates(
      [row],
      [update({ updatedAt: "2026-09-14T09:00:00Z" })],
      noFilter,
      false
    );
    expect(result).toEqual({ rows: [row], refetch: false });
  });

  it.each(["alert_firing", "alert_resolved", "auto_resolved", "scope_changed", "future_change"])(
    "refetches a listed incident after %s",
    (change) => {
      expect(applyIncidentUpdates([row], [update({ change })], noFilter, false).refetch).toBe(true);
    }
  );

  it("refetches when a listed incident leaves the status filter", () => {
    const filter = { statuses: ["FIRING"], clusterId: "" };
    expect(applyIncidentUpdates([row], [update()], filter, false).refetch).toBe(true);
  });

  it("refetches for an unloaded incident that matches the filter or was opened", () => {
    const other = { incidentId: "inc-2" };
    expect(applyIncidentUpdates([row], [update(other)], noFilter, false).refetch).toBe(true);
    const firingOnly = { statuses: ["FIRING"], clusterId: "" };
    expect(
      applyIncidentUpdates([row], [update({ ...other, change: "opened" })], firingOnly, false)
        .refetch
    ).toBe(true);
  });

  it("skips an unloaded incident outside the filter when every page is loaded", () => {
    const firingOnly = { statuses: ["FIRING"], clusterId: "" };
    const other = update({ incidentId: "inc-2" });
    expect(applyIncidentUpdates([row], [other], firingOnly, false).refetch).toBe(false);
    expect(applyIncidentUpdates([row], [other], firingOnly, true).refetch).toBe(true);
    const otherCluster = { statuses: [], clusterId: "away" };
    expect(applyIncidentUpdates([row], [other], otherCluster, false).refetch).toBe(false);
  });
});
