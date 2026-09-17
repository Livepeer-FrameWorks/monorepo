import { browser } from "$app/environment";
import { GetIncidentsStore, IncidentUpdatesStore } from "$houdini";
import { onRealtimeReconnect } from "$lib/houdini/reconnect";
import type { IncidentUpdate } from "$lib/incidents";

/** Bursts (an alert group resolving, a cluster changing owner) arrive as one batch. */
const INCIDENT_UPDATE_DEBOUNCE_MS = 300;

interface IncidentUpdateBatch {
  events: IncidentUpdate[];
  /** The WebSocket reconnected, so changes may have been missed. */
  reconnected: boolean;
}

type BatchHandler = (batch: IncidentUpdateBatch) => void;

class IncidentUpdateWatcher {
  private events: IncidentUpdate[] = [];
  private reconnected = false;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(private readonly handler: BatchHandler) {}

  push(event: IncidentUpdate) {
    this.events.push(event);
    this.schedule();
  }

  markReconnected() {
    this.reconnected = true;
    this.schedule();
  }

  stop() {
    clearTimeout(this.timer);
    this.timer = undefined;
  }

  private schedule() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.flush(), INCIDENT_UPDATE_DEBOUNCE_MS);
  }

  private flush() {
    this.timer = undefined;
    const batch = { events: this.events, reconnected: this.reconnected };
    this.events = [];
    this.reconnected = false;
    this.handler(batch);
  }
}

const watchers = new Set<IncidentUpdateWatcher>();
let updatesStore: IncidentUpdatesStore | null = null;
let stopUpdates: (() => void) | null = null;
let stopReconnect: (() => void) | null = null;
// Houdini re-emits the last result when its fetching flag changes; identity
// tells a new event from a repeat.
let lastEvent: IncidentUpdate | null = null;
let lastErrors: unknown = null;

function startUpdates() {
  updatesStore ??= new IncidentUpdatesStore();
  // Svelte stores call a new subscriber synchronously with their current value,
  // which is an event from before this subscription started.
  let replaying = true;
  stopUpdates = updatesStore.subscribe((result) => {
    if (replaying) {
      lastEvent = result.data?.liveIncidentUpdates ?? lastEvent;
      lastErrors = result.errors ?? lastErrors;
      return;
    }
    if (result.errors?.length) {
      if (result.errors !== lastErrors) {
        lastErrors = result.errors;
        console.warn("[IncidentUpdates] Subscription error:", result.errors);
      }
      return;
    }
    const event = result.data?.liveIncidentUpdates;
    if (!event || event === lastEvent) return;
    lastEvent = event;
    for (const watcher of watchers) watcher.push(event);
  });
  replaying = false;
  stopReconnect = onRealtimeReconnect(() => {
    for (const watcher of watchers) watcher.markReconnected();
  });
  void Promise.resolve(updatesStore.listen()).catch((error: unknown) => {
    console.warn("[IncidentUpdates] Could not subscribe:", error);
  });
}

function stopUpdatesIfIdle() {
  if (watchers.size > 0 || !updatesStore) return;
  stopUpdates?.();
  stopReconnect?.();
  stopUpdates = null;
  stopReconnect = null;
  void Promise.resolve(updatesStore.unlisten()).catch(() => null);
}

/**
 * Delivers debounced batches of incident changes, plus a batch after every
 * WebSocket reconnect. All callers share one `liveIncidentUpdates` operation on
 * the app's GraphQL WebSocket. The Gateway decides the scope: the caller's
 * tenant, or every incident for a platform operator, so tenant views must
 * treat events for incidents they do not show as possibly unrelated. Returns
 * the stop function.
 */
export function watchIncidentUpdates(handler: BatchHandler): () => void {
  if (!browser) return () => {};
  const watcher = new IncidentUpdateWatcher(handler);
  watchers.add(watcher);
  if (watchers.size === 1) startUpdates();
  return () => {
    if (!watchers.delete(watcher)) return;
    watcher.stop();
    stopUpdatesIfIdle();
  };
}

let firingCount = $state(0);
let countRequest = 0;

export const incidentCounts = {
  get firing() {
    return firingCount;
  },
};

/** Best-effort: a tenant without incident access keeps a zero count. */
export async function refreshFiringIncidentCount(): Promise<void> {
  if (!browser) return;
  const current = ++countRequest;
  try {
    const result = await new GetIncidentsStore().fetch({
      policy: "NetworkOnly",
      variables: { first: 1, filter: { statuses: ["FIRING"] } },
    });
    const total = result.data?.incidentsConnection?.totalCount;
    if (current === countRequest && typeof total === "number") firingCount = total;
  } catch {
    // The count is a hint; the incidents page reports load failures.
  }
}

/**
 * Keeps the firing count current: loads it now, then again after incident
 * changes and reconnects. Returns the stop function.
 */
export function syncFiringIncidentCount(): () => void {
  if (!browser) return () => {};
  void refreshFiringIncidentCount();
  return watchIncidentUpdates(() => void refreshFiringIncidentCount());
}
