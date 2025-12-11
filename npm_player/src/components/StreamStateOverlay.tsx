import React from 'react';
import type { StreamStatus } from '../types';

export interface StreamStateOverlayProps {
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
  className?: string;
}

/**
 * Get status icon based on stream state
 */
function StatusIcon({ status }: { status: StreamStatus }) {
  const iconClass = "w-5 h-5";

  switch (status) {
    case 'ONLINE':
      return (
        <svg className={`${iconClass} fw-status-online`} fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" />
        </svg>
      );

    case 'OFFLINE':
      return (
        <svg className={`${iconClass} fw-status-offline`} fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414" />
        </svg>
      );

    case 'INITIALIZING':
    case 'BOOTING':
    case 'WAITING_FOR_DATA':
      return (
        <svg className={`${iconClass} fw-status-warning animate-spin`} fill="none" viewBox="0 0 24 24">
          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
        </svg>
      );

    case 'SHUTTING_DOWN':
      return (
        <svg className={`${iconClass} fw-status-warning`} fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 10V3L4 14h7v7l9-11h-7z" />
        </svg>
      );

    case 'ERROR':
    case 'INVALID':
    default:
      return (
        <svg className={`${iconClass} fw-status-offline`} fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
        </svg>
      );
  }
}

/**
 * Get status label for header
 */
function getStatusLabel(status: StreamStatus): string {
  switch (status) {
    case 'ONLINE': return 'ONLINE';
    case 'OFFLINE': return 'OFFLINE';
    case 'INITIALIZING': return 'INITIALIZING';
    case 'BOOTING': return 'STARTING';
    case 'WAITING_FOR_DATA': return 'WAITING';
    case 'SHUTTING_DOWN': return 'ENDING';
    case 'ERROR': return 'ERROR';
    case 'INVALID': return 'INVALID';
    default: return 'STATUS';
  }
}

/**
 * StreamStateOverlay - Shows stream status when not playable
 *
 * Slab-based design with header/body/actions zones.
 * Uses Tokyo Night color palette and seam-based layout.
 */
export const StreamStateOverlay: React.FC<StreamStateOverlayProps> = ({
  status,
  message,
  percentage,
  onRetry,
  visible = true,
  className = '',
}) => {
  if (!visible || status === 'ONLINE') {
    return null;
  }

  const showRetry = status === 'ERROR' || status === 'INVALID' || status === 'OFFLINE';
  const showProgress = status === 'INITIALIZING' && percentage !== undefined;

  return (
    <div
      className={`absolute inset-0 z-20 flex items-center justify-center ${className}`}
      style={{ backgroundColor: 'hsl(var(--tn-bg-dark) / 0.8)', backdropFilter: 'blur(4px)' }}
      role="status"
      aria-live="polite"
    >
      {/* Slab container - no rounded corners, seam borders */}
      <div
        className="fw-slab w-[280px] max-w-[90%]"
        style={{ backgroundColor: 'hsl(var(--tn-bg) / 0.95)' }}
      >
        {/* Slab header - status label with icon */}
        <div className="fw-slab-header flex items-center gap-2">
          <StatusIcon status={status} />
          <span>{getStatusLabel(status)}</span>
        </div>

        {/* Slab body - message and progress */}
        <div className="fw-slab-body">
          <p className="text-sm" style={{ color: 'hsl(var(--tn-fg))' }}>
            {message}
          </p>

          {showProgress && (
            <div className="mt-3">
              {/* Progress bar - no rounded corners */}
              <div
                className="h-1.5 w-full overflow-hidden"
                style={{ backgroundColor: 'hsl(var(--tn-bg-visual))' }}
              >
                <div
                  className="h-full transition-all duration-300"
                  style={{
                    width: `${Math.min(100, percentage)}%`,
                    backgroundColor: 'hsl(var(--tn-yellow))',
                  }}
                />
              </div>
              <p
                className="mt-1.5 text-xs font-mono"
                style={{ color: 'hsl(var(--tn-fg-dark))' }}
              >
                {Math.round(percentage)}%
              </p>
            </div>
          )}

          {status === 'OFFLINE' && (
            <p className="mt-2 text-xs" style={{ color: 'hsl(var(--tn-fg-dark))' }}>
              The stream will start when the broadcaster goes live
            </p>
          )}

          {(status === 'BOOTING' || status === 'WAITING_FOR_DATA') && (
            <p className="mt-2 text-xs" style={{ color: 'hsl(var(--tn-fg-dark))' }}>
              Please wait while the stream prepares...
            </p>
          )}

          {/* Polling indicator for non-error states */}
          {!showRetry && (
            <div
              className="mt-3 flex items-center gap-2 text-xs"
              style={{ color: 'hsl(var(--tn-fg-dark))' }}
            >
              <span
                className="h-1.5 w-1.5 animate-pulse"
                style={{ backgroundColor: 'hsl(var(--tn-cyan))' }}
              />
              <span>Checking stream status...</span>
            </div>
          )}
        </div>

        {/* Slab actions - flush retry button */}
        {showRetry && onRetry && (
          <div className="fw-slab-actions">
            <button
              type="button"
              onClick={onRetry}
              className="fw-btn-flush py-2.5 text-xs font-medium uppercase tracking-wide"
              style={{ color: 'hsl(var(--tn-blue))' }}
              aria-label="Retry connection"
            >
              Retry Connection
            </button>
          </div>
        )}
      </div>
    </div>
  );
};

export default StreamStateOverlay;
