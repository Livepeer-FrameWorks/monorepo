import { writable } from "svelte/store";

// Generic GraphQL error interface (compatible with Houdini errors)
interface GraphQLError {
  message: string;
  extensions?: {
    code?: string;
    [key: string]: unknown;
  };
}

interface GraphQLErrorLike {
  graphQLErrors?: GraphQLError[];
  networkError?: Error & { statusCode?: number };
  message?: string;
}

interface ErrorBoundaryState {
  hasError: boolean;
  error: Error | null;
  errorMessage: string | null;
  errorInfo: string | null;
}

function createErrorBoundaryStore() {
  const { subscribe, set } = writable<ErrorBoundaryState>({
    hasError: false,
    error: null,
    errorMessage: null,
    errorInfo: null,
  });

  return {
    subscribe,

    setError(
      error: Error,
      userMessage?: string,
      additionalInfo?: string,
    ): void {
      console.error("ErrorBoundary caught error:", error);

      set({
        hasError: true,
        error,
        errorMessage: userMessage || "An unexpected error occurred",
        errorInfo: additionalInfo || error.message,
      });
    },

    clearError(): void {
      set({
        hasError: false,
        error: null,
        errorMessage: null,
        errorInfo: null,
      });
    },

    handleGraphQLError(graphQLError: GraphQLErrorLike): void {
      let userMessage = "Failed to load data";
      let additionalInfo = "";

      if (graphQLError.networkError) {
        const hasStatusCode =
          typeof graphQLError.networkError === "object" &&
          graphQLError.networkError &&
          "statusCode" in graphQLError.networkError;
        const statusCode = hasStatusCode
          ? (graphQLError.networkError as { statusCode?: number }).statusCode
          : undefined;

        if (statusCode === 401) {
          userMessage = "Authentication required. Please log in again.";
        } else if (typeof statusCode === "number" && statusCode >= 500) {
          userMessage = "Server error. Please try again later.";
        } else {
          userMessage = "Network error. Please check your connection.";
        }
      } else if (
        graphQLError.graphQLErrors &&
        graphQLError.graphQLErrors.length > 0
      ) {
        const firstError = graphQLError.graphQLErrors[0];
        if (firstError.extensions?.code === "FORBIDDEN") {
          userMessage = "You do not have permission to access this resource.";
        } else if (firstError.extensions?.code === "UNAUTHENTICATED") {
          userMessage = "Please log in to continue.";
        } else {
          userMessage = firstError.message || "GraphQL request failed";
        }
      }

      this.setError(new Error(userMessage), userMessage, additionalInfo);
    },
  };
}

export const errorBoundary = createErrorBoundaryStore();
