export function formatBytes(bytes: number, decimals: number = 2): string {
  if (!bytes || bytes === 0) return "0 Bytes";

  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ["Bytes", "KB", "MB", "GB", "TB", "PB"];

  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + " " + sizes[i];
}

export function formatDuration(seconds: number): string {
  if (!seconds || seconds === 0) return "0s";

  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = seconds % 60;

  if (hours > 0) {
    return `${hours}h ${minutes}m ${remainingSeconds}s`;
  }

  if (minutes > 0) {
    return `${minutes}m ${remainingSeconds}s`;
  }

  return `${remainingSeconds}s`;
}

export function decodeRelayId(
  value: string | null | undefined,
  expectedType?: string
): string | null {
  if (!value) return null;

  const atobFn =
    typeof globalThis !== "undefined"
      ? (globalThis as { atob?: (input: string) => string }).atob
      : undefined;

  if (!atobFn) return value;

  let decoded = "";
  try {
    decoded = atobFn(value);
  } catch {
    return value;
  }

  const parts = decoded.split(":", 2);
  if (parts.length !== 2 || !parts[1]) return value;
  if (expectedType && parts[0] !== expectedType) return value;
  return parts[1];
}

export function formatDate(date: string | Date): string {
  if (!date) return "N/A";

  const dateObj = typeof date === "string" ? new Date(date) : date;

  if (isNaN(dateObj.getTime())) return "Invalid Date";

  const now = new Date();
  const diffMs = now.getTime() - dateObj.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  // Less than a minute ago
  if (diffMins < 1) {
    return "Just now";
  }

  // Less than an hour ago
  if (diffMins < 60) {
    return `${diffMins}m ago`;
  }

  // Less than 24 hours ago
  if (diffHours < 24) {
    return `${diffHours}h ago`;
  }

  // Less than 7 days ago
  if (diffDays < 7) {
    return `${diffDays}d ago`;
  }

  // More than a week ago - show actual date
  return dateObj.toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * Format an expiry/retention date for display.
 * Unlike formatDate which is for past events, this handles future dates.
 */
export function formatExpiry(date: string | Date | null | undefined): string {
  if (!date) return "Never";

  const dateObj = typeof date === "string" ? new Date(date) : date;
  if (isNaN(dateObj.getTime())) return "Invalid Date";

  const now = new Date();
  const diffMs = dateObj.getTime() - now.getTime(); // Future is positive

  // Past date - expired
  if (diffMs < 0) return "Expired";

  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  // Less than an hour from now
  if (diffMins < 60) return `in ${Math.max(1, diffMins)}m`;

  // Less than 24 hours from now
  if (diffHours < 24) return `in ${diffHours}h`;

  // Less than 7 days from now
  if (diffDays < 7) return `in ${diffDays}d`;

  // More than a week - show actual date
  return dateObj.toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

/**
 * Check if a date has expired (is in the past).
 * Returns false for null/undefined dates (interpreted as "never expires").
 */
export function isExpired(date: string | Date | null | undefined): boolean {
  if (!date) return false;

  const dateObj = typeof date === "string" ? new Date(date) : date;
  if (isNaN(dateObj.getTime())) return false;

  return dateObj.getTime() < Date.now();
}

export function formatTimestamp(timestamp: string | Date): string {
  if (!timestamp) return "N/A";

  const dateObj = typeof timestamp === "string" ? new Date(timestamp) : timestamp;

  if (isNaN(dateObj.getTime())) return "Invalid Date";

  return dateObj.toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function formatNumber(num: number): string {
  if (num === null || num === undefined || isNaN(num)) return "N/A";

  return new Intl.NumberFormat("en-US").format(num);
}

export function formatPercentage(value: number, total: number, decimals: number = 1): string {
  if (!value || !total || total === 0) return "0%";

  const percentage = (value / total) * 100;
  return percentage.toFixed(decimals) + "%";
}

export function formatBitrate(kbps: number): string {
  if (!kbps || kbps === 0) return "0 kbps";

  if (kbps >= 1000) {
    return (kbps / 1000).toFixed(1) + " Mbps";
  }

  return kbps + " kbps";
}

export function formatResolution(resolution: string): string {
  if (!resolution) return "N/A";

  // Common resolution mappings
  const resolutionMap: Record<string, string> = {
    "1920x1080": "1080p",
    "1280x720": "720p",
    "854x480": "480p",
    "640x360": "360p",
    "426x240": "240p",
    "3840x2160": "4K",
    "2560x1440": "1440p",
  };

  return resolutionMap[resolution] || resolution;
}

/**
 * Formats a money amount in major units. The currency is always explicit: the
 * prepaid ledger and invoice amounts are EUR, while charges are in the tenant's
 * presentment currency.
 */
export function formatCurrency(amount: number, currency: string): string {
  if (amount === null || amount === undefined || isNaN(amount)) return "N/A";

  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: currency,
  }).format(amount);
}

/** Formats a money amount given in minor units (cents). */
export function formatCents(cents: number, currency: string): string {
  if (cents === null || cents === undefined || isNaN(cents)) return "N/A";
  return formatCurrency(cents / 100, currency);
}

/** Decimal string of an ECB rate without trailing zeros, e.g. "1.1698". */
export function formatUnitsPerEur(unitsPerEur: string): string {
  const trimmed = unitsPerEur.trim();
  return trimmed.includes(".") ? trimmed.replace(/0+$/, "").replace(/\.$/, "") : trimmed;
}

/**
 * States the ECB rate of a conversion, e.g. "1.1698 USD per EUR, ECB reference
 * rate of 2026-09-16". Returns null for EUR, which converts at the identity.
 */
export function formatEcbRate(
  currency: string | null | undefined,
  unitsPerEur: string | null | undefined,
  referenceDate: string | null | undefined
): string | null {
  if (!currency || currency.toUpperCase() === "EUR" || !unitsPerEur || !referenceDate) {
    return null;
  }
  return `${formatUnitsPerEur(unitsPerEur)} ${currency.toUpperCase()} per EUR, ECB reference rate of ${referenceDate}`;
}

export interface CurrencyConversionFields {
  originalAmountCents: number;
  originalCurrency: string;
  eurAmountCents: number;
  unitsPerEur: string;
  referenceDate: string;
}

/**
 * Describes the EUR amount of a non-EUR payment, e.g. "€21.37 at 1.1698 USD
 * per EUR, ECB reference rate of 2026-09-16". Returns null for EUR payments
 * and payments without a conversion.
 */
export function formatEurConversion(
  conversion: CurrencyConversionFields | null | undefined
): string | null {
  if (!conversion) return null;
  const rate = formatEcbRate(
    conversion.originalCurrency,
    conversion.unitsPerEur,
    conversion.referenceDate
  );
  if (!rate) return null;
  return `${formatCents(conversion.eurAmountCents, "EUR")} at ${rate}`;
}

export interface InvoicePresentmentFields {
  amount: number | string;
  currency: string;
  presentmentAmountCents?: number | null;
  presentmentCurrency?: string | null;
  presentmentUnitsPerEur?: string | null;
  presentmentReferenceDate?: string | null;
}

/**
 * The amount an invoice charges and, when it is presented in another currency
 * than EUR, the EUR total with the rate and reference date. Drafts carry no
 * presentment fields and show their EUR amount.
 */
export function invoiceChargeDisplay(invoice: InvoicePresentmentFields): {
  charged: string;
  eurNote: string | null;
} {
  const eurAmount = typeof invoice.amount === "number" ? invoice.amount : Number(invoice.amount);
  const eurTotal = formatCurrency(eurAmount, invoice.currency);
  if (invoice.presentmentAmountCents == null || !invoice.presentmentCurrency) {
    return { charged: eurTotal, eurNote: null };
  }
  const charged = formatCents(invoice.presentmentAmountCents, invoice.presentmentCurrency);
  if (invoice.presentmentCurrency.toUpperCase() === "EUR") {
    return { charged, eurNote: null };
  }
  const rate = formatEcbRate(
    invoice.presentmentCurrency,
    invoice.presentmentUnitsPerEur,
    invoice.presentmentReferenceDate
  );
  return { charged, eurNote: rate ? `${eurTotal} at ${rate}` : eurTotal };
}

const TOKEN_DECIMALS: Record<string, number> = {
  ETH: 18,
  USDC: 6,
  LPT: 18,
};

const TOKEN_DISPLAY_PRECISION: Record<string, number> = {
  ETH: 6,
  USDC: 2,
  LPT: 4,
};

/**
 * Format a token amount given as a base-units decimal string (e.g. wei) into
 * a human-readable whole-token decimal trimmed to the asset's display precision.
 * Returns the input unchanged for unknown assets so callers don't crash on new symbols.
 */
export function formatTokenAmount(baseUnits: string | null | undefined, asset: string): string {
  if (!baseUnits) return "N/A";
  const decimals = TOKEN_DECIMALS[asset];
  if (decimals === undefined) return baseUnits;

  const negative = baseUnits.startsWith("-");
  const digits = (negative ? baseUnits.slice(1) : baseUnits).replace(/^0+/, "") || "0";

  let whole: string;
  let frac: string;
  if (digits.length <= decimals) {
    whole = "0";
    frac = digits.padStart(decimals, "0");
  } else {
    whole = digits.slice(0, digits.length - decimals);
    frac = digits.slice(digits.length - decimals);
  }

  const precision = TOKEN_DISPLAY_PRECISION[asset] ?? Math.min(6, decimals);
  frac = frac.slice(0, precision).replace(/0+$/, "");

  const out = frac ? `${whole}.${frac}` : whole;
  return negative ? `-${out}` : out;
}

export function formatRelativeTime(dateStr: string): string {
  const diffMs = Date.now() - new Date(dateStr).getTime();
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return "just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  return `${Math.floor(diffHr / 24)}d ago`;
}

export function formatUptime(uptimeMs: number): string {
  if (!uptimeMs || uptimeMs === 0) return "0s";

  const seconds = Math.floor(uptimeMs / 1000);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = seconds % 60;

  const parts = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (remainingSeconds > 0) parts.push(`${remainingSeconds}s`);

  return parts.join(" ") || "0s";
}
