import { writable } from "svelte/store";
import { browser } from "$app/environment";
import { authAPI } from "$lib/authAPI.js";
import { refreshAuthSession } from "$lib/auth/refresh";
import { initializeWebSocket, disconnectWebSocket } from "./realtime.js";

interface User {
  id: string;
  email: string;
  tenant_id?: string;
  email_verified?: boolean;
  first_name?: string;
  last_name?: string;
  role?: string;
  platform_operator?: boolean;
  created_at?: string;
  is_active?: boolean;
  is_verified?: boolean;
}

interface BotProtectionData {
  turnstileToken?: string;
  human_check?: string;
  phone_number?: string; // Honeypot field
  behavior?: string; // Behavioral data for verification when Turnstile is disabled
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_content?: string;
  utm_term?: string;
  referral_code?: string;
  landing_page?: string;
}

interface AuthState {
  isAuthenticated: boolean;
  user: User | null;
  loading: boolean;
  error: string | null;
  initialized: boolean;
}

interface LoginResponse {
  success: boolean;
  error?: string;
  errorCode?: string;
}

interface AuthErrorResponse {
  error?: unknown;
  error_code?: unknown;
  code?: unknown;
}

function getAuthErrorResponse(error: unknown): AuthErrorResponse | null {
  if (
    !error ||
    typeof error !== "object" ||
    !("response" in error) ||
    !error.response ||
    typeof error.response !== "object" ||
    !("data" in error.response) ||
    !error.response.data ||
    typeof error.response.data !== "object"
  ) {
    return null;
  }
  return error.response.data as AuthErrorResponse;
}

function extractAuthError(
  error: unknown,
  fallback: string
): { message: string; errorCode?: string } {
  const data = getAuthErrorResponse(error);
  const message = typeof data?.error === "string" ? data.error : fallback;
  const errorCode =
    typeof data?.error_code === "string"
      ? data.error_code
      : typeof data?.code === "string"
        ? data.code
        : undefined;
  return { message, errorCode };
}

