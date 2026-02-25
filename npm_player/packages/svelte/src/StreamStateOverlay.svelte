<!--
  StreamStateOverlay.svelte - Stream status overlay
  Port of src/components/StreamStateOverlay.tsx

  Shows stream status when not playable:
  - Status-specific icons (online, offline, initializing, shutting down, error)
  - Progress bar for INITIALIZING state
  - Context-aware messaging
  - Polling indicator
  - Retry button for errors
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import type { StreamStatus } from "@livepeer-frameworks/player-core";

  interface Props {
    /** Current stream status */
    status: StreamStatus;
    /** Human-readable message */
    message: string;
    /** Processing percentage (for INITIALIZING state) */
    percentage?: number;
    /** Callback for retry button */
    onRetry?: () => void;
    /** Whether to show the overlay */
    visible?: boolean;
    /** Additional className */
    class?: string;
  }

  let {
    status,
    message,
    percentage,
    onRetry,
    visible = true,
    class: className = "",
  }: Props = $props();

  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);

  // Computed states
  let showRetry = $derived(status === "ERROR" || status === "INVALID" || status === "OFFLINE");
  let showProgress = $derived(status === "INITIALIZING" && percentage !== undefined);

  // Get status label for header
  function getStatusLabel(status: StreamStatus): string {
    switch (status) {
      case "ONLINE":
        return "ONLINE";
      case "OFFLINE":
        return "OFFLINE";
      case "INITIALIZING":
        return "INITIALIZING";
      case "BOOTING":
        return "STARTING";
      case "WAITING_FOR_DATA":
        return "WAITING";
      case "SHUTTING_DOWN":
        return "ENDING";
      case "ERROR":
        return "ERROR";
      case "INVALID":
        return "INVALID";
      default:
        return "STATUS";
    }
  }
</script>

{#if visible && status !== "ONLINE"}
  <div class="overlay-backdrop {className}" role="status" aria-live="polite">
    <div class="slab">
      <!-- Slab header - status label with icon -->
      <div class="slab-header">
        <!-- Status Icon -->
        {#if status === "OFFLINE"}
          <svg class="icon icon-offline" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414"
            />
          </svg>
        {:else if status === "INITIALIZING" || status === "BOOTING" || status === "WAITING_FOR_DATA"}
          <svg class="icon icon-warning animate-spin" fill="none" viewBox="0 0 24 24">
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            />
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            />
          </svg>
        {:else if status === "SHUTTING_DOWN"}
          <svg class="icon icon-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M13 10V3L4 14h7v7l9-11h-7z"
            />
          </svg>
        {:else}
          <!-- ERROR or INVALID -->
          <svg class="icon icon-offline" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
            />
          </svg>
        {/if}

        <span>{getStatusLabel(status)}</span>
      </div>

      <!-- Slab body - message and progress -->
      <div class="slab-body">
        <p style="font-size: 0.875rem; color: hsl(var(--tn-fg, 233 23% 75%));">
          {message}
        </p>

        {#if showProgress && percentage !== undefined}
          <div style="margin-top: 0.75rem;">
            <div class="progress-bar">
              <div class="progress-fill" style="width: {Math.min(100, percentage)}%;"></div>
            </div>
            <p
              style="margin-top: 0.375rem; font-size: 0.75rem; font-family: monospace; color: hsl(var(--tn-fg-dark, 233 23% 60%));"
            >
              {Math.round(percentage)}%
            </p>
          </div>
        {/if}

        {#if status === "OFFLINE"}
          <p
            style="margin-top: 0.5rem; font-size: 0.75rem; color: hsl(var(--tn-fg-dark, 233 23% 60%));"
          >
            {t("broadcasterGoLive")}
          </p>
        {/if}

        {#if status === "BOOTING" || status === "WAITING_FOR_DATA"}
          <p
            style="margin-top: 0.5rem; font-size: 0.75rem; color: hsl(var(--tn-fg-dark, 233 23% 60%));"
          >
            {t("streamPreparing")}
          </p>
        {/if}

        <!-- Polling indicator for non-error states -->
        {#if !showRetry}
          <div class="polling-indicator">
            <span class="polling-dot"></span>
            <span>{t("checkingStatus")}</span>
          </div>
        {/if}
      </div>

      <!-- Slab actions - flush retry button -->
      {#if showRetry && onRetry}
        <div class="slab-actions">
          <button
            type="button"
            class="btn-flush"
            onclick={onRetry}
            aria-label={t("retryConnection")}
          >
            {t("retryConnection")}
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .overlay-backdrop {
    position: absolute;
    inset: 0;
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: center;
    background-color: hsl(var(--tn-bg-dark, 235 21% 11%) / 0.8);
    backdrop-filter: blur(4px);
  }

  .slab {
    width: 280px;
    max-width: 90%;
    background-color: hsl(var(--tn-bg, 233 23% 17%) / 0.95);
    border: 1px solid hsl(var(--tn-border, 233 23% 25%) / 0.3);
  }

  .slab-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid hsl(var(--tn-border, 233 23% 25%) / 0.3);
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: hsl(var(--tn-fg-dark, 233 23% 60%));
  }

  .slab-body {
    padding: 1rem;
  }

  .slab-actions {
    border-top: 1px solid hsl(var(--tn-border, 233 23% 25%) / 0.3);
  }

  .btn-flush {
    width: 100%;
    padding: 0.625rem 1rem;
    background: none;
    border: none;
    cursor: pointer;
    font-size: 0.75rem;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: hsl(var(--tn-blue, 217 89% 71%));
    transition: background-color 0.15s;
  }

  .btn-flush:hover {
    background-color: hsl(var(--tn-bg-visual, 233 23% 20%) / 0.5);
  }

  .progress-bar {
    height: 0.375rem;
    width: 100%;
    overflow: hidden;
    background-color: hsl(var(--tn-bg-visual, 233 23% 20%));
  }

  .progress-fill {
    height: 100%;
    transition: width 0.3s ease;
    background-color: hsl(var(--tn-yellow, 40 70% 64%));
  }

  .icon {
    width: 1.25rem;
    height: 1.25rem;
  }

  .icon-online {
    color: hsl(var(--tn-green, 115 54% 57%));
  }

  .icon-offline {
    color: hsl(var(--tn-red, 355 68% 65%));
  }

  .icon-warning {
    color: hsl(var(--tn-yellow, 40 70% 64%));
  }

  .polling-indicator {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.75rem;
    font-size: 0.75rem;
    color: hsl(var(--tn-fg-dark, 233 23% 60%));
  }

  .polling-dot {
    width: 0.375rem;
    height: 0.375rem;
    background-color: hsl(var(--tn-cyan, 192 78% 73%));
    animation: pulse 2s cubic-bezier(0.4, 0, 0.6, 1) infinite;
  }

  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.5;
    }
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  .animate-spin {
    animation: spin 1s linear infinite;
  }
</style>
