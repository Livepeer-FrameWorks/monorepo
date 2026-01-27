import { forwardRef } from "react";
import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";

const ComparisonTable = forwardRef(
  (
    {
      columns = [],
      rows = [],
      caption,
      condensed = false,
      tone = "accent",
      className,
      style,
      ...props
    },
    ref
  ) => (
    <div
      ref={ref}
      className={cn("comparison-table__wrapper", className)}
      data-tone={tone}
      style={{ "--comparison-cols": columns.length, ...style }}
      {...props}
    >
      {caption ? <div className="comparison-table__caption">{caption}</div> : null}
      <div className={cn("comparison-table", condensed && "comparison-table--condensed")}>
        <div className="comparison-table__head">
          <div className="comparison-table__cell comparison-table__cell--label" />
          {columns.map((column) => (
            <div key={column.key ?? column.label} className="comparison-table__cell">
              {column.label}
            </div>
          ))}
        </div>
        {rows.map((row) => (
          <div key={row.key ?? row.label} className="comparison-table__row">
            <div className="comparison-table__cell comparison-table__cell--label">{row.label}</div>
            {columns.map((column) => {
              const cellKey = column.key ?? column.label;
              const value = row[cellKey] ?? row.cells?.[cellKey];
              return (
                <div key={`${row.key ?? row.label}-${cellKey}`} className="comparison-table__cell">
                  {renderSlot(value ?? "—")}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {columns.length ? (
        <div className="comparison-table__mobile">
          {columns.map((column, columnIndex) => (
            <div
              key={column.key ?? column.label ?? columnIndex}
              className="comparison-table__mobile-card"
            >
              <div className="comparison-table__mobile-heading">{column.label}</div>
              <dl className="comparison-table__mobile-list">
                {rows.map((row) => {
                  const cellKey = column.key ?? column.label;
                  const value = row[cellKey] ?? row.cells?.[cellKey];
                  return (
                    <div
                      key={`${column.key ?? column.label ?? columnIndex}-${row.key ?? row.label}`}
                      className="comparison-table__mobile-item"
                    >
                      <dt className="comparison-table__mobile-term">{row.label}</dt>
                      <dd className="comparison-table__mobile-value">{renderSlot(value ?? "—")}</dd>
                    </div>
                  );
                })}
              </dl>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
);

ComparisonTable.displayName = "ComparisonTable";

export default ComparisonTable;
