import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import IncidentDetailView from "$lib/components/incidents/IncidentDetailView.svelte";
import NotificationBell from "$lib/components/NotificationBell.svelte";
import IncidentsPage from "../../src/routes/infrastructure/incidents/+page.svelte";
import AdminIncidentsPage from "../../src/routes/admin/incidents/+page.svelte";
import AdminIncidentPage from "../../src/routes/admin/incidents/[id]/+page.svelte";
import { page } from "$app/state";
import { notifyRealtimeReconnected } from "$lib/houdini/reconnect";

const mocks = vi.hoisted(() => ({
  fetchIncident: vi.fn(),
  acknowledge: vi.fn(),
  resolveIncident: vi.fn(),
  addNote: vi.fn(),
  assign: vi.fn(),
  fetchIncidents: vi.fn(),
  fetchPlatformIncidents: vi.fn(),
  listen: vi.fn(),
  unlisten: vi.fn(),
  emitUpdate: (_event: Record<string, unknown>) => {},
  setPlatformOperator: (_operator: boolean) => {},
}));

vi.mock("$lib/stores/notifications.svelte", () => ({
  notificationStore: {
    unreadCount: 0,
    panelOpen: false,
    loadReports: vi.fn(),
    refreshUnreadCount: vi.fn(),
    togglePanel: vi.fn(),
  },
}));

vi.mock("$lib/stores/auth", async () => {
  const { writable } = await import("svelte/store");
  const user = (platformOperator: boolean) => ({
    isAuthenticated: true,
    user: {
      id: "user-a",
      tenant_id: "tenant-a",
      role: "owner",
      platform_operator: platformOperator,
    },
  });
  const auth = writable(user(false));
  mocks.setPlatformOperator = (operator) => auth.set(user(operator));
  return { auth };
});

vi.mock("$houdini", async () => {
  const { readable, writable } = await import("svelte/store");
  const updates = writable<{ data: unknown; errors: unknown[] | null }>({
    data: null,
    errors: null,
  });
  mocks.emitUpdate = (event) => updates.set({ data: { liveIncidentUpdates: event }, errors: null });
  return {
    fragment: (value: unknown) => readable(value),
    IncidentFieldsStore: class {},
    GetIncidentStore: class {
      fetch = (...args: unknown[]) => mocks.fetchIncident(...args);
    },
    GetIncidentsStore: class {
      fetch = (...args: unknown[]) => mocks.fetchIncidents(...args);
    },
    GetPlatformIncidentsStore: class {
      fetch = (...args: unknown[]) => mocks.fetchPlatformIncidents(...args);
    },
    GetClustersAccessStore: class {
      subscribe = readable({ data: { clustersAccess: [] } }).subscribe;
      fetch = async () => ({ data: { clustersAccess: [] } });
    },
    IncidentUpdatesStore: class {
      subscribe = updates.subscribe;
      listen = (...args: unknown[]) => mocks.listen(...args);
      unlisten = (...args: unknown[]) => mocks.unlisten(...args);
    },
    AcknowledgeIncidentStore: class {
      mutate = mocks.acknowledge;
    },
    ResolveIncidentStore: class {
      mutate = mocks.resolveIncident;
    },
    AddIncidentNoteStore: class {
      mutate = mocks.addNote;
    },
    AssignIncidentStore: class {
      mutate = mocks.assign;
    },
  };
});

const incident = {
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
  summary: "edge-2 stopped reporting.",
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
};

function event(kind: string, extra: Record<string, unknown> = {}) {
  return {
    id: `event-${kind}`,
    kind,
    actorUserId: null,
    createdAt: "2026-09-14T09:40:00Z",
    note: null,
    assignedTo: null,
    reportId: null,
    channel: null,
    resolution: null,
    alertFingerprint: null,
    alertname: null,
    ...extra,
  };
}

function detail(status = "FIRING") {
  return {
    data: {
      incident: {
        incident: { ...incident, status },
        alerts: [
          {
            fingerprint: "fp-1",
            status: "firing",
            labels: { alertname: "EdgeNodeHeartbeatMissing", cluster: "home" },
            annotations: { summary: "edge-2 stopped reporting." },
            startsAt: "2026-09-14T09:30:00Z",
            endsAt: null,
            generatorUrl: null,
          },
        ],
        timeline: [
          event("ALERT_FIRING", { alertname: "EdgeNodeHeartbeatMissing" }),
          event("NOTE", { note: "Rebooting edge-2", actorUserId: "user-1" }),
          event("INVESTIGATION_ATTACHED", { reportId: "report-9" }),
          event("NOTIFIED", { channel: "slack" }),
        ],
      },
    },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.fetchIncidents.mockResolvedValue({ data: null });
  mocks.setPlatformOperator(false);
});
afterEach(cleanup);

