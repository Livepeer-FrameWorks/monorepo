import type { ErrorInfo, ReactNode } from "react";
import React, { Component } from "react";
import { Button } from "../ui/button";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
  onError?: (error: Error, errorInfo: ErrorInfo) => void;
  onRetry?: () => void;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

/**
 * Error boundary to catch and handle player errors gracefully.
 * Prevents player errors from crashing the parent application.
 */
class PlayerErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo): void {
    console.error("[PlayerErrorBoundary] Caught error:", error, errorInfo);
    this.props.onError?.(error, errorInfo);
  }

  handleRetry = (): void => {
    this.setState({ hasError: false, error: null });
    this.props.onRetry?.();
  };

  render(): ReactNode {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }

      return (
        <div className="fw-player-error flex min-h-[280px] flex-col items-center justify-center gap-4 rounded-xl bg-slate-950 p-6 text-center text-white">
          <div className="text-lg font-semibold text-red-400">Playback Error</div>
          <p className="max-w-sm text-sm text-slate-400">
            {this.state.error?.message || "An unexpected error occurred while loading the player."}
          </p>
          <Button type="button" variant="secondary" onClick={this.handleRetry} className="mt-2">
            Try Again
          </Button>
        </div>
      );
    }

    return this.props.children;
  }
}

export default PlayerErrorBoundary;
