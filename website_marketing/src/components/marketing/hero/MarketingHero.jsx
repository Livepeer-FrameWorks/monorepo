import { Fragment, useMemo } from "react";
import { cn } from "@/lib/utils";
import { motion } from "framer-motion";
import { SectionContainer } from "@/components/ui/section";
import { renderSlot } from "../utils";
import MarketingCTAButton from "../buttons/MarketingCTAButton";

const createHeroSurfaceStyle = (seed = "") => {
  if (!seed) {
    return {};
  }

  let hash = 0;
  for (let i = 0; i < seed.length; i += 1) {
    hash = (hash << 5) - hash + seed.charCodeAt(i);
    hash |= 0;
  }

  const next = () => {
    hash = (hash * 9301 + 49297) % 233280;
    return hash / 233280;
  };

  const range = (min, max) => min + (max - min) * next();

  const gradientAngle = range(130, 165);

  const beforeTop = range(-360, -220);
  const beforeLeft = range(-360, -240);
  const beforeRotate = range(-18, -8);
  const beforeScale = range(0.95, 1.15);
  const beforeOpacity = range(0.35, 0.65);

  const afterTop = range(-160, -80);
  const afterRight = range(-320, -200);
  const afterRotate = range(-10, 2);
  const afterScale = range(0.92, 1.12);
  const afterOpacity = range(0.3, 0.55);

  return {
    "--hero-gradient-angle": `${gradientAngle.toFixed(1)}deg`,
    "--hero-before-top": `${beforeTop.toFixed(2)}px`,
    "--hero-before-left": `${beforeLeft.toFixed(2)}px`,
    "--hero-before-rotate": `${beforeRotate.toFixed(2)}deg`,
    "--hero-before-scale": beforeScale.toFixed(3),
    "--hero-before-opacity": beforeOpacity.toFixed(3),
    "--hero-after-top": `${afterTop.toFixed(2)}px`,
    "--hero-after-right": `${afterRight.toFixed(2)}px`,
    "--hero-after-rotate": `${afterRotate.toFixed(2)}deg`,
    "--hero-after-scale": afterScale.toFixed(3),
    "--hero-after-opacity": afterOpacity.toFixed(3),
  };
};

const MarketingHero = ({
  eyebrow,
  title,
  description,
  primaryAction,
  secondaryAction,
  media,
  align = "center",
  seed = "",
  layout = "stacked",
  mediaPosition = "right",
  mediaSurface = "glow",
  mediaClassName,
  surface = "gradient",
  surfaceTone = "accent",
  surfaceIntensity = "base",
  accents = [],
  support,
  footnote,
  children,
  className,
}) => {
  const alignmentClass = align === "left" ? "marketing-hero--left" : "marketing-hero--center";
  const secondaryActions = Array.isArray(secondaryAction)
    ? secondaryAction
    : secondaryAction
      ? [secondaryAction]
      : [];

  const renderAction = (action, intent, index = 0) => {
    if (!action) return null;

    if (typeof action.render === "function") {
      return (
        <Fragment key={action.key ?? action.label ?? `hero-action-${intent}-${index}`}>
          {action.render({ intent })}
        </Fragment>
      );
    }

    // eslint-disable-next-line no-unused-vars
    const { label, className: actionClassName, icon, key: actionKey, render, ...rest } = action;

    if (!label) return null;

    return (
      <MarketingCTAButton
        key={actionKey ?? label ?? `hero-action-${intent}-${index}`}
        intent={intent}
        label={label}
        icon={icon}
        className={cn("marketing-hero__button", actionClassName)}
        {...rest}
      />
    );
  };

  const heroVariables = useMemo(() => createHeroSurfaceStyle(seed), [seed]);
  const hasMedia = Boolean(media);
  const mediaWrapperClass = cn(
    "marketing-hero__media",
    mediaSurface && `marketing-hero__media--surface-${mediaSurface}`,
    mediaClassName
  );

  const accentElements = (accents ?? [])
    .map((accent, index) => {
      if (!accent) return null;

      const {
        kind = "beam",
        x = 50,
        y = 50,
        width = "40vw",
        height = "28vw",
        rotate = 0,
        fill,
        opacity = 0.45,
        blur = "0px",
        radius,
        layer,
      } = accent;

      const style = {
        "--accent-x": typeof x === "number" ? x : parseFloat(x),
        "--accent-y": typeof y === "number" ? y : parseFloat(y),
        "--accent-width": width,
        "--accent-height": height,
        "--accent-rotate": typeof rotate === "number" ? `${rotate}deg` : rotate,
        "--accent-fill": fill,
        "--accent-opacity": opacity,
        "--accent-blur": blur,
        "--accent-radius": radius,
        "--accent-layer": layer,
      };

      return (
        <div
          key={accent.key ?? `hero-accent-${index}`}
          className={cn("marketing-hero__accent", kind && `marketing-hero__accent--${kind}`)}
          style={style}
          aria-hidden="true"
        />
      );
    })
    .filter(Boolean);

  return (
    <section
      className={cn(
        "marketing-hero",
        alignmentClass,
        hasMedia && "marketing-hero--has-media",
        layout && `marketing-hero--layout-${layout}`,
        hasMedia && `marketing-hero--media-${mediaPosition}`,
        className
      )}
      style={heroVariables}
      data-surface={surface}
      data-surface-tone={surfaceTone}
      data-surface-intensity={surfaceIntensity}
    >
      <div className="marketing-hero__surface" aria-hidden="true" />
      {accentElements.length ? (
        <div className="marketing-hero__accents" aria-hidden="true">
          {accentElements}
        </div>
      ) : null}
      <SectionContainer className="marketing-hero__container px-0 sm:px-0">
        <motion.div
          className="marketing-hero__layout"
          initial={{ opacity: 0, y: 26 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.55 }}
        >
          <div className="marketing-hero__body">
            <div className="marketing-hero__content">
              {eyebrow ? (
                <span className="marketing-hero__eyebrow marketing-pill">{eyebrow}</span>
              ) : null}
              {title ? <h1 className="marketing-hero__title">{title}</h1> : null}
              {description ? <p className="marketing-hero__description">{description}</p> : null}
              {support ? (
                <div className="marketing-hero__support">{renderSlot(support)}</div>
              ) : null}
              {primaryAction || secondaryActions.length ? (
                <div className="marketing-hero__actions">
                  {renderAction(primaryAction, "primary")}
                  {secondaryActions.map((action, idx) => renderAction(action, "secondary", idx))}
                </div>
              ) : null}
              {footnote ? (
                <div className="marketing-hero__footnote">{renderSlot(footnote)}</div>
              ) : null}
              {children ? <div className="marketing-hero__extra">{children}</div> : null}
            </div>
            {hasMedia ? <div className={mediaWrapperClass}>{renderSlot(media)}</div> : null}
          </div>
        </motion.div>
      </SectionContainer>
    </section>
  );
};

export default MarketingHero;
