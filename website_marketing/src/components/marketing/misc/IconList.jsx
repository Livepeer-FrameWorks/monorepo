import { forwardRef } from "react";
import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";

const IconList = forwardRef(
  (
    {
      items = [],
      tone = "accent",
      columns = 1,
      stackAt = "md",
      variant = "card",
      indicator = "icon",
      className,
      ...props
    },
    ref
  ) => (
    <div
      ref={ref}
      className={cn(
        "marketing-icon-list",
        columns && `marketing-icon-list--cols-${columns}`,
        stackAt && `marketing-icon-list--stack-${stackAt}`,
        variant && `marketing-icon-list--${variant}`,
        indicator && `marketing-icon-list--indicator-${indicator}`,
        className
      )}
      data-tone={tone}
      {...props}
    >
      {items.map((item, index) => (
        <div
          key={item.title ?? item.id ?? index}
          className="marketing-icon-list__item"
          data-tone={item.tone}
        >
          {indicator !== "none" || item.title ? (
            <div className="marketing-icon-list__header">
              {indicator === "dot" ? (
                <span className="marketing-icon-list__dot" data-tone={item.tone || tone} />
              ) : indicator === "icon" && item.icon ? (
                <span className="marketing-icon-list__icon">{renderSlot(item.icon)}</span>
              ) : null}
              {item.title ? <h4 className="marketing-icon-list__title">{item.title}</h4> : null}
            </div>
          ) : null}
          {item.description ? (
            <p className="marketing-icon-list__description">{item.description}</p>
          ) : null}
        </div>
      ))}
    </div>
  )
);

IconList.displayName = "IconList";

export default IconList;
