import { browser } from "$app/environment";

const GQL_ENDPOINT = "/api/graphql";

export interface SkipperReport {
  id: string;
  trigger: string;
  summary: string;
  metricsReviewed: string[];
  rootCause: string;
  recommendations: Array<{ text: string; confidence: string }>;
  createdAt: string;
  readAt: string | null;
}

interface ReportsResponse {
  data?: {
    skipperReports: {
      nodes: SkipperReport[];
      totalCount: number;
      unreadCount: number;
    };
  };
}

interface UnreadCountResponse {
  data?: {
    skipperUnreadReportCount: number;
  };
}

interface MarkReadResponse {
  data?: {
    markSkipperReportsRead: number;
  };
}

async function gql<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const res = await fetch(GQL_ENDPOINT, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify({ query, variables }),
  });
  if (!res.ok) throw new Error(`GraphQL request failed: ${res.status}`);
  return res.json();
}

function createNotificationStore() {
  let reports = $state<SkipperReport[]>([]);
  let totalCount = $state(0);
  let unreadCount = $state(0);
  let loading = $state(false);
  let panelOpen = $state(false);
  let initialized = $state(false);

  async function loadReports(limit = 20, offset = 0) {
    if (!browser) return;
    loading = true;
    try {
      const resp = await gql<ReportsResponse>(
        `query GetSkipperReports($limit: Int, $offset: Int) {
          skipperReports(limit: $limit, offset: $offset) {
            nodes {
              id trigger summary metricsReviewed rootCause
              recommendations { text confidence }
              createdAt readAt
            }
            totalCount unreadCount
          }
        }`,
        { limit, offset }
      );
      if (resp.data) {
        reports = resp.data.skipperReports.nodes;
        totalCount = resp.data.skipperReports.totalCount;
        unreadCount = resp.data.skipperReports.unreadCount;
      }
      initialized = true;
    } catch (err) {
      console.error("Failed to load Skipper reports:", err);
    } finally {
      loading = false;
    }
  }

  async function refreshUnreadCount() {
    if (!browser) return;
    try {
      const resp = await gql<UnreadCountResponse>(`query { skipperUnreadReportCount }`);
      if (resp.data) {
        unreadCount = resp.data.skipperUnreadReportCount;
      }
    } catch {
      // silent — badge is best-effort
    }
  }

  async function markAllRead() {
    if (!browser) return;
    try {
      await gql<MarkReadResponse>(
        `mutation MarkSkipperReportsRead($ids: [ID!]) {
          markSkipperReportsRead(ids: $ids)
        }`,
        { ids: null }
      );
      reports = reports.map((r) => (r.readAt ? r : { ...r, readAt: new Date().toISOString() }));
      unreadCount = 0;
    } catch (err) {
      console.error("Failed to mark reports read:", err);
    }
  }

  async function markRead(ids: string[]) {
    if (!browser || ids.length === 0) return;
    try {
      const resp = await gql<MarkReadResponse>(
        `mutation MarkSkipperReportsRead($ids: [ID!]) {
          markSkipperReportsRead(ids: $ids)
        }`,
        { ids }
      );
      if (resp.data) {
        const idSet = new Set(ids);
        reports = reports.map((r) =>
          idSet.has(r.id) && !r.readAt ? { ...r, readAt: new Date().toISOString() } : r
        );
        unreadCount = Math.max(0, unreadCount - resp.data.markSkipperReportsRead);
      }
    } catch (err) {
      console.error("Failed to mark reports read:", err);
    }
  }

  function handleRealtimeEvent() {
    refreshUnreadCount();
    loadReports();
  }

  function togglePanel() {
    panelOpen = !panelOpen;
  }

  function closePanel() {
    panelOpen = false;
  }

  return {
    get reports() {
      return reports;
    },
    get totalCount() {
      return totalCount;
    },
    get unreadCount() {
      return unreadCount;
    },
    get loading() {
      return loading;
    },
    get panelOpen() {
      return panelOpen;
    },
    get initialized() {
      return initialized;
    },

    loadReports,
    refreshUnreadCount,
    markAllRead,
    markRead,
    handleRealtimeEvent,
    togglePanel,
    closePanel,
  };
}

export const notificationStore = createNotificationStore();
