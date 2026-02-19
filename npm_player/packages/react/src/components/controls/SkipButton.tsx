import React from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import { SkipBackIcon, SkipForwardIcon } from "../Icons";

export interface SkipButtonProps {
  direction: "back" | "forward";
  seconds?: number;
  onSkip?: () => void;
  size?: number;
  className?: string;
}

export const SkipButton: React.FC<SkipButtonProps> = ({
  direction,
  seconds = 10,
  onSkip,
  size = 16,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();
  const Icon = direction === "back" ? SkipBackIcon : SkipForwardIcon;
  const delta = direction === "back" ? -seconds : seconds;
  const label = direction === "back" ? t("skipBackward") : t("skipForward");
  const title = direction === "back" ? `${t("seekBackward")} (j)` : `${t("seekForward")} (l)`;

  const handleClick = onSkip ?? (() => ctx?.seekBy(delta));

  return (
    <button
      type="button"
      className={className ?? "fw-btn-flush"}
      aria-label={label}
      title={title}
      onClick={handleClick}
    >
      <Icon size={size} />
    </button>
  );
};
