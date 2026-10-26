import type { ExplorerViewport } from "./network-explorer";
import type { WorkHistory } from "./network-work";

export type Location = {
  known: boolean;
  latitude: number;
  longitude: number;
  source: string;
  country?: string;
  city?: string;
};
export type Endpoint = {
  id: string;
  resource: { id: string; uri: string; active: boolean };
  ip: string;
  authority?: string;
  status: string;
  location: Location;
  last_success: string;
  last_check: string;
  cadence_seconds: number;
  capabilities?: string[];
  hardware?: string;
  price?: {
    numerator: string;
    denominator: string;
    currency: string;
    unit: string;
    observed_at: string;
  };
};
export type Cohort = {
  bucket: string;
  kind: string;
  resource_id: string;
  ip: string;
  region: string;
  profile_key: string;
  samples: number;
  qualified_samples: number;
  successes: number;
  failures: number;
  p50_ms: number;
  p95_ms: number;
  p99_ms: number;
  duration_samples: number;
  p95_duration_ratio: number;
  retries: number;
  fallbacks: number;
  deadline_misses: number;
};
export type Observation = {
  timestamp: string;
  kind: string;
  region: string;
  resource_id: string;
  uri?: string;
  ip: string;
  failure_kind?: string;
  disposition?: string;
  success: boolean;
  qualifying: boolean;
  cadence_seconds: number;
  price?: Endpoint["price"];
};
export type Response = {
  history_status?: string;
  inventory: {
    generated_at: string;
    endpoints: Endpoint[];
    resources: Array<{ id: string; uri: string; active: boolean }>;
    observers?: Array<{
      id: string;
      kind: string;
      region: string;
      location: Location;
      last_seen: string;
    }>;
  };
  history: {
    work?: WorkHistory;
    generated_at: string;
    start: string;
    end: string;
    interval_seconds: number;
    cohorts: Cohort[];
    summaries: Cohort[];
    truncated: boolean;
    rolled_up: boolean;
    details?: Array<Record<string, unknown>>;
    observations: Observation[];
  };
};
export type Options = {
  apiURL: string;
  infrastructureURL?: string;
  graphqlURL?: string;
  headers?: () => Record<string, string>;
  viewport?: ExplorerViewport;
  onQueryChange?: (query: URLSearchParams) => void;
};
export type InfrastructureNode = {
  nodeId: string;
  name: string;
  latitude: number | null;
  longitude: number | null;
  status: string;
};
export type GatewayCluster = {
  clusterId: string;
  name: string;
  region: string;
  latitude: number;
  longitude: number;
  services: string[];
};
