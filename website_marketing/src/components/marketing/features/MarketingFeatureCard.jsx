import { forwardRef } from "react";
import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";
import MarketingIconBadge from "../misc/MarketingIconBadge";

const MarketingFeatureCard = forwardRef(
  (
    {
      icon: Icon,
      iconTone = "accent",
      badge,
      title,
      subtitle,
      description,
      meta,
      tone = "accent",
      actions,
      children,
      hover = "lift",
      flush = false,
      stripe = false,
      metaAlign = "start",
      className,
      ...props
    },
    ref
  ) => (
    <div
      ref={ref}
      className={cn(
        "marketing-feature-card",
        tone && `marketing-feature-card--tone-${tone}`,
        flush && "marketing-feature-card--flush",
        stripe && "marketing-feature-card--stripe",
        metaAlign && `marketing-feature-card--meta-${metaAlign}`,
        className
      )}
      data-hover={hover}
      {...props}
    >
      <div className="marketing-feature-card__header">
        {Icon ? (
          <MarketingIconBadge
            tone={iconTone}
            variant="neutral"
            className="marketing-feature-card__icon"
          >
            <Icon className="marketing-feature-card__icon-symbol" />
          </MarketingIconBadge>
        ) : null}
        <div className="marketing-feature-card__heading">
          {badge ? <span className="marketing-feature-card__badge">{badge}</span> : null}
          {title ? <h4 className="marketing-feature-card__title">{title}</h4> : null}
          {subtitle ? <span className="marketing-feature-card__subtitle">{subtitle}</span> : null}
        </div>
        {meta ? <div className="marketing-feature-card__meta">{renderSlot(meta)}</div> : null}
      </div>
      {description ? <p className="marketing-feature-card__description">{description}</p> : null}
      {children}
      {actions ? (
        <div className="marketing-feature-card__actions">{renderSlot(actions)}</div>
      ) : null}
    </div>
  )
);

MarketingFeatureCard.displayName = "MarketingFeatureCard";

export default MarketingFeatureCard;
