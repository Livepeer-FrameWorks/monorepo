import React from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import type {
  IngestState,
  ReconnectionState,
  StudioTranslateFn,
} from "@livepeer-frameworks/streamcrafter-core";

export interface StudioStatusBadgeProps {
  /** Override state (falls back to context) */
  state?: IngestState;
  /** Override reconnection state (falls back to context) */
  reconnectionState?: ReconnectionState | null;
  /** Override reconnecting flag (falls back to context) */
  isReconnecting?: boolean;
}

function getStatusText(
  state: IngestState,
  t: StudioTranslateFn,
  reconnectionState?: ReconnectionState | null
): string {
  if (reconnectionState?.isReconnecting) {
    return t("reconnectingAttempt", { attempt: reconnectionState.attemptNumber, max: 5 });
  }
  switch (state) {
    case "idle":
      return t("idle");
    case "requesting_permissions":
      return t("requestingPermissions");
    case "capturing":
      return t("ready");
    case "connecting":
      return t("connecting");
    case "streaming":
      return t("live");
    case "reconnecting":
      return t("reconnecting");
    case "error":
      return t("error");
    case "destroyed":
      return t("destroyed");
    default:
      return state;
  }
}

function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
  if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
  if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
  if (state === "error") return "fw-sc-badge fw-sc-badge--error";
  if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
  return "fw-sc-badge fw-sc-badge--idle";
}

export const StudioStatusBadge: React.FC<StudioStatusBadgeProps> = ({
  state: propState,
  reconnectionState: propReconnectionState,
  isReconnecting: propIsReconnecting,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const state = propState ?? ctx.state;
  const reconnectionState = propReconnectionState ?? ctx.reconnectionState;
  const isReconnecting = propIsReconnecting ?? ctx.isReconnecting;

  const text = getStatusText(state, t, reconnectionState);
  const badgeClass = getStatusBadgeClass(state, isReconnecting);

  return <span className={badgeClass}>{text}</span>;
};
