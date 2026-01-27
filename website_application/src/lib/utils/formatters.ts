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

export function formatCurrency(amount: number, currency: string = "USD"): string {
  if (amount === null || amount === undefined || isNaN(amount)) return "N/A";

  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: currency,
  }).format(amount);
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
