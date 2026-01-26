export type TimeRangeOption = {
  value: string;
  label: string;
  days: number;
  hours?: number;
};

export const DEFAULT_TIME_RANGE = "7d";

export const TIME_RANGE_OPTIONS: TimeRangeOption[] = [
  { value: "24h", label: "Last 24 Hours", days: 1, hours: 24 },
  { value: "7d", label: "Last 7 Days", days: 7 },
  { value: "30d", label: "Last 30 Days", days: 30 },
  { value: "90d", label: "Last 90 Days", days: 90 },
];

export function resolveTimeRange(value: string, now: Date = new Date()) {
  const option =
    TIME_RANGE_OPTIONS.find((item) => item.value === value) ||
    TIME_RANGE_OPTIONS.find((item) => item.value === DEFAULT_TIME_RANGE);

  if (!option) {
    const endFallback = now.toISOString();
    const startFallback = new Date(
      now.getTime() - 7 * 24 * 60 * 60 * 1000,
    ).toISOString();
    return {
      start: startFallback,
      end: endFallback,
      days: 7,
      hours: 168,
      label: "Last 7 Days",
      value: DEFAULT_TIME_RANGE,
    };
  }

  const end = now.toISOString();
  const startDate = new Date(now.getTime());

  if (option.hours && option.value === "24h") {
    startDate.setHours(startDate.getHours() - option.hours);
  } else {
    startDate.setDate(startDate.getDate() - option.days);
  }

  return {
    start: startDate.toISOString(),
    end,
    days: option.days,
    hours: option.hours ?? option.days * 24,
    label: option.label,
    value: option.value,
  };
}

export function timeRangeLabel(value: string) {
  return (
    TIME_RANGE_OPTIONS.find((item) => item.value === value)?.label ??
    "Custom Range"
  );
}
