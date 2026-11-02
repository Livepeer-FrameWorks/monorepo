/**
 * StudioConnectionHealth Component
 *
 * A pill that grades the publish connection while live, with the send rate,
 * packet loss and round-trip time as its title. Reads stats from the
 * StreamCrafter context unless they are given.
 */

import React from "react";
import { assessConnectionHealth, type IngestStats } from "@livepeer-frameworks/streamcrafter-core";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContextOptional } from "../context/StreamCrafterContext";

const LABEL_KEYS = {
  good: "connectionGood",
  fair: "connectionFair",
  poor: "connectionPoor",
} as const;

export interface StudioConnectionHealthProps {
  stats?: IngestStats | null;
  isStreaming?: boolean;
}

export function StudioConnectionHealth({
  stats: statsProp,
  isStreaming: streamingProp,
}: StudioConnectionHealthProps) {
  const t = useStudioTranslate();
  const ctx = useStreamCrafterContextOptional();
  const isStreaming = streamingProp ?? ctx?.isStreaming ?? false;
  const health = assessConnectionHealth(statsProp ?? ctx?.stats ?? null);
  if (!isStreaming || !health) return null;

  return (
    <span
      className={`fw-sc-health fw-sc-health--${health.level}`}
      role="status"
      title={t("connectionDetails", {
        bitrate: health.bitrateKbps,
        loss: health.packetLossPct.toFixed(1),
        rtt: Math.round(health.rttMs),
      })}
    >
      {t(LABEL_KEYS[health.level])}
    </span>
  );
}
