import { cn } from "@/lib/utils";
import MarketingCTAButton from "../buttons/MarketingCTAButton";
import MarketingSlab from "../content/MarketingSlab";

const MarketingFinalCTA = ({
  eyebrow,
  title,
  description,
  primaryAction,
  secondaryAction,
  alignment = "center",
  variant = "slab",
  className,
}) => {
  const isBand = variant === "band";
  const alignmentClass = alignment === "left" ? "marketing-cta--left" : "marketing-cta--center";
  const secondaryActions = Array.isArray(secondaryAction)
    ? secondaryAction
    : secondaryAction
      ? [secondaryAction]
      : [];

  const renderAction = (action, intent, index = 0) => {
    if (!action?.label) return null;

    const { label, className: actionClassName, icon, key: actionKey, ...rest } = action;

    return (
      <MarketingCTAButton
        key={actionKey ?? label ?? `cta-action-${intent}-${index}`}
        intent={intent}
        label={label}
        icon={icon}
        className={cn(actionClassName, isBand && "marketing-cta__button--slab")}
        {...rest}
      />
    );
  };

  const actionItems = [
    primaryAction ? { action: primaryAction, intent: "primary", index: 0 } : null,
    ...secondaryActions.map((action, index) => ({
      action,
      intent: "secondary",
      index: index + 1,
    })),
  ].filter(Boolean);

  const content = (
    <div className={cn("marketing-cta", alignmentClass, isBand ? "marketing-cta--band" : null)}>
      <div className="marketing-cta__body">
        {eyebrow ? <span className="marketing-pill marketing-cta__pill">{eyebrow}</span> : null}
        {title ? <h2 className="marketing-cta__title">{title}</h2> : null}
        {description ? <p className="marketing-cta__description">{description}</p> : null}
      </div>
      {actionItems.length ? (
        <div className={cn("marketing-cta__actions", isBand && "marketing-cta__actions--slab")}>
          {actionItems.map(({ action, intent, index }) => (
            <div
              key={action.key ?? action.label ?? `cta-action-slot-${intent}-${index}`}
              className={cn(
                "marketing-cta__action-cell",
                isBand && "marketing-cta__action-cell--slab"
              )}
            >
              {renderAction(action, intent, index)}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );

  if (isBand) {
    return <div className={cn("marketing-cta-band", className)}>{content}</div>;
  }

  return (
    <MarketingSlab variant="cta-panel" className={className}>
      {content}
    </MarketingSlab>
  );
};

export default MarketingFinalCTA;
