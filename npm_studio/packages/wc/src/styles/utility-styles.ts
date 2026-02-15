/**
 * Minimal utility styles for StreamCrafter WC components.
 * Subset of Tailwind-equivalent classes used across the React components.
 */
import { css } from "lit";

export const utilityStyles = css`
  .flex {
    display: flex;
  }
  .inline-flex {
    display: inline-flex;
  }
  .block {
    display: block;
  }
  .hidden {
    display: none;
  }
  .contents {
    display: contents;
  }
  .flex-col {
    flex-direction: column;
  }
  .flex-row {
    flex-direction: row;
  }
  .flex-1 {
    flex: 1 1 0%;
  }
  .flex-none {
    flex: none;
  }
  .flex-wrap {
    flex-wrap: wrap;
  }
  .items-center {
    align-items: center;
  }
  .items-start {
    align-items: flex-start;
  }
  .justify-center {
    justify-content: center;
  }
  .justify-between {
    justify-content: space-between;
  }
  .gap-0\\.5 {
    gap: 0.125rem;
  }
  .gap-1 {
    gap: 0.25rem;
  }
  .gap-2 {
    gap: 0.5rem;
  }
  .gap-3 {
    gap: 0.75rem;
  }
  .gap-4 {
    gap: 1rem;
  }
  .w-full {
    width: 100%;
  }
  .h-full {
    height: 100%;
  }
  .min-w-0 {
    min-width: 0;
  }
  .relative {
    position: relative;
  }
  .absolute {
    position: absolute;
  }
  .inset-0 {
    inset: 0;
  }
  .overflow-hidden {
    overflow: hidden;
  }
  .overflow-auto {
    overflow: auto;
  }
  .text-xs {
    font-size: 0.75rem;
    line-height: 1rem;
  }
  .text-sm {
    font-size: 0.875rem;
    line-height: 1.25rem;
  }
  .font-mono {
    font-family:
      ui-monospace,
      SFMono-Regular,
      SF Mono,
      Menlo,
      Consolas,
      monospace;
  }
  .font-medium {
    font-weight: 500;
  }
  .font-semibold {
    font-weight: 600;
  }
  .uppercase {
    text-transform: uppercase;
  }
  .tracking-wider {
    letter-spacing: 0.05em;
  }
  .whitespace-nowrap {
    white-space: nowrap;
  }
  .tabular-nums {
    font-variant-numeric: tabular-nums;
  }
  .rounded {
    border-radius: 0.25rem;
  }
  .rounded-md {
    border-radius: 0.375rem;
  }
  .rounded-lg {
    border-radius: 0.5rem;
  }
  .border-none {
    border: none;
  }
  .cursor-pointer {
    cursor: pointer;
  }
  .cursor-not-allowed {
    cursor: not-allowed;
  }
  .pointer-events-none {
    pointer-events: none;
  }
  .select-none {
    user-select: none;
  }
  .opacity-50 {
    opacity: 0.5;
  }
  .opacity-70 {
    opacity: 0.7;
  }
  .transition {
    transition: all 150ms ease;
  }
  .p-0 {
    padding: 0;
  }
  .p-2 {
    padding: 0.5rem;
  }
  .px-2 {
    padding-left: 0.5rem;
    padding-right: 0.5rem;
  }
  .py-1 {
    padding-top: 0.25rem;
    padding-bottom: 0.25rem;
  }
  .m-0 {
    margin: 0;
  }
  .bg-none {
    background: none;
  }
`;