describe("incident detail", () => {
  it("renders alerts and each timeline kind with the report link", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
      reportHref: (id: string) => `/skipper?report=${id}`,
      live: true,
    });
    await screen.findByText("Timeline");
    expect(mocks.fetchIncident.mock.calls[0][0].variables).toEqual({ id: "inc-1" });
    expect(mocks.listen).toHaveBeenCalled();
    expect(screen.getByText("EdgeNodeHeartbeatMissing started firing")).toBeTruthy();
    expect(screen.getByText("Rebooting edge-2")).toBeTruthy();
    expect(screen.getByText("Operators notified by Slack")).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Open the investigation report" }).getAttribute("href")
    ).toBe("/skipper?report=report-9");
    expect(screen.getByText("2 labels · 1 annotations")).toBeTruthy();
  });

  it("reloads after an acknowledge and not after a refused resolve", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    mocks.acknowledge.mockResolvedValueOnce({
      data: { acknowledgeIncident: { __typename: "Incident" } },
    });
    mocks.resolveIncident.mockResolvedValueOnce({
      data: {
        resolveIncident: { __typename: "AuthError", message: "Only cluster owners can resolve." },
      },
    });
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
    });
    await fireEvent.click(await screen.findByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
    expect(mocks.acknowledge).toHaveBeenCalledWith({ id: "inc-1" });

    await fireEvent.click(screen.getByRole("button", { name: "Resolve" }));
    await waitFor(() => expect(mocks.resolveIncident).toHaveBeenCalledWith({ id: "inc-1" }));
    expect(mocks.fetchIncident).toHaveBeenCalledTimes(2);
  });

  it("hides acknowledge and resolve on resolved incidents and omits the report link without a route", async () => {
    mocks.fetchIncident.mockResolvedValue(detail("RESOLVED"));
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/admin/incidents",
      backLabel: "All incidents",
    });
    await screen.findByText("Timeline");
    expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resolve" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Open the investigation report" })).toBeNull();
    expect(screen.getByText("Report report-9")).toBeTruthy();
  });

  it("sends a trimmed note", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    mocks.addNote.mockResolvedValueOnce({ data: { addIncidentNote: { __typename: "Incident" } } });
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
    });
    const input = await screen.findByLabelText("Add a note");
    await fireEvent.input(input, { target: { value: "  Replaced the PSU  " } });
    await fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    await waitFor(() =>
      expect(mocks.addNote).toHaveBeenCalledWith({ id: "inc-1", body: "Replaced the PSU" })
    );
  });

  it("assigns the incident to the signed-in user and unassigns it", async () => {
    mocks.fetchIncident.mockResolvedValueOnce(detail());
    mocks.assign.mockResolvedValue({ data: { assignIncident: { __typename: "Incident" } } });
    const assigned = detail();
    mocks.fetchIncident.mockResolvedValue({
      data: {
        incident: {
          ...assigned.data.incident,
          incident: { ...assigned.data.incident.incident, assignedTo: "user-a" as string | null },
        },
      },
    });
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
    });
    await fireEvent.click(await screen.findByRole("button", { name: "Assign to me" }));
    await waitFor(() =>
      expect(mocks.assign).toHaveBeenCalledWith({ id: "inc-1", assigneeUserId: "user-a" })
    );
    await fireEvent.click(await screen.findByRole("button", { name: "Unassign me" }));
    await waitFor(() =>
      expect(mocks.assign).toHaveBeenLastCalledWith({ id: "inc-1", assigneeUserId: null })
    );
    expect(screen.getByText("You")).toBeTruthy();
  });

  it("explains a missing incident", async () => {
    mocks.fetchIncident.mockResolvedValue({ data: { incident: null } });
    render(IncidentDetailView, {
      incidentId: "missing",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
    });
    expect(await screen.findByText("Incident not found")).toBeTruthy();
  });
});

