/**
 * Hand-authored Tailwind-equivalent utility classes for Shadow DOM.
 * Only includes utilities actually used by the React player components.
 * ~4KB gzipped — avoids bundling all of Tailwind into shadow DOM.
 */
import { css } from "lit";

export const utilityStyles = css`
  /* ======== Layout & Positioning ======== */
  .absolute {
    position: absolute;
  }
  .relative {
    position: relative;
  }
  .inset-0 {
    inset: 0;
  }
  .inset-x-0 {
    left: 0;
    right: 0;
  }
  .top-0 {
    top: 0;
  }
  .top-2 {
    top: 0.5rem;
  }
  .top-3 {
    top: 0.75rem;
  }
  .left-0 {
    left: 0;
  }
  .left-3 {
    left: 0.75rem;
  }
  .left-4 {
    left: 1rem;
  }
  .right-0 {
    right: 0;
  }
  .right-2 {
    right: 0.5rem;
  }
  .right-3 {
    right: 0.75rem;
  }
  .right-4 {
    right: 1rem;
  }
  .bottom-0 {
    bottom: 0;
  }
  .bottom-3 {
    bottom: 0.75rem;
  }
  .bottom-4 {
    bottom: 1rem;
  }
  .bottom-20 {
    bottom: 5rem;
  }
  .left-1\\/2 {
    left: 50%;
  }
  .top-1\\/4 {
    top: 25%;
  }

  /* ======== Display & Flex ======== */
  .hidden {
    display: none;
  }
  .flex {
    display: flex;
  }
  .inline-flex {
    display: inline-flex;
  }
  .inline-block {
    display: inline-block;
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
  .shrink-0 {
    flex-shrink: 0;
  }
  .items-center {
    align-items: center;
  }
  .items-start {
    align-items: flex-start;
  }
  .items-end {
    align-items: flex-end;
  }
  .justify-center {
    justify-content: center;
  }
  .justify-between {
    justify-content: space-between;
  }
  .justify-start {
    justify-content: flex-start;
  }
  .justify-end {
    justify-content: flex-end;
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
  .gap-6 {
    gap: 1.5rem;
  }
  .gap-8 {
    gap: 2rem;
  }

  /* ======== Sizing ======== */
  .w-full {
    width: 100%;
  }
  .w-4 {
    width: 1rem;
  }
  .w-5 {
    width: 1.25rem;
  }
  .w-8 {
    width: 2rem;
  }
  .w-20 {
    width: 5rem;
  }
  .h-full {
    height: 100%;
  }
  .h-1\\.5 {
    height: 0.375rem;
  }
  .h-4 {
    height: 1rem;
  }
  .h-5 {
    height: 1.25rem;
  }
  .h-6 {
    height: 1.5rem;
  }
  .h-8 {
    height: 2rem;
  }
  .h-20 {
    height: 5rem;
  }
  .min-w-0 {
    min-width: 0;
  }
  .max-w-sm {
    max-width: 24rem;
  }
  .max-w-\\[320px\\] {
    max-width: 320px;
  }
  .max-w-\\[90\\%\\] {
    max-width: 90%;
  }
  .max-w-\\[80\\%\\] {
    max-width: 80%;
  }
  .max-w-\\[70\\%\\] {
    max-width: 70%;
  }
  .max-h-\\[72px\\] {
    max-height: 72px;
  }
  .max-h-\\[80\\%\\] {
    max-height: 80%;
  }
  .min-h-\\[280px\\] {
    min-height: 280px;
  }
  .min-h-\\[300px\\] {
    min-height: 300px;
  }
  .w-\\[280px\\] {
    width: 280px;
  }

  /* ======== Spacing ======== */
  .p-1 {
    padding: 0.25rem;
  }
  .p-2 {
    padding: 0.5rem;
  }
  .p-4 {
    padding: 1rem;
  }
  .p-5 {
    padding: 1.25rem;
  }
  .p-6 {
    padding: 1.5rem;
  }
  .px-2\\.5 {
    padding-left: 0.625rem;
    padding-right: 0.625rem;
  }
  .px-3 {
    padding-left: 0.75rem;
    padding-right: 0.75rem;
  }
  .px-4 {
    padding-left: 1rem;
    padding-right: 1rem;
  }
  .px-6 {
    padding-left: 1.5rem;
    padding-right: 1.5rem;
  }
  .py-1 {
    padding-top: 0.25rem;
    padding-bottom: 0.25rem;
  }
  .py-2 {
    padding-top: 0.5rem;
    padding-bottom: 0.5rem;
  }
  .py-2\\.5 {
    padding-top: 0.625rem;
    padding-bottom: 0.625rem;
  }
  .py-3 {
    padding-top: 0.75rem;
    padding-bottom: 0.75rem;
  }
  .pl-8 {
    padding-left: 2rem;
  }
  .pr-8 {
    padding-right: 2rem;
  }
  .pb-2 {
    padding-bottom: 0.5rem;
  }
  .pb-3 {
    padding-bottom: 0.75rem;
  }
  .pt-2 {
    padding-top: 0.5rem;
  }
  .mt-0\\.5 {
    margin-top: 0.125rem;
  }
  .mt-1 {
    margin-top: 0.25rem;
  }
  .mt-1\\.5 {
    margin-top: 0.375rem;
  }
  .mt-2 {
    margin-top: 0.5rem;
  }
  .mt-3 {
    margin-top: 0.75rem;
  }
  .mb-1 {
    margin-bottom: 0.25rem;
  }
  .ml-0\\.5 {
    margin-left: 0.125rem;
  }
  .-ml-4 {
    margin-left: -1rem;
  }

  /* ======== Typography ======== */
  .text-xs {
    font-size: 0.75rem;
    line-height: 1rem;
  }
  .text-sm {
    font-size: 0.875rem;
    line-height: 1.25rem;
  }
  .text-base {
    font-size: 1rem;
    line-height: 1.5rem;
  }
  .text-lg {
    font-size: 1.125rem;
    line-height: 1.75rem;
  }
  .text-center {
    text-align: center;
  }
  .text-right {
    text-align: right;
  }
  .font-mono {
    font-family:
      ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New",
      monospace;
  }
  .font-medium {
    font-weight: 500;
  }
  .font-semibold {
    font-weight: 600;
  }
  .font-bold {
    font-weight: 700;
  }
  .truncate {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .whitespace-pre-wrap {
    white-space: pre-wrap;
  }
  .tabular-nums {
    font-variant-numeric: tabular-nums;
  }
  .tracking-wider {
    letter-spacing: 0.05em;
  }
  .uppercase {
    text-transform: uppercase;
  }

  /* ======== Colors ======== */
  .text-white {
    color: white;
  }
  .text-white\\/15 {
    color: rgb(255 255 255 / 0.15);
  }
  .text-white\\/50 {
    color: rgb(255 255 255 / 0.5);
  }
  .text-white\\/60 {
    color: rgb(255 255 255 / 0.6);
  }
  .text-white\\/70 {
    color: rgb(255 255 255 / 0.7);
  }
  .text-white\\/80 {
    color: rgb(255 255 255 / 0.8);
  }
  .text-white\\/90 {
    color: rgb(255 255 255 / 0.9);
  }

  /* ======== Backgrounds ======== */
  .bg-black {
    background-color: black;
  }
  .bg-black\\/40 {
    background-color: rgb(0 0 0 / 0.4);
  }
  .bg-black\\/70 {
    background-color: rgb(0 0 0 / 0.7);
  }
  .bg-black\\/80 {
    background-color: rgb(0 0 0 / 0.8);
  }
  .bg-white {
    background-color: white;
  }
  .bg-white\\/10 {
    background-color: rgb(255 255 255 / 0.1);
  }
  .bg-white\\/15 {
    background-color: rgb(255 255 255 / 0.15);
  }
  .bg-slate-400 {
    background-color: rgb(148 163 184);
  }
  .bg-slate-900 {
    background-color: rgb(15 23 42);
  }
  .bg-slate-950 {
    background-color: rgb(2 6 23);
  }
  .bg-red-400 {
    background-color: rgb(248 113 113);
  }

  /* ======== Gradients ======== */
  .bg-gradient-to-b {
    background-image: linear-gradient(to bottom, var(--tw-gradient-stops));
  }
  .from-black\\/70 {
    --tw-gradient-from: rgb(0 0 0 / 0.7);
    --tw-gradient-stops: var(--tw-gradient-from), var(--tw-gradient-to, rgb(0 0 0 / 0));
  }
  .via-black\\/40 {
    --tw-gradient-via: rgb(0 0 0 / 0.4);
    --tw-gradient-stops:
      var(--tw-gradient-from), var(--tw-gradient-via), var(--tw-gradient-to, rgb(0 0 0 / 0));
  }
  .to-transparent {
    --tw-gradient-to: transparent;
  }
  .from-slate-900 {
    --tw-gradient-from: rgb(15 23 42);
    --tw-gradient-stops: var(--tw-gradient-from), var(--tw-gradient-to, rgb(15 23 42 / 0));
  }
  .via-slate-950 {
    --tw-gradient-via: rgb(2 6 23);
    --tw-gradient-stops:
      var(--tw-gradient-from), var(--tw-gradient-via), var(--tw-gradient-to, rgb(2 6 23 / 0));
  }
  .to-slate-900 {
    --tw-gradient-to: rgb(15 23 42);
  }

  /* ======== Borders ======== */
  .border {
    border-width: 1px;
  }
  .border-b {
    border-bottom-width: 1px;
  }
  .border-2 {
    border-width: 2px;
  }
  .border-white\\/10 {
    border-color: rgb(255 255 255 / 0.1);
  }
  .border-white\\/15 {
    border-color: rgb(255 255 255 / 0.15);
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
  .rounded-xl {
    border-radius: 0.75rem;
  }
  .rounded-full {
    border-radius: 9999px;
  }
  .rounded-\\[4px\\] {
    border-radius: 4px;
  }
  .outline-none {
    outline: 2px solid transparent;
    outline-offset: 2px;
  }

  /* ======== Opacity ======== */
  .opacity-0 {
    opacity: 0;
  }
  .opacity-25 {
    opacity: 0.25;
  }
  .opacity-50 {
    opacity: 0.5;
  }
  .opacity-60 {
    opacity: 0.6;
  }
  .opacity-70 {
    opacity: 0.7;
  }
  .opacity-75 {
    opacity: 0.75;
  }
  .opacity-90 {
    opacity: 0.9;
  }
  .opacity-100 {
    opacity: 1;
  }

  /* ======== Effects ======== */
  .shadow-lg {
    box-shadow:
      0 10px 15px -3px rgb(0 0 0 / 0.1),
      0 4px 6px -4px rgb(0 0 0 / 0.1);
  }
  .shadow-xl {
    box-shadow:
      0 20px 25px -5px rgb(0 0 0 / 0.1),
      0 8px 10px -6px rgb(0 0 0 / 0.1);
  }
  .shadow-inner {
    box-shadow: inset 0 2px 4px 0 rgb(0 0 0 / 0.05);
  }
  .backdrop-blur {
    backdrop-filter: blur(8px);
  }
  .backdrop-blur-sm {
    backdrop-filter: blur(4px);
  }

  /* ======== Overflow ======== */
  .overflow-hidden {
    overflow: hidden;
  }
  .overflow-auto {
    overflow: auto;
  }

  /* ======== Interaction ======== */
  .pointer-events-none {
    pointer-events: none;
  }
  .pointer-events-auto {
    pointer-events: auto;
  }
  .cursor-pointer {
    cursor: pointer;
  }
  .cursor-not-allowed {
    cursor: not-allowed;
  }

  /* ======== Z-Index ======== */
  .z-5 {
    z-index: 5;
  }
  .z-10 {
    z-index: 10;
  }
  .z-20 {
    z-index: 20;
  }
  .z-30 {
    z-index: 30;
  }
  .z-40 {
    z-index: 40;
  }
  .z-100 {
    z-index: 100;
  }

  /* ======== Transforms ======== */
  .transform {
    transform: var(--tw-transform);
  }
  .scale-50 {
    transform: scale(0.5);
  }
  .scale-75 {
    transform: scale(0.75);
  }
  .scale-90 {
    transform: scale(0.9);
  }
  .scale-100 {
    transform: scale(1);
  }
  .scale-110 {
    transform: scale(1.1);
  }
  .scale-120 {
    transform: scale(1.2);
  }
  .-translate-x-1\\/2 {
    transform: translateX(-50%);
  }
  .translate-x-1\\/2 {
    transform: translateX(50%);
  }
  .rotate-45 {
    transform: rotate(45deg);
  }
  .-rotate-45 {
    transform: rotate(-45deg);
  }
  .rotate-90 {
    transform: rotate(90deg);
  }

  /* ======== Transitions ======== */
  .transition {
    transition-property:
      color, background-color, border-color, text-decoration-color, fill, stroke, opacity,
      box-shadow, transform, filter, backdrop-filter;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 150ms;
  }
  .transition-all {
    transition-property: all;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 150ms;
  }
  .transition-colors {
    transition-property: color, background-color, border-color, text-decoration-color, fill, stroke;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 150ms;
  }
  .transition-opacity {
    transition-property: opacity;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 150ms;
  }
  .transition-transform {
    transition-property: transform;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 150ms;
  }
  .duration-150 {
    transition-duration: 150ms;
  }
  .duration-200 {
    transition-duration: 200ms;
  }
  .duration-300 {
    transition-duration: 300ms;
  }
  .duration-500 {
    transition-duration: 500ms;
  }
  .ease-out {
    transition-timing-function: cubic-bezier(0, 0, 0.2, 1);
  }
  .ease-in-out {
    transition-timing-function: cubic-bezier(0.4, 0, 0.6, 1);
  }

  /* ======== Animations ======== */
  .animate-spin {
    animation: _fw-spin 1s linear infinite;
  }
  .animate-pulse {
    animation: _fw-pulse 2s cubic-bezier(0.4, 0, 0.6, 1) infinite;
  }
  @keyframes _fw-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @keyframes _fw-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.5;
    }
  }

  /* ======== Responsive (sm: 640px+) ======== */
  @media (min-width: 640px) {
    .sm\\:flex {
      display: flex;
    }
    .sm\\:left-4 {
      left: 1rem;
    }
    .sm\\:top-4 {
      top: 1rem;
    }
    .sm\\:right-4 {
      right: 1rem;
    }
    .sm\\:gap-6 {
      gap: 1.5rem;
    }
  }

  /* ======== Hover states ======== */
  .hover\\:bg-white\\/10:hover {
    background-color: rgb(255 255 255 / 0.1);
  }
  .hover\\:text-white:hover {
    color: white;
  }
  .hover\\:rotate-90:hover {
    transform: rotate(90deg);
  }

  /* ======== Focus-visible states ======== */
  .focus-visible\\:ring-2:focus-visible {
    box-shadow: 0 0 0 2px var(--tw-ring-color, rgb(59 130 246));
  }
  .focus-visible\\:ring-offset-2:focus-visible {
    box-shadow:
      0 0 0 2px var(--tw-ring-offset-color, white),
      0 0 0 4px var(--tw-ring-color, rgb(59 130 246));
  }

  /* ======== Group hover ======== */
  .group:hover .group-hover\\:rotate-90 {
    transform: rotate(90deg);
  }
`;
