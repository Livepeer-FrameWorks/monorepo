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
  if (!ts) return "Unknown";
  try {
    const d = new Date(ts);
    if (isNaN(+d)) return "Unknown";
    return d.toLocaleString();
  } catch {
    return "Unknown";
  }
};

const computeClusterRollups = (clusters) => {
  return clusters
    .map((cluster) => {
      const statusValue = String(cluster.status || "unknown").toLowerCase();
      let status = "operational";
      if (statusValue === "down" || statusValue === "unhealthy") status = "down";
      else if (statusValue !== "healthy" && statusValue !== "operational") status = "degraded";
      return {
        id: cluster.clusterId,
        name: cluster.name || cluster.clusterId,
        type: cluster.clusterType || "cluster",
        total: cluster.nodeCount || 0,
        healthy: cluster.healthyNodeCount || 0,
        status,
        currentStreams: cluster.currentStreams || 0,
        currentViewers: cluster.currentViewers || 0,
        egressMbps: cluster.egressMbps || 0,
        ingressMbps: cluster.ingressMbps || 0,
      };
    })
    .sort((a, b) => a.name.localeCompare(b.name));
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
  const [clusters, setClusters] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [lastUpdated, setLastUpdated] = useState(0);

  const rollups = useMemo(() => computeClusterRollups(clusters), [clusters]);
  const overall = useMemo(() => overallFromRollups(rollups), [rollups]);
  const lastCheck = useMemo(() => lastUpdated, [lastUpdated]);

  const fetchHealth = async () => {
    setLoading(true);
    setError("");
    try {
      const res = await fetch(config.gatewayUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          query: `query PublicStatus {
            networkStatus {
              clusters {
                clusterId
                name
                clusterType
                status
                nodeCount
                healthyNodeCount
                currentStreams
                currentViewers
                egressMbps
                ingressMbps
              }
            }
          }`,
          operationName: "PublicStatus",
        }),
      });
      if (!res.ok) throw new Error(`Gateway status ${res.status}`);
      const json = await res.json();
      if (json.errors?.length) throw new Error(json.errors[0]?.message || "GraphQL error");
      const list = json.data?.networkStatus?.clusters || [];
      setClusters(Array.isArray(list) ? list : []);
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
              title="Coverage"
              subtitle="Live public rollup from cluster topology and aggregate load counters."
              align="left"
              underlineAlign="start"
            />
            {rollups.length === 0 ? (
              <div className="px-6 pb-6 text-muted-foreground">No health data yet.</div>
            ) : (
              <MarketingGridSeam columns={2} stackAt="md" gap="tight" surface="glass">
                {rollups.map((r) => (
                  <div key={r.id} className="flex h-full flex-col gap-2">
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div className="text-base font-semibold text-foreground">{r.name}</div>
                        <div className="text-xs uppercase tracking-[0.14em] text-muted-foreground">
                          {r.type}
                        </div>
                      </div>
                      {r.status === "operational" &&
                        pill("Operational", "bg-green-500/20 text-green-400 border-green-500/40")}
                      {r.status === "degraded" &&
                        pill("Degraded", "bg-yellow-500/20 text-yellow-400 border-yellow-500/40")}
                      {r.status === "down" &&
                        pill("Down", "bg-red-500/20 text-red-400 border-red-500/40")}
                    </div>
                    <div className="text-sm text-muted-foreground">
                      Nodes: {r.healthy}/{r.total} active
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {r.currentStreams} live streams · {r.currentViewers} viewers · {r.egressMbps}{" "}
                      Mbps egress · {r.ingressMbps} Mbps ingress
                    </div>
                  </div>
                ))}
              </MarketingGridSeam>
            )}
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer className="max-w-3xl space-y-6">
          <HeadlineStack
            eyebrow="Methodology"
            title="How we measure health"
            subtitle="Every status above is derived from live signals, not a hand-edited dashboard."
            align="left"
            underlineAlign="start"
          />
          <div className="space-y-4 text-sm leading-relaxed text-muted-foreground">
            <p>
              The rollup on this page is generated in real time from FrameWorks' own control plane.
              Each edge cluster continuously reports node health, active streams, concurrent
              viewers, and ingress and egress throughput. We aggregate those signals per cluster and
              roll them into a single platform-wide state that refreshes every thirty seconds.
            </p>
            <p>
              A cluster is marked <strong>operational</strong> when its nodes are healthy and
              accepting traffic, <strong>degraded</strong> when some capacity is impaired but
              delivery continues, and <strong>down</strong> when it can no longer serve viewers.
              Because the platform routes around unhealthy nodes, a degraded cluster does not
              necessarily mean your streams are affected—routing steers viewers to the nearest
              healthy edge.
            </p>
            <p>
              This is a public beta, so the demo environment may be intermittently offline for
              maintenance or upgrades. The numbers here reflect the live network as the control
              plane sees it; if you operate a hybrid or self-hosted deployment, your own clusters
              report into the same health model.
            </p>
          </div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingBand surface="midnight" tone="neutral" texture="none" density="compact" flush>
            <HeadlineStack
              eyebrow="Incidents"
              title="Recent incidents"
              subtitle="No incidents reported. Subscribe to updates by contacting our team if you operate production workloads."
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
          title="Build on observable infrastructure"
          description="Get QoE metrics and routing visibility on every plan."
          primaryAction={{
            label: "Start Free",
            href: config.appUrl,
            external: true,
          }}
          secondaryAction={[
            {
              label: "Browse Docs",
              href: config.docsUrl,
              external: true,
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