function incidentUpdate(overrides: Record<string, unknown> = {}) {
  return {
    incidentId: "inc-1",
    clusterId: "home",
    status: "FIRING",
    severity: "critical",
    title: "Edge node heartbeat missing",
    change: "acknowledged",
    updatedAt: "2026-09-14T10:00:00Z",
    ...overrides,
  };
}

function renderLiveDetail() {
  return render(IncidentDetailView, {
    incidentId: "inc-1",
    backHref: "/infrastructure/incidents",
    backLabel: "All incidents",
    live: true,
  });
}

describe("incident detail realtime", () => {
  it("reloads for changes to its incident only", async () => {
    mocks.fetchIncident.mockResolvedValueOnce(detail()).mockResolvedValue(detail("ACKNOWLEDGED"));
    renderLiveDetail();
    await screen.findByText("Timeline");

    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-other" }));
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(mocks.fetchIncident).toHaveBeenCalledTimes(1);

    mocks.emitUpdate(incidentUpdate({ status: "ACKNOWLEDGED" }));
    mocks.emitUpdate(incidentUpdate({ status: "ACKNOWLEDGED", change: "note" }));
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull());
  });

  it("replaces an incident that moved to another owner instead of showing stale data", async () => {
    mocks.fetchIncident.mockResolvedValueOnce(detail()).mockResolvedValue({
      data: { incident: null },
    });
    renderLiveDetail();
    await screen.findByText("Timeline");

    mocks.emitUpdate(incidentUpdate({ change: "scope_changed" }));
    expect(await screen.findByText("Incident no longer available")).toBeTruthy();
    expect(screen.getByText(/Ownership of this incident's cluster changed/)).toBeTruthy();
    expect(screen.queryByText("Timeline")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Edge node heartbeat missing" })).toBeNull();
  });

  it("keeps the incident shown when a background reload fails", async () => {
    mocks.fetchIncident.mockResolvedValueOnce(detail()).mockRejectedValue(new Error("offline"));
    renderLiveDetail();
    await screen.findByText("Timeline");

    mocks.emitUpdate(incidentUpdate({ change: "note" }));
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
    expect(screen.getByText("Timeline")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("reloads once after the WebSocket reconnects", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    renderLiveDetail();
    await screen.findByText("Timeline");

    notifyRealtimeReconnected();
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(mocks.fetchIncident).toHaveBeenCalledTimes(2);
  });

  it("does not subscribe without live", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    render(IncidentDetailView, {
      incidentId: "inc-1",
      backHref: "/infrastructure/incidents",
      backLabel: "All incidents",
    });
    await screen.findByText("Timeline");
    expect(mocks.listen).not.toHaveBeenCalled();

    mocks.emitUpdate(incidentUpdate({ change: "note" }));
    notifyRealtimeReconnected();
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(mocks.fetchIncident).toHaveBeenCalledTimes(1);
  });
});

function incidentList(rows: (typeof incident)[], totalCount = rows.length) {
  return {
    data: {
      incidentsConnection: {
        nodes: rows,
        pageInfo: { hasNextPage: false, endCursor: rows.length ? "cursor-end" : null },
        totalCount,
      },
    },
  };
}

describe("tenant incident list realtime", () => {
  it("updates a listed incident in place", async () => {
    mocks.fetchIncidents.mockResolvedValue(incidentList([incident]));
    render(IncidentsPage);
    const link = await screen.findByRole("link", { name: "Edge node heartbeat missing" });
    const row = link.closest("tr") as HTMLElement;
    expect(within(row).getByText("Firing")).toBeTruthy();
    expect(
      within(row)
        .getByRole("link", { name: "View incident: Edge node heartbeat missing" })
        .getAttribute("href")
    ).toBe("/infrastructure/incidents/inc-1");

    mocks.emitUpdate(
      incidentUpdate({ status: "ACKNOWLEDGED", title: "Edge node heartbeat still missing" })
    );
    const renamed = await screen.findByRole("link", { name: "Edge node heartbeat still missing" });
    expect(within(renamed.closest("tr") as HTMLElement).getByText("Acknowledged")).toBeTruthy();
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(1);
  });

  it("refetches the filtered list when an incident moves away", async () => {
    mocks.fetchIncidents
      .mockResolvedValueOnce(incidentList([incident]))
      .mockResolvedValueOnce(incidentList([incident]))
      .mockResolvedValue(incidentList([]));
    render(IncidentsPage);
    await screen.findByRole("link", { name: "Edge node heartbeat missing" });
    await fireEvent.click(screen.getByRole("button", { name: "Firing" }));
    await waitFor(() => expect(mocks.fetchIncidents).toHaveBeenCalledTimes(2));

    mocks.emitUpdate(incidentUpdate({ change: "scope_changed" }));
    expect(await screen.findByText("No incidents match these filters")).toBeTruthy();
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(3);
    expect(mocks.fetchIncidents.mock.calls[2][0].variables).toEqual({
      first: 25,
      after: null,
      filter: { statuses: ["FIRING"], clusterId: null },
    });
  });

  it("refetches when an incident leaves the status filter", async () => {
    mocks.fetchIncidents
      .mockResolvedValueOnce(incidentList([incident]))
      .mockResolvedValueOnce(incidentList([incident]))
      .mockResolvedValue(incidentList([]));
    render(IncidentsPage);
    await screen.findByRole("link", { name: "Edge node heartbeat missing" });
    await fireEvent.click(screen.getByRole("button", { name: "Firing" }));
    await waitFor(() => expect(mocks.fetchIncidents).toHaveBeenCalledTimes(2));

    mocks.emitUpdate(incidentUpdate({ status: "RESOLVED", change: "resolved" }));
    expect(await screen.findByText("No incidents match these filters")).toBeTruthy();
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(3);
  });

  it("ignores an update delivered before the page subscribed", async () => {
    mocks.emitUpdate(incidentUpdate({ change: "scope_changed" }));
    mocks.fetchIncidents.mockResolvedValue(incidentList([incident]));
    render(IncidentsPage);
    await screen.findByRole("link", { name: "Edge node heartbeat missing" });
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(1);
  });

  it("refetches the list after the WebSocket reconnects", async () => {
    mocks.fetchIncidents.mockResolvedValue(incidentList([incident]));
    render(IncidentsPage);
    await screen.findByRole("link", { name: "Edge node heartbeat missing" });

    notifyRealtimeReconnected();
    await waitFor(() => expect(mocks.fetchIncidents).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("link", { name: "Edge node heartbeat missing" })).toBeTruthy();
  });
});

const platformIncident = {
  ...incident,
  id: "inc-platform",
  scope: "PLATFORM",
  tenantId: null as string | null,
  clusterId: "core-eu",
  title: "Kafka broker down",
};

type OperatorIncidentRow = Omit<typeof incident, "tenantId"> & { tenantId: string | null };

function platformIncidentList(rows: OperatorIncidentRow[], hasNextPage = false) {
  return {
    data: {
      platform: {
        incidents: {
          nodes: rows,
          pageInfo: { hasNextPage, endCursor: rows.length ? "cursor-end" : null },
          totalCount: rows.length,
        },
      },
    },
  };
}

describe("operator incident list realtime", () => {
  beforeEach(() => mocks.setPlatformOperator(true));

  it("updates a platform incident in place", async () => {
    mocks.fetchPlatformIncidents.mockResolvedValue(platformIncidentList([platformIncident]));
    render(AdminIncidentsPage);
    const link = await screen.findByRole("link", { name: "Kafka broker down" });
    expect(link.getAttribute("href")).toBe("/admin/incidents/inc-platform");
    expect(
      within(link.closest("tr") as HTMLElement)
        .getByRole("link", { name: "View incident: Kafka broker down" })
        .getAttribute("href")
    ).toBe("/admin/incidents/inc-platform");
    expect(mocks.listen).toHaveBeenCalled();

    mocks.emitUpdate(
      incidentUpdate({
        incidentId: "inc-platform",
        clusterId: "core-eu",
        status: "ACKNOWLEDGED",
        title: "Kafka broker still down",
      })
    );
    const renamed = await screen.findByRole("link", { name: "Kafka broker still down" });
    expect(within(renamed.closest("tr") as HTMLElement).getByText("Acknowledged")).toBeTruthy();
    expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(1);
  });

  it("refetches with the applied filters when another tenant's incident opens", async () => {
    const opened = { ...incident, id: "inc-b", tenantId: "tenant-b", title: "Tenant B edge down" };
    mocks.fetchPlatformIncidents
      .mockResolvedValueOnce(platformIncidentList([platformIncident]))
      .mockResolvedValueOnce(platformIncidentList([platformIncident]))
      .mockResolvedValue(platformIncidentList([platformIncident, opened]));
    render(AdminIncidentsPage);
    await screen.findByRole("link", { name: "Kafka broker down" });
    await fireEvent.input(screen.getByLabelText("Tenant ID"), { target: { value: " tenant-b " } });
    await fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(2));
    // Typed but not applied: the refetch keeps the applied tenant filter.
    await fireEvent.input(screen.getByLabelText("Tenant ID"), { target: { value: "tenant-c" } });

    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-b", change: "opened" }));
    expect(await screen.findByRole("link", { name: "Tenant B edge down" })).toBeTruthy();
    expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(3);
    expect(mocks.fetchPlatformIncidents.mock.calls[2][0].variables).toEqual({
      first: 50,
      after: null,
      filter: {
        scope: null,
        statuses: ["FIRING", "ACKNOWLEDGED"],
        tenantId: "tenant-b",
        clusterId: null,
      },
    });
  });

  it("refetches when a listed incident leaves the status filter", async () => {
    mocks.fetchPlatformIncidents
      .mockResolvedValueOnce(platformIncidentList([platformIncident]))
      .mockResolvedValue(platformIncidentList([]));
    render(AdminIncidentsPage);
    await screen.findByRole("link", { name: "Kafka broker down" });

    mocks.emitUpdate(
      incidentUpdate({ incidentId: "inc-platform", status: "RESOLVED", change: "resolved" })
    );
    expect(await screen.findByText("No incidents match these filters")).toBeTruthy();
    expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(2);
  });

  it("refetches the list after the WebSocket reconnects", async () => {
    mocks.fetchPlatformIncidents.mockResolvedValue(platformIncidentList([platformIncident]));
    render(AdminIncidentsPage);
    await screen.findByRole("link", { name: "Kafka broker down" });

    notifyRealtimeReconnected();
    await waitFor(() => expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("link", { name: "Kafka broker down" })).toBeTruthy();
  });

  it("keeps the rows shown when a background refetch fails", async () => {
    mocks.fetchPlatformIncidents
      .mockResolvedValueOnce(platformIncidentList([platformIncident]))
      .mockRejectedValue(new Error("offline"));
    render(AdminIncidentsPage);
    await screen.findByRole("link", { name: "Kafka broker down" });

    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-new", change: "opened" }));
    await waitFor(() => expect(mocks.fetchPlatformIncidents).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("link", { name: "Kafka broker down" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("operator incident detail realtime", () => {
  beforeEach(() => {
    mocks.setPlatformOperator(true);
    page.params = { id: "inc-1" };
  });

  it("reloads for changes to its incident and stays shown after a scope change", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    render(AdminIncidentPage);
    await screen.findByText("Timeline");
    expect(mocks.listen).toHaveBeenCalled();

    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-other", change: "note" }));
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(mocks.fetchIncident).toHaveBeenCalledTimes(1);

    mocks.emitUpdate(incidentUpdate({ change: "scope_changed" }));
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("Timeline")).toBeTruthy();
    expect(screen.queryByText("Incident no longer available")).toBeNull();
  });

  it("reloads after the WebSocket reconnects", async () => {
    mocks.fetchIncident.mockResolvedValue(detail());
    render(AdminIncidentPage);
    await screen.findByText("Timeline");

    notifyRealtimeReconnected();
    await waitFor(() => expect(mocks.fetchIncident).toHaveBeenCalledTimes(2));
  });
});

describe("firing incident badge", () => {
  it("follows incident updates and reconnects without polling", async () => {
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    mocks.fetchIncidents
      .mockResolvedValueOnce(incidentList([incident], 1))
      .mockResolvedValueOnce(incidentList([incident], 3))
      .mockResolvedValue(incidentList([], 0));
    render(NotificationBell);
    expect(await screen.findByText("1")).toBeTruthy();
    expect(mocks.fetchIncidents.mock.calls[0][0].variables).toEqual({
      first: 1,
      filter: { statuses: ["FIRING"] },
    });

    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-2", change: "opened" }));
    mocks.emitUpdate(incidentUpdate({ incidentId: "inc-3", change: "opened" }));
    expect(await screen.findByText("3")).toBeTruthy();
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(2);

    notifyRealtimeReconnected();
    await waitFor(() => expect(screen.queryByText("3")).toBeNull());
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(3);

    const polls = setIntervalSpy.mock.calls.filter((call) => call[1] === 60_000);
    expect(polls).toHaveLength(1);
    (polls[0][0] as () => void)();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(mocks.fetchIncidents).toHaveBeenCalledTimes(3);
  });
});
