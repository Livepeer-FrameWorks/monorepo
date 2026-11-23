export function orchestratorLocation(vantage) {
  const lat = vantage.latitude;
  const lng = vantage.longitude;
  return Number.isFinite(lat) &&
    Number.isFinite(lng) &&
    Math.abs(lat) <= 90 &&
    Math.abs(lng) <= 180 &&
    !(lat === 0 && lng === 0)
    ? [lat, lng]
    : null;
}

export function dedupeOrchestratorVantages(vantages) {
  const byInstance = new Map();
  for (const vantage of vantages || []) {
    if (!vantage.dialedRecently) continue;
    const key = `${vantage.orchAddr}:${vantage.resolvedIp}`;
    const current = byInstance.get(key);
    const located = orchestratorLocation(vantage) !== null;
    const currentLocated = current ? orchestratorLocation(current) !== null : false;
    if (
      !current ||
      (located && !currentLocated) ||
      (located === currentLocated &&
        (Number(vantage.latestLatencyMs || 0) < Number(current.latestLatencyMs || 0) ||
          (Number(vantage.latestLatencyMs || 0) === Number(current.latestLatencyMs || 0) &&
            Number(vantage.score || 0) > Number(current.score || 0))))
    ) {
      byInstance.set(key, vantage);
    }
  }
  return [...byInstance.values()];
}
