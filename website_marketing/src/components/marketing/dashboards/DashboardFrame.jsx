import { cn } from "@/lib/utils";

// App-window chrome so a visualization reads as a real product panel: a title
// bar with traffic-light dots + tab label, over a dark gradient body. Styling
// mirrors .marketing-code-panel (see styles/marketing/dashboards.css).
export function DashboardFrame({
  title,
  badge,
  tone = "accent",
  actions,
  className,
  bodyClassName,
  children,
}) {
  return (
    <div className={cn("dashboard-frame", className)}>
      <div className="dashboard-frame__bar">
        <span className="dashboard-frame__dots" aria-hidden="true">
          <i />
          <i />
          <i />
        </span>
        {title ? <span className="dashboard-frame__title">{title}</span> : null}
        {badge ? (
          <span className="dashboard-frame__badge" data-tone={tone}>
            {badge}
          </span>
        ) : null}
        {actions ? <span className="dashboard-frame__actions">{actions}</span> : null}
      </div>
      <div className={cn("dashboard-frame__body", bodyClassName)}>{children}</div>
    </div>
  );
}

export default DashboardFrame;
