import { Component, type ReactNode, type ErrorInfo } from "react";
import { Button } from "@/components/ui/button";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: null };

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error("Player error:", error, errorInfo);
  }

  handleRetry = () => {
    this.setState({ hasError: false, error: null });
  };

  render() {
    if (this.state.hasError) {
      return (
        this.props.fallback ?? (
          <div className="flex h-full flex-col items-center justify-center gap-4 bg-destructive/10 p-6">
            <p className="text-sm font-medium text-destructive">Player crashed</p>
            <p className="text-xs text-muted-foreground">{this.state.error?.message ?? "Unknown error"}</p>
            <Button size="sm" variant="outline" onClick={this.handleRetry}>
              Retry
            </Button>
          </div>
        )
      );
    }
    return this.props.children;
  }
}
