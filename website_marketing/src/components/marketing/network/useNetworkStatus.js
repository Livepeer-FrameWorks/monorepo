import { useState, useEffect, useRef } from "react";
import config from "../../../config";

const NETWORK_STATUS_QUERY = `query GetNetworkStatus {
  networkStatus {
    clusters {
      clusterId
      name
      region
      latitude
      longitude
      nodeCount
      healthyNodeCount
      peerCount
      status
      clusterType
      shortDescription
      currentStreams
      currentViewers
      egressMbps
      egressCapacityMbps
      ingressMbps
      services
    }
    peerConnections {
      sourceCluster
      targetCluster
      connected
      connectionType
    }
    nodes {
      nodeId
      name
      nodeType
      latitude
      longitude
      status
      clusterId
    }
    serviceInstances {
      instanceId
      serviceId
      clusterId
      nodeId
      status
      healthStatus
    }
    totalNodes
    healthyNodes
    updatedAt
  }
}`;

const VANTAGES_QUERY = `query GetOrchestratorVantages {
  orchestratorVantages {
    orchAddr
    resolvedIp
    gatewayId
    gatewayRegion
    latitude
    longitude
    geoSource
    latestLatencyMs
    score
    dialedRecently
  }
}`;

const POLL_INTERVAL = 30_000;

async function fetchGql(query, operationName, signal) {
  const res = await fetch(config.gatewayUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ query, operationName }),
    signal,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export function useNetworkStatus() {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(true);
  const abortRef = useRef(null);

  useEffect(() => {
    let active = true;

    const fetchData = async () => {
      try {
        abortRef.current?.abort();
        abortRef.current = new AbortController();
        const signal = abortRef.current.signal;

        // Two parallel queries so an error in one (e.g. no Livepeer gateway
        // tenants for orchestratorVantages) cannot null-bubble through the
        // root and starve the map of cluster data.
        const [statusResult, vantageResult] = await Promise.allSettled([
          fetchGql(NETWORK_STATUS_QUERY, "GetNetworkStatus", signal),
          fetchGql(VANTAGES_QUERY, "GetOrchestratorVantages", signal),
        ]);

        if (!active) return;

        const status =
          statusResult.status === "fulfilled" ? statusResult.value?.data?.networkStatus : null;
        if (!status) {
          const reason =
            statusResult.status === "rejected"
              ? statusResult.reason
              : new Error(statusResult.value?.errors?.[0]?.message || "networkStatus unavailable");
          throw reason;
        }

        const vantages =
          vantageResult.status === "fulfilled"
            ? (vantageResult.value?.data?.orchestratorVantages ?? [])
            : [];

        setData({
          ...status,
          clusters: status.clusters ?? [],
          nodes: status.nodes ?? [],
          peerConnections: status.peerConnections ?? [],
          serviceInstances: status.serviceInstances ?? [],
          orchestratorVantages: vantages,
        });
        setError(null);
      } catch (err) {
        if (!active || err.name === "AbortError") return;
        setError(err);
      } finally {
        if (active) setLoading(false);
      }
    };

    fetchData();
    const id = setInterval(fetchData, POLL_INTERVAL);

    return () => {
      active = false;
      clearInterval(id);
      abortRef.current?.abort();
    };
  }, []);

  return { data, error, loading };
}
