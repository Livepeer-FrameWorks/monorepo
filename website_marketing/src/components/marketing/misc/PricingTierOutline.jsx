import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";
import MarketingOutline from "../misc/MarketingOutline";

const PricingTierOutline = ({
  tone = "accent",
  badge,
  name,
  price,
  period,
  description,
  body = [],
  sections = [],
  actions,
  className,
  ...props
}) => {
  const accentMap = {
    primary: "accent",
    accent: "accent",
    amber: "yellow",
    yellow: "yellow",
    green: "green",
    purple: "purple",
  };

  const outlineAccent = accentMap[tone] ?? "accent";

  const normalizedSections = (sections ?? [])
    .map((section, index) => {
      const items = (section?.items ?? [])
        .map((item) => item)
        .filter((item) => item !== null && item !== undefined);
      return {
        key: section?.key ?? section?.title ?? `section-${index}`,
        title: section?.title,
        bullet: section?.bullet ?? "dot",
        items,
      };
    })
    .filter((section) => section.items.length > 0 || section.title);

  return (
    <MarketingOutline
      accent={outlineAccent}
      label={badge}
      labelPosition={badge ? "center" : "corner"}
      className={cn(
        "pricing-tier",
        tone && tone !== "accent" && `pricing-tier--${tone}`,
        className
      )}
      {...props}
    >
      <div className="pricing-tier__shell">
        <div className="pricing-tier__summary">
          {(name || price || period) && (
            <div className="pricing-tier__heading">
              {name ? <span className="pricing-tier__name">{name}</span> : null}
              {price || period ? (
                <div className="pricing-tier__price">
                  {price ? <span className="pricing-tier__amount">{price}</span> : null}
                  {period ? <span className="pricing-tier__period">{period}</span> : null}
                </div>
              ) : null}
            </div>
          )}
          {description ? (
            <p className="pricing-tier__description">{renderSlot(description)}</p>
          ) : null}
          {body && body.length ? (
            <div className="pricing-tier__body">
              {body.map((paragraph, index) => (
                <p key={`pricing-tier-body-${index}`} className="pricing-tier__paragraph">
                  {renderSlot(paragraph)}
                </p>
              ))}
            </div>
          ) : null}
          {actions ? <div className="pricing-tier__actions">{renderSlot(actions)}</div> : null}
        </div>
        {normalizedSections.length ? (
          <div className="pricing-tier__details">
            {normalizedSections.map((section) => (
              <div key={section.key} className="pricing-tier__section">
                {section.title ? (
                  <span className="pricing-tier__section-title">{section.title}</span>
                ) : null}
                {section.items.length ? (
                  <ul className="pricing-tier__list">
                    {section.items.map((item, idx) => (
                      <li key={`${section.key}-item-${idx}`} className="pricing-tier__list-item">
                        <span
                          className={cn(
                            "pricing-tier__bullet",
                            section.bullet === "dash" && "pricing-tier__bullet--dash"
                          )}
                          aria-hidden="true"
                        />
                        <span>{renderSlot(item)}</span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </MarketingOutline>
  );
};

export default PricingTierOutline;
