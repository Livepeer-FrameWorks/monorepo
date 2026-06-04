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
      {/* Real <table> markup so the canonical data is machine-extractable (search +
          AI answer engines). The grid appearance is preserved via CSS; the mobile
          card view below adapts the same data responsively. */}
      <table className={cn("comparison-table", condensed && "comparison-table--condensed")}>
        <thead>
          <tr className="comparison-table__head">
            <th scope="col" className="comparison-table__cell comparison-table__cell--label">
              <span className="sr-only">Feature</span>
            </th>
            {columns.map((column) => (
              <th scope="col" key={column.key ?? column.label} className="comparison-table__cell">
                {column.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.key ?? row.label} className="comparison-table__row">
              <th scope="row" className="comparison-table__cell comparison-table__cell--label">
                {row.label}
              </th>
              {columns.map((column) => {
                const cellKey = column.key ?? column.label;
                const value = row[cellKey] ?? row.cells?.[cellKey];
                return (
                  <td key={`${row.key ?? row.label}-${cellKey}`} className="comparison-table__cell">
                    {renderSlot(value ?? "—")}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
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
