import { useEffect, useMemo, useState } from "react";
import config from "../../config";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  HeadlineStack,
  MarketingBand,
  MarketingHero,
  MarketingGridSeam,
  MarketingFinalCTA,
  MarketingScrollProgress,
  SectionDivider,
} from "@/components/marketing";
import { Section, SectionContainer } from "@/components/ui/section";

const pill = (label, cls) => <Badge className={cls}>{label}</Badge>;

const formatTime = (ts) => {
  try {
    const d = new Date(ts);
    if (isNaN(+d)) return "Unknown";
    return d.toLocaleString();
  } catch {
    return "Unknown";
  }
};

const computeServiceRollups = (instances) => {
  const byService = new Map();
  for (const inst of instances) {
    const sid = inst.serviceId || "unknown";
    const arr = byService.get(sid) || [];
    arr.push(inst);
    byService.set(sid, arr);
  }
  const rollups = [];
  for (const [serviceId, list] of byService) {
    const total = list.length;
    const healthyCount = list.filter(
      (x) =>
        String(x.status).toLowerCase() === "healthy" ||
        String(x.status).toLowerCase() === "live" ||
        String(x.status).toLowerCase() === "ready"
    ).length;
    const last = list.reduce(
      (acc, x) => Math.max(acc, x.lastHealthCheck ? Date.parse(x.lastHealthCheck) : 0),
      0
    );
    let status = "operational";
    if (healthyCount === 0) status = "down";
    else if (healthyCount < total) status = "degraded";
    rollups.push({ serviceId, total, healthy: healthyCount, status, lastHealthCheck: last });
  }
  return rollups.sort((a, b) => a.serviceId.localeCompare(b.serviceId));
};

const overallFromRollups = (rollups) => {
  if (!rollups.length)
    return {
      label: "Unknown",
      cls: "bg-[hsl(var(--brand-comment)/0.2)] text-brand-muted border-[hsl(var(--brand-comment)/0.4)]",
    };
  const anyDown = rollups.some((r) => r.status === "down");
  const anyDegraded = rollups.some((r) => r.status === "degraded");
  if (anyDown)
    return { label: "Partial Outage", cls: "bg-red-500/20 text-red-400 border-red-500/40" };
  if (anyDegraded)
    return {
      label: "Degraded Performance",
      cls: "bg-yellow-500/20 text-yellow-400 border-yellow-500/40",
    };
  return {
    label: "All Systems Operational",
    cls: "bg-green-500/20 text-green-400 border-green-500/40",
  };
};

const statusHeroAccents = [
  {
    kind: "beam",
    x: 18,
    y: 40,
    width: "clamp(24rem, 44vw, 36rem)",
    height: "clamp(18rem, 32vw, 26rem)",
    rotate: -19,
    fill: "linear-gradient(138deg, rgba(98, 145, 218, 0.33), rgba(24, 32, 56, 0.21))",
    opacity: 0.54,
    radius: "47px",
  },
  {
    kind: "beam",
    x: 76,
    y: 26,
    width: "clamp(20rem, 38vw, 30rem)",
    height: "clamp(16rem, 28vw, 23rem)",
    rotate: 17,
    fill: "linear-gradient(148deg, rgba(64, 188, 243, 0.29), rgba(19, 25, 43, 0.19))",
    opacity: 0.5,
    radius: "43px",
  },
];

const StatusPage = () => {
  const [instances, setInstances] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [lastUpdated, setLastUpdated] = useState(0);

  const rollups = useMemo(() => computeServiceRollups(instances), [instances]);
  const overall = useMemo(() => overallFromRollups(rollups), [rollups]);
  const lastCheck = useMemo(() => {
    const last = Math.max(
      ...instances.map((i) => (i.lastHealthCheck ? Date.parse(i.lastHealthCheck) : 0)),
      0
    );
    return last || lastUpdated;
  }, [instances, lastUpdated]);

  const fetchHealth = async () => {
    setLoading(true);
    setError("");
    try {
      const res = await fetch(config.gatewayUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          query:
            "query { serviceInstancesHealth { instanceId serviceId clusterId protocol host port healthEndpoint status lastHealthCheck } }",
        }),
      });
      if (!res.ok) throw new Error(`Gateway status ${res.status}`);
      const json = await res.json();
      if (json.errors?.length) throw new Error(json.errors[0]?.message || "GraphQL error");
      const list = json.data?.serviceInstancesHealth || [];
      setInstances(Array.isArray(list) ? list : []);
      setLastUpdated(Date.now());
    } catch (e) {
      setError(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchHealth();
    const id = setInterval(fetchHealth, 30000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/status"
        eyebrow="Platform health"
        title="Status"
        description="Public Beta: demo may be intermittently offline."
        align="center"
        surface="gradient"
        accents={statusHeroAccents}
      >
        <div className="flex flex-wrap items-center justify-center gap-3">
          {pill(overall.label, overall.cls)}
          <span className="text-muted-foreground text-sm">
            {loading ? "Refreshing…" : `Last check: ${formatTime(lastCheck)}`}
          </span>
          <Button onClick={fetchHealth} variant="secondary" size="sm">
            Refresh
          </Button>
        </div>
        {error ? <div className="text-sm text-destructive text-center">{error}</div> : null}
      </MarketingHero>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingBand
            preset="foundation"
            texturePattern="pinlines"
            textureNoise="grain"
            textureBeam="soft"
            textureMotion="drift"
            textureStrength="soft"
          >
            <HeadlineStack
              eyebrow="Health"
              title="Services"
              subtitle="Live rollup from service instance checks."
              align="left"
              underlineAlign="start"
            />
            {rollups.length === 0 ? (
              <div className="px-6 pb-6 text-muted-foreground">No health data yet.</div>
            ) : (
              <MarketingGridSeam columns={2} stackAt="md" gap="tight" surface="glass">
                {rollups.map((r) => (
                  <div key={r.serviceId} className="flex h-full flex-col gap-2">
                    <div className="flex items-start justify-between gap-3">
                      <div className="text-base font-semibold text-foreground">{r.serviceId}</div>
                      {r.status === "operational" &&
                        pill("Operational", "bg-green-500/20 text-green-400 border-green-500/40")}
                      {r.status === "degraded" &&
                        pill("Degraded", "bg-yellow-500/20 text-yellow-400 border-yellow-500/40")}
                      {r.status === "down" &&
                        pill("Down", "bg-red-500/20 text-red-400 border-red-500/40")}
                    </div>
                    <div className="text-sm text-muted-foreground">
                      Instances: {r.healthy}/{r.total} healthy
                    </div>
                    <div className="text-xs text-muted-foreground">
                      Last health: {formatTime(r.lastHealthCheck)}
                    </div>
                  </div>
                ))}
              </MarketingGridSeam>
            )}
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingBand surface="midnight" tone="neutral" texture="none" density="compact" flush>
            <HeadlineStack
              eyebrow="Incidents"
              title="Recent incidents"
              subtitle="No incidents reported."
              align="left"
              underlineAlign="start"
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <MarketingFinalCTA
          variant="band"
          eyebrow="Next steps"
          title="Ready to get started?"
          description="Monitor your infrastructure health in real-time."
          primaryAction={{
            label: "Start Free",
            href: config.appUrl,
            external: true,
          }}
          secondaryAction={[
            {
              label: "View Documentation",
              to: "/docs",
            },
            {
              label: "Talk to our team",
              to: "/contact",
            },
          ]}
        />
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default StatusPage;
