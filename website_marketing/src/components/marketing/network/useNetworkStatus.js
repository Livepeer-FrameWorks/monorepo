import { useState, useEffect, useRef } from "react";
import config from "../../../config";

const QUERY = `query GetNetworkStatus {
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
      maxStreams
      currentStreams
      maxViewers
      currentViewers
      maxBandwidthMbps
      currentBandwidthMbps
      services
    }
    peerConnections {
      sourceCluster
      targetCluster
      connected
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

const POLL_INTERVAL = 30_000;

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

        const res = await fetch(config.gatewayUrl, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            query: QUERY,
            operationName: "GetNetworkStatus",
          }),
          signal: abortRef.current.signal,
        });

        if (!res.ok) throw new Error(`HTTP ${res.status}`);

        const json = await res.json();
        if (!active) return;

        if (json.data?.networkStatus) {
          setData(json.data.networkStatus);
          setError(null);
        } else {
          throw new Error(json.errors?.[0]?.message || "No data");
        }
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
