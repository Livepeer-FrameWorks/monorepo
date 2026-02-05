import { forwardRef } from "react";
import { cn } from "@/lib/utils";

const BAND_PRESETS = {
  foundation: {
    surface: "panel",
    tone: "neutral",
    texture: "none",
    density: "compact",
    flush: true,
  },
  beam: {
    surface: "panel",
    tone: "cool",
    texture: "beams",
    density: "spacious",
    flush: true,
  },
  quiet: {
    surface: "panel",
    tone: "neutral",
    texture: "grain",
    density: "compact",
    flush: true,
  },
  signal: {
    surface: "midnight",
    tone: "violet",
    texture: "beams",
    density: "compact",
    flush: true,
  },
};

const MarketingBand = forwardRef(
  (
    {
      as: Component = "section",
      preset,
      surface,
      tone,
      texture,
      density,
      bleed = false,
      flush,
      contentClassName,
      className,
      children,
      ...props
    },
    ref
  ) => {
    const presetConfig = (preset && BAND_PRESETS[preset]) || null;
    const resolvedSurface = surface ?? presetConfig?.surface ?? "none";
    const resolvedTone = tone ?? presetConfig?.tone ?? "default";
    const resolvedTexture = texture ?? presetConfig?.texture ?? "default";
    const resolvedDensity = density ?? presetConfig?.density ?? "default";
    const resolvedFlush = flush ?? presetConfig?.flush ?? false;

    return (
      <Component
        ref={ref}
        className={cn("marketing-band", bleed && "marketing-band--bleed", className)}
        data-surface={resolvedSurface}
        data-tone={resolvedTone !== "default" ? resolvedTone : undefined}
        data-texture={resolvedTexture !== "default" ? resolvedTexture : undefined}
        data-density={resolvedDensity !== "default" ? resolvedDensity : undefined}
        {...props}
      >
        <div
          className={cn(
            "marketing-band__inner",
            resolvedFlush && "marketing-band__inner--flush",
            contentClassName
          )}
        >
          {children}
        </div>
      </Component>
    );
  }
);

MarketingBand.displayName = "MarketingBand";

export default MarketingBand;
