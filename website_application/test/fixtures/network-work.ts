import type { WorkSummary } from "../../../packages/map-core/network-work";

export function workFixture(overrides: Partial<WorkSummary> = {}): WorkSummary {
  return {
    resource_id: "0xabc",
    ip: "192.0.2.1",
    region: "eu",
    profile_key: "720p",
    accounted_attempts: 1,
    unaccounted_samples: 0,
    dispatched_attempts: 1,
    dispatched_source_ms: "60000",
    returned_attempts: 1,
    returned_pixels: "9007199254740993",
    accepted_segments: 1,
    accepted_source_ms: "60000",
    accepted_pixels: "100",
    accepted_rendition_ms: "180000",
    service_charge_samples: 1,
    service_charge: { numerator: "1", denominator: "3", currency: "ETH", unit: "wei" },
    ticket_credit_samples: 1,
    ticket_credit_sent: { numerator: "7", denominator: "2", currency: "ETH", unit: "wei" },
    tickets_sent: "2",
    dispatched_duration_samples: 1,
    accepted_duration_samples: 1,
    accepted_output_samples: 1,
    ...overrides,
  };
}
