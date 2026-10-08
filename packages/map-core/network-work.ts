export type Amount = {
  numerator: string;
  denominator: string;
  currency: string;
  unit: string;
};
export type WorkSummary = {
  resource_id: string;
  ip: string;
  region: string;
  profile_key: string;
  accounted_attempts: number;
  unaccounted_samples: number;
  dispatched_attempts: number;
  dispatched_source_ms: string;
  returned_attempts: number;
  returned_pixels: string;
  accepted_segments: number;
  accepted_source_ms: string;
  accepted_pixels: string;
  accepted_rendition_ms: string;
  service_charge_samples: number;
  service_charge?: Amount;
  ticket_credit_samples: number;
  ticket_credit_sent?: Amount;
  ticket_credit_incomplete?: boolean;
  tickets_sent: string;
  dispatched_duration_samples: number;
  accepted_duration_samples: number;
  accepted_output_samples: number;
};
export type WorkHistory = {
  scope: string;
  start: string;
  end: string;
  interval_seconds: number;
  summaries: WorkSummary[];
  truncated: boolean;
  ticket_credit_truncated?: boolean;
};

function gcd(a: bigint, b: bigint): bigint {
  while (b) [a, b] = [b, a % b];
  return a;
}
export function addAmounts(left?: Amount, right?: Amount): Amount | undefined {
  if (!left) return right;
  if (!right) return left;
  if (left.currency !== right.currency || left.unit !== right.unit)
    throw new Error("Incompatible work accounting units");
  let numerator =
    BigInt(left.numerator) * BigInt(right.denominator) +
    BigInt(right.numerator) * BigInt(left.denominator);
  let denominator = BigInt(left.denominator) * BigInt(right.denominator);
  const divisor = gcd(numerator, denominator);
  numerator /= divisor;
  denominator /= divisor;
  return { ...left, numerator: String(numerator), denominator: String(denominator) };
}

export function sumWork(values: WorkSummary[]): WorkSummary | undefined {
  if (!values.length) return;
  const total: WorkSummary = {
    resource_id: values[0].resource_id,
    ip: "",
    region: "",
    profile_key: "",
    accounted_attempts: 0,
    unaccounted_samples: 0,
    dispatched_attempts: 0,
    dispatched_source_ms: "0",
    returned_attempts: 0,
    returned_pixels: "0",
    accepted_segments: 0,
    accepted_source_ms: "0",
    accepted_pixels: "0",
    accepted_rendition_ms: "0",
    service_charge_samples: 0,
    ticket_credit_samples: 0,
    tickets_sent: "0",
    dispatched_duration_samples: 0,
    accepted_duration_samples: 0,
    accepted_output_samples: 0,
  };
  for (const value of values) {
    for (const field of [
      "accounted_attempts",
      "unaccounted_samples",
      "dispatched_attempts",
      "returned_attempts",
      "accepted_segments",
      "service_charge_samples",
      "ticket_credit_samples",
      "dispatched_duration_samples",
      "accepted_duration_samples",
      "accepted_output_samples",
    ] as const)
      total[field] += value[field];
    for (const field of [
      "dispatched_source_ms",
      "returned_pixels",
      "accepted_source_ms",
      "accepted_pixels",
      "accepted_rendition_ms",
      "tickets_sent",
    ] as const)
      total[field] = String(BigInt(total[field]) + BigInt(value[field]));
    total.service_charge = addAmounts(total.service_charge, value.service_charge);
    total.ticket_credit_sent = addAmounts(total.ticket_credit_sent, value.ticket_credit_sent);
    total.ticket_credit_incomplete ||= value.ticket_credit_incomplete;
  }
  if (total.ticket_credit_incomplete) total.ticket_credit_sent = undefined;
  return total;
}

export function workMinutes(milliseconds: string): string {
  const hundredths = (BigInt(milliseconds) + 300n) / 600n;
  return `${hundredths / 100n}.${String(hundredths % 100n).padStart(2, "0")}`;
}

export function isPartialWork(total: WorkSummary): boolean {
  return (
    !!total.ticket_credit_incomplete ||
    total.unaccounted_samples > 0 ||
    total.service_charge_samples < total.dispatched_attempts ||
    total.dispatched_duration_samples < total.dispatched_attempts ||
    total.accepted_duration_samples < total.accepted_segments ||
    total.accepted_output_samples < total.accepted_segments
  );
}

export function workAmount(amount?: Amount): string {
  if (!amount) return "Not observed";
  if (amount.currency !== "ETH" || amount.unit !== "wei")
    return `${amount.numerator}/${amount.denominator} ${amount.currency}/${amount.unit}`;
  const numerator = BigInt(amount.numerator),
    denominator = BigInt(amount.denominator);
  if (numerator === 0n) return "0 ETH";
  const wei = numerator / denominator;
  if (wei === 0n) return "<1 wei";
  const whole = wei / 1000000000000000000n;
  const fraction = String(wei % 1000000000000000000n)
    .padStart(18, "0")
    .replace(/0+$/, "");
  return `${numerator % denominator ? "≈" : ""}${whole}${fraction ? `.${fraction}` : ""} ETH`;
}

export function compareWorkAmounts(left?: Amount, right?: Amount): number {
  if (!left || !right) return left ? -1 : right ? 1 : 0;
  if (left.currency !== right.currency || left.unit !== right.unit)
    return `${left.currency}/${left.unit}`.localeCompare(`${right.currency}/${right.unit}`);
  const difference =
    BigInt(right.numerator) * BigInt(left.denominator) -
    BigInt(left.numerator) * BigInt(right.denominator);
  return difference < 0n ? -1 : difference > 0n ? 1 : 0;
}