function createAuthStore() {
  const { subscribe, set, update } = writable<AuthState>({
    isAuthenticated: false,
    user: null,
    loading: true,
    error: null,
    initialized: false,
  });
  let checkAuthPromise: Promise<void> | null = null;
  let refreshTimer: ReturnType<typeof setInterval> | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;

  function stopRefreshLoop() {
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
  }

  // One-shot retry after a transient refresh failure (network blip, backend
  // restart) so a healthy session recovers without waiting a full interval.
  function scheduleRefreshRetry() {
    if (!browser || retryTimer) return;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      void refreshSession({ restartSocket: false });
    }, 30 * 1000);
  }

  function readStoredUser(): User | null {
    try {
      const stored = localStorage.getItem("user");
      return stored ? (JSON.parse(stored) as User) : null;
    } catch {
      return null;
    }
  }

  function startRefreshLoop() {
    if (!browser || refreshTimer) return;
    refreshTimer = setInterval(
      () => {
        void refreshSession({ restartSocket: false });
      },
      10 * 60 * 1000
    );
  }

  async function refreshSession({ restartSocket = true } = {}): Promise<boolean> {
    const result = await refreshAuthSession();

    if (result === "ok") {
      const user = readStoredUser();
      update((state) => ({
        isAuthenticated: true,
        user: user ?? state.user,
        loading: false,
        error: null,
        initialized: true,
      }));

      startRefreshLoop();
      if (restartSocket) {
        initializeWebSocket();
      }
      return true;
    }

    if (result === "unauthorized") {
      // The backend definitively rejected the refresh token: the session is
      // dead and local state must reflect that.
      stopRefreshLoop();
      disconnectWebSocket();
      localStorage.removeItem("user");

      set({
        isAuthenticated: false,
        user: null,
        loading: false,
        error: null,
        initialized: true,
      });
      return false;
    }

    // Transient failure: the cookies may still be valid (e.g. the network
    // isn't back yet after wake-from-sleep). Keep the session and retry.
    update((state) => ({ ...state, loading: false, initialized: true }));
    scheduleRefreshRetry();
    return false;
  }

  return {
    subscribe,

    async login(
      email: string,
      password: string,
      botProtectionData: BotProtectionData = {}
    ): Promise<LoginResponse> {
      update((state) => ({ ...state, loading: true, error: null }));

      try {
        // Backend now sets httpOnly cookies automatically
        // Response only contains user data (no token in body)
        const response = await authAPI.post<{ user: User; expires_at: string }>("/login", {
          email,
          password,
          turnstile_token: botProtectionData.turnstileToken,
          ...botProtectionData,
        });

        const { user } = response.data;

        // Store user in localStorage for client-side access to non-sensitive data
        localStorage.setItem("user", JSON.stringify(user));

        set({
          isAuthenticated: true,
          user,
          loading: false,
          error: null,
          initialized: true,
        });

        // Initialize WebSocket - will use session from Houdini (populated from cookies)
        startRefreshLoop();
        initializeWebSocket();

        return { success: true };
      } catch (error: unknown) {
        const { message: errorMessage, errorCode } = extractAuthError(error, "Login failed");
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage, errorCode };
      }
    },

    async register(
      email: string,
      password: string,
      botProtectionData: BotProtectionData = {},
      firstName: string = "",
      lastName: string = ""
    ): Promise<LoginResponse> {
      update((state) => ({ ...state, loading: true, error: null }));

      try {
        await authAPI.post("/register", {
          email,
          password,
          ...(firstName && { first_name: firstName }),
          ...(lastName && { last_name: lastName }),
          turnstile_token: botProtectionData.turnstileToken,
          ...botProtectionData,
        });

        set({
          isAuthenticated: false,
          user: null,
          loading: false,
          error: null,
          initialized: true,
        });
        return { success: true };
      } catch (error: unknown) {
        const errorMessage =
          error &&
          typeof error === "object" &&
          "response" in error &&
          error.response &&
          typeof error.response === "object" &&
          "data" in error.response &&
          error.response.data &&
          typeof error.response.data === "object" &&
          "error" in error.response.data &&
          typeof error.response.data.error === "string"
            ? error.response.data.error
            : "Registration failed";
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },

    async checkAuth(force = false) {
      if (checkAuthPromise) {
        return checkAuthPromise;
      }

      checkAuthPromise = (async () => {
        const currentState: AuthState = {
          isAuthenticated: false,
          user: null,
          loading: false,
          error: null,
          initialized: false,
        };
        const unsubscribe = subscribe((state) => {
          Object.assign(currentState, state);
        });
        unsubscribe();

        if (!force && currentState.initialized && !currentState.loading) {
          return;
        }

        update((state) => ({ ...state, loading: true }));

        try {
          // Call /me endpoint - cookies sent automatically with withCredentials
          const response = await authAPI.get<User>("/me");
          const user = response.data;

          // Update localStorage with fresh user data
          localStorage.setItem("user", JSON.stringify(user));

          set({
            isAuthenticated: true,
            user,
            loading: false,
            error: null,
            initialized: true,
          });

          startRefreshLoop();
          if (!currentState.isAuthenticated) {
            initializeWebSocket();
          }
        } catch {
          // Not authenticated or token expired - try refresh
          await refreshSession();
        }
      })();

      try {
        await checkAuthPromise;
      } finally {
        checkAuthPromise = null;
      }
    },

    async logout() {
      try {
        // Backend clears all auth cookies
        await authAPI.post("/logout");
      } catch {
        // Ignore errors - clear local state anyway
      }

      stopRefreshLoop();
      disconnectWebSocket();
      localStorage.removeItem("user");

      set({
        isAuthenticated: false,
        user: null,
        loading: false,
        error: null,
        initialized: true,
      });
    },

    async resendVerification(email: string, turnstileToken?: string): Promise<LoginResponse> {
      try {
        const response = await authAPI.post<{
          success: boolean;
          message: string;
        }>("/resend-verification", {
          email,
          turnstile_token: turnstileToken,
        });
        return { success: response.data.success, error: response.data.message };
      } catch (error: unknown) {
        const errorMessage =
          error &&
          typeof error === "object" &&
          "response" in error &&
          error.response &&
          typeof error.response === "object" &&
          "data" in error.response &&
          error.response.data &&
          typeof error.response.data === "object" &&
          "error" in error.response.data &&
          typeof error.response.data.error === "string"
            ? error.response.data.error
            : "Failed to send verification email";
        return { success: false, error: errorMessage };
      }
    },

    async forgotPassword(email: string): Promise<LoginResponse> {
      try {
        const response = await authAPI.post<{
          success: boolean;
          message: string;
        }>("/forgot-password", {
          email,
        });
        return { success: response.data.success, error: response.data.message };
      } catch (error: unknown) {
        const errorMessage =
          error &&
          typeof error === "object" &&
          "response" in error &&
          error.response &&
          typeof error.response === "object" &&
          "data" in error.response &&
          error.response.data &&
          typeof error.response.data === "object" &&
          "error" in error.response.data &&
          typeof error.response.data.error === "string"
            ? error.response.data.error
            : "Failed to send password reset email";
        return { success: false, error: errorMessage };
      }
    },

    async resetPassword(token: string, password: string): Promise<LoginResponse> {
      try {
        const response = await authAPI.post<{
          success: boolean;
          message: string;
        }>("/reset-password", {
          token,
          password,
        });
        return { success: response.data.success, error: response.data.message };
      } catch (error: unknown) {
        const errorMessage =
          error &&
          typeof error === "object" &&
          "response" in error &&
          error.response &&
          typeof error.response === "object" &&
          "data" in error.response &&
          error.response.data &&
          typeof error.response.data === "object" &&
          "error" in error.response.data &&
          typeof error.response.data.error === "string"
            ? error.response.data.error
            : "Failed to reset password";
        return { success: false, error: errorMessage };
      }
    },

    async walletLogin(address: string, message: string, signature: string): Promise<LoginResponse> {
      update((state) => ({ ...state, loading: true, error: null }));

      try {
        // POST to /auth/wallet-login - backend sets httpOnly cookies
        const response = await authAPI.post<{ user: User; expires_at: string }>("/wallet-login", {
          address,
          message,
          signature,
        });

        const { user } = response.data;

        // Store user in localStorage for client-side access
        localStorage.setItem("user", JSON.stringify(user));

        set({
          isAuthenticated: true,
          user,
          loading: false,
          error: null,
          initialized: true,
        });

        // Initialize WebSocket
        initializeWebSocket();

        return { success: true };
      } catch (error: unknown) {
        const errorMessage =
          error &&
          typeof error === "object" &&
          "response" in error &&
          error.response &&
          typeof error.response === "object" &&
          "data" in error.response &&
          error.response.data &&
          typeof error.response.data === "object" &&
          "error" in error.response.data &&
          typeof error.response.data.error === "string"
            ? error.response.data.error
            : "Wallet login failed";
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },
  };
}

export const auth = createAuthStore();
