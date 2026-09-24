// Cost model behind the pricing calculator. Mirrors Purser's rating: delivered
// minutes above the plan allowance at the plan rate, plus time-weighted cold
// storage per GiB-month. Plan prices come from the generated pricing catalog.

const BYTES_PER_GIB = 2 ** 30;

// Live ABR ladder produced for every tier (billing_tiers.yaml processes_vod).
// VOD uploads keep these renditions (up to the source height) and drop the
// source video; live recordings keep the source only.
export const ABR_LADDER = [
  { height: 360, mbps: 0.9 },
  { height: 480, mbps: 1.6 },
  { height: 720, mbps: 3.2 },
  { height: 1080, mbps: 6.5 },
];

export const SOURCE_QUALITIES = [
  { id: "720p", label: "720p", height: 720, mbps: 3 },
  { id: "1080p", label: "1080p", height: 1080, mbps: 6 },
  { id: "1440p", label: "1440p", height: 1440, mbps: 10 },
  { id: "2160p", label: "4K", height: 2160, mbps: 20 },
];

// Minutes above this are quoted by sales rather than self-serve tiers.
export const ENTERPRISE_MINUTES_THRESHOLD = 5_000_000;

export const gibPerHour = (mbps) => (mbps * 1e6 * 3600) / 8 / BYTES_PER_GIB;

export const ladderMbps = (sourceHeight) =>
  ABR_LADDER.filter((r) => r.height <= sourceHeight).reduce((sum, r) => sum + r.mbps, 0);

// Integral of min(t, D) over [t0, t1]: GiB-days held per unit of daily intake
// when every addition is deleted D days after it was written.
const heldIntegral = (t0, t1, retentionDays) => {
  const d = retentionDays;
  if (d >= t1) return (t1 * t1 - t0 * t0) / 2;
  if (d <= t0) return d * (t1 - t0);
  return (d * d - t0 * t0) / 2 + d * (t1 - d);
};

// Average GiB held during `month` (1-based) when `gibPerMonth` is added at a
// steady rate from month 1 and kept for `retentionDays` (Infinity = forever).
// This is the quantity storage is billed on: GiB-seconds over the month.
export function averageHeldGiB(gibPerMonth, retentionDays, month, daysPerMonth) {
  if (gibPerMonth <= 0 || retentionDays <= 0) return 0;
  const perDay = gibPerMonth / daysPerMonth;
  const t0 = (month - 1) * daysPerMonth;
  const t1 = month * daysPerMonth;
  return (perDay * heldIntegral(t0, t1, retentionDays)) / daysPerMonth;
}

// GiB held at the end of `month`, which is what point-in-time caps check.
export function heldAtEndGiB(gibPerMonth, retentionDays, month, daysPerMonth) {
  if (gibPerMonth <= 0 || retentionDays <= 0) return 0;
  return (gibPerMonth / daysPerMonth) * Math.min(month * daysPerMonth, retentionDays);
}

export function estimateUsage(inputs, catalog, month) {
  const daysPerMonth = catalog.storageHoursPerMonth / 24;
  const broadcastMinutes = inputs.broadcastHours * 60;
  const liveViewerMinutes = broadcastMinutes * inputs.avgViewers;
  const onDemandMinutes = inputs.onDemandHours * 60;
  const restreamMinutes = broadcastMinutes * inputs.restreamTargets;
  // Self-hosted edges serve viewers; restream pushes leave from the platform.
  const offloaded = (liveViewerMinutes + onDemandMinutes) * (inputs.edgeOffloadPercent / 100);
  const deliveredMinutes = liveViewerMinutes + onDemandMinutes + restreamMinutes;
  const billableMinutes = deliveredMinutes - offloaded;

  const source = SOURCE_QUALITIES.find((q) => q.id === inputs.sourceQuality) ?? SOURCE_QUALITIES[1];
  const recordingGiBPerMonth = inputs.recordLive
    ? inputs.broadcastHours * gibPerHour(source.mbps)
    : 0;
  const vodGiBPerMonth = inputs.vodHours * gibPerHour(ladderMbps(source.height));

  const storageGiBMonths =
    averageHeldGiB(recordingGiBPerMonth, inputs.recordingRetentionDays, month, daysPerMonth) +
    averageHeldGiB(vodGiBPerMonth, inputs.vodRetentionDays, month, daysPerMonth);
  const storageHeldAtEndGiB =
    heldAtEndGiB(recordingGiBPerMonth, inputs.recordingRetentionDays, month, daysPerMonth) +
    heldAtEndGiB(vodGiBPerMonth, inputs.vodRetentionDays, month, daysPerMonth);

  return {
    month,
    liveViewerMinutes,
    onDemandMinutes,
    restreamMinutes,
    offloadedMinutes: offloaded,
    deliveredMinutes,
    billableMinutes,
    recordingGiBPerMonth,
    vodGiBPerMonth,
    storageGiBMonths,
    storageHeldAtEndGiB,
    sourceMbps: source.mbps,
    vodLadderMbps: ladderMbps(source.height),
  };
}

// Reasons a plan's hard caps would refuse this workload. Paid plans have none.
function planBlockers(tier, inputs, usage) {
  const limits = tier.limits;
  const reasons = [];
  if (
    tier.deliveredMinutes.unitPrice === 0 &&
    usage.billableMinutes > tier.deliveredMinutes.included
  ) {
    reasons.push(`${formatCount(tier.deliveredMinutes.included)} delivered minutes/month`);
  }
  if (limits.maxConcurrentViewers && inputs.avgViewers > limits.maxConcurrentViewers) {
    reasons.push(`${limits.maxConcurrentViewers} concurrent viewers`);
  }
  if (limits.maxConcurrentStreams && inputs.concurrentChannels > limits.maxConcurrentStreams) {
    reasons.push(`${limits.maxConcurrentStreams} live streams at once`);
  }
  if (limits.storageGiB && usage.storageHeldAtEndGiB > limits.storageGiB) {
    reasons.push(`${limits.storageGiB} GB stored`);
  }
  const longestRetention = Math.max(
    inputs.recordLive ? inputs.recordingRetentionDays : 0,
    inputs.vodHours > 0 ? inputs.vodRetentionDays : 0
  );
  if (limits.retentionDays && longestRetention > limits.retentionDays) {
    reasons.push(`${limits.retentionDays}-day retention`);
  }
  return reasons;
}

export function estimatePlans(inputs, catalog, month) {
  const usage = estimateUsage(inputs, catalog, month);
  const plans = catalog.tiers.map((tier) => {
    const overMinutes = Math.max(0, usage.billableMinutes - tier.deliveredMinutes.included);
    const overStorage = Math.max(0, usage.storageGiBMonths - tier.storage.included);
    const delivery = overMinutes * tier.deliveredMinutes.unitPrice;
    const storage = overStorage * tier.storage.unitPrice;
    const blockers = planBlockers(tier, inputs, usage);
    return {
      id: tier.id,
      name: tier.name,
      prepaid: tier.prepaid,
      base: tier.basePrice,
      delivery,
      storage,
      total: tier.basePrice + delivery + storage,
      eligible: blockers.length === 0,
      blockers,
    };
  });
  const recommended = plans
    .filter((p) => p.eligible)
    .reduce((best, p) => (best === null || p.total < best.total ? p : best), null);
  return { usage, plans, recommendedId: recommended?.id ?? null };
}

export function formatCount(n) {
  return new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(n);
}
