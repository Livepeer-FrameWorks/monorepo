import { browser } from "$app/environment";
import {
  GetSkipperReportsStore,
  GetSkipperUnreadReportCountStore,
  MarkSkipperReportsReadStore,
  type GetSkipperReports$result,
} from "$houdini";

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

type SkipperReportNode = GetSkipperReports$result["skipperReports"]["nodes"][number];

function toSkipperReport(node: SkipperReportNode): SkipperReport {
  return {
    id: node.id,
    trigger: node.trigger,
    summary: node.summary,
    metricsReviewed: [...node.metricsReviewed],
    rootCause: node.rootCause,
    recommendations: node.recommendations.map((rec) => ({
      text: rec.text,
      confidence: rec.confidence,
    })),
    createdAt: node.createdAt,
    readAt: node.readAt,
  };
}

function createNotificationStore() {
  const reportsQuery = new GetSkipperReportsStore();
  const unreadCountQuery = new GetSkipperUnreadReportCountStore();
  const markReadMutation = new MarkSkipperReportsReadStore();

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
      const resp = await reportsQuery.fetch({
        policy: "NetworkOnly",
        variables: { limit, offset },
      });
      if (resp.errors?.length) {
        throw new Error(resp.errors[0].message);
      }

      const skipperReports = resp.data?.skipperReports;
      if (skipperReports) {
        reports = skipperReports.nodes.map(toSkipperReport);
        totalCount = skipperReports.totalCount;
        unreadCount = skipperReports.unreadCount;
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
      const resp = await unreadCountQuery.fetch({ policy: "NetworkOnly" });
      if (resp.errors?.length) {
        throw new Error(resp.errors[0].message);
      }

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
      const resp = await markReadMutation.mutate({ ids: null });
      if (resp.errors?.length) {
        throw new Error(resp.errors[0].message);
      }

      const markedCount = resp.data?.markSkipperReportsRead ?? 0;
      if (markedCount <= 0) return;

      const now = new Date().toISOString();
      reports = reports.map((r) => (r.readAt ? r : { ...r, readAt: now }));
      unreadCount = Math.max(0, unreadCount - markedCount);
    } catch (err) {
      console.error("Failed to mark reports read:", err);
    }
  }

  async function markRead(ids: string[]) {
    if (!browser || ids.length === 0) return;
    try {
      const resp = await markReadMutation.mutate({ ids });
      if (resp.errors?.length) {
        throw new Error(resp.errors[0].message);
      }

      const markedCount = resp.data?.markSkipperReportsRead ?? 0;
      if (markedCount <= 0) return;

      const now = new Date().toISOString();
      const idSet = new Set(ids);
      reports = reports.map((r) => (idSet.has(r.id) && !r.readAt ? { ...r, readAt: now } : r));
      unreadCount = Math.max(0, unreadCount - markedCount);
    } catch (err) {
      console.error("Failed to mark reports read:", err);
    }
  }

  function handleRealtimeEvent() {
    void refreshUnreadCount();
    void loadReports();
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
