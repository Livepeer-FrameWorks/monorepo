import { forwardRef } from "react";
import { cn } from "@/lib/utils";

const TEXTURE_PRESETS = {
  default: {
    pattern: "none",
    noise: "none",
    beam: "none",
    motion: "none",
  },
  none: {
    pattern: "none",
    noise: "none",
    beam: "none",
    motion: "none",
  },
  grid: {
    pattern: "seams",
    noise: "none",
    beam: "none",
    motion: "none",
  },
  grain: {
    pattern: "pinlines",
    noise: "grain",
    beam: "none",
    motion: "none",
  },
  beams: {
    pattern: "seams",
    noise: "film",
    beam: "soft",
    motion: "drift",
  },
  broadcast: {
    pattern: "scanlines",
    noise: "film",
    beam: "soft",
    motion: "sweep",
  },
};

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
    tone: "steel",
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
      texturePattern,
      textureNoise,
      textureBeam,
      textureMotion,
      textureStrength,
      bleed = false,
      flush,
      layoutClassName,
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
    const texturePreset = TEXTURE_PRESETS[resolvedTexture] ?? TEXTURE_PRESETS.default;
    const resolvedTexturePattern = texturePattern ?? texturePreset.pattern;
    const resolvedTextureNoise = textureNoise ?? texturePreset.noise;
    const resolvedTextureBeam = textureBeam ?? texturePreset.beam;
    const resolvedTextureMotion = textureMotion ?? texturePreset.motion;
    const resolvedTextureStrength = textureStrength ?? "base";

    return (
      <Component
        ref={ref}
        className={cn("marketing-band", bleed && "marketing-band--bleed", className)}
        data-surface={resolvedSurface}
        data-tone={resolvedTone !== "default" ? resolvedTone : undefined}
        data-texture={resolvedTexture !== "default" ? resolvedTexture : undefined}
        data-density={resolvedDensity !== "default" ? resolvedDensity : undefined}
        data-texture-pattern={
          resolvedTexturePattern !== "none" ? resolvedTexturePattern : undefined
        }
        data-texture-noise={resolvedTextureNoise !== "none" ? resolvedTextureNoise : undefined}
        data-texture-beam={resolvedTextureBeam !== "none" ? resolvedTextureBeam : undefined}
        data-texture-motion={resolvedTextureMotion !== "none" ? resolvedTextureMotion : undefined}
        data-texture-strength={
          resolvedTextureStrength !== "base" ? resolvedTextureStrength : undefined
        }
        {...props}
      >
        <div
          className={cn(
            "marketing-band__inner",
            "marketing-band__layout",
            resolvedFlush && "marketing-band__inner--flush",
            layoutClassName
          )}
        >
          <div className={cn("marketing-band__content", contentClassName)}>{children}</div>
        </div>
      </Component>
    );
  }
);

MarketingBand.displayName = "MarketingBand";

export default MarketingBand;
