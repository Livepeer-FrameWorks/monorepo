export interface LiveCatchupConfig {
  enabled: boolean;
  thresholdMs: number;
  requestMs: number;
  cooldownMs: number;
}

export type LiveCatchupOption =
  | boolean
  | number
  | {
      thresholdMs?: number;
      requestMs?: number;
      cooldownMs?: number;
    }
  | null
  | undefined;

export const LIVE_CATCHUP_DEFAULTS: LiveCatchupConfig = {
  enabled: true,
  thresholdMs: 5000,
  requestMs: 5000,
  cooldownMs: 2000,
};

export type LiveCatchupDefault =
  | { undefinedMeans: "off" }
  | { undefinedMeans: { thresholdMs: number; requestMs?: number; cooldownMs?: number } };

export function normalizeLiveCatchupConfig(
  option: LiveCatchupOption,
  perCallerDefault: LiveCatchupDefault
): LiveCatchupConfig {
  if (option === undefined) {
    if (perCallerDefault.undefinedMeans === "off") {
      return { ...LIVE_CATCHUP_DEFAULTS, enabled: false };
    }
    return {
      ...LIVE_CATCHUP_DEFAULTS,
      thresholdMs: perCallerDefault.undefinedMeans.thresholdMs,
      requestMs: perCallerDefault.undefinedMeans.requestMs ?? LIVE_CATCHUP_DEFAULTS.requestMs,
      cooldownMs: perCallerDefault.undefinedMeans.cooldownMs ?? LIVE_CATCHUP_DEFAULTS.cooldownMs,
    };
  }

  if (option === false || option === 0 || option === null) {
    return { ...LIVE_CATCHUP_DEFAULTS, enabled: false };
  }

  if (option === true) {
    return { ...LIVE_CATCHUP_DEFAULTS, thresholdMs: 60000 };
  }

  if (typeof option === "number") {
    return { ...LIVE_CATCHUP_DEFAULTS, thresholdMs: option * 1000 };
  }

  return {
    ...LIVE_CATCHUP_DEFAULTS,
    thresholdMs: option.thresholdMs ?? LIVE_CATCHUP_DEFAULTS.thresholdMs,
    requestMs: option.requestMs ?? LIVE_CATCHUP_DEFAULTS.requestMs,
    cooldownMs: option.cooldownMs ?? LIVE_CATCHUP_DEFAULTS.cooldownMs,
  };
}
