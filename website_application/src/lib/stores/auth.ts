import { writable } from "svelte/store";
import { authAPI } from "$lib/authAPI.js";
import { initializeWebSocket, disconnectWebSocket } from "./realtime.js";

interface User {
  id: string;
  email: string;
  tenant_id?: string;
  email_verified?: boolean;
  first_name?: string;
  last_name?: string;
  role?: string;
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
}

function createAuthStore() {
  const { subscribe, set, update } = writable<AuthState>({
    isAuthenticated: false,
    user: null,
    loading: true,
    error: null,
    initialized: false,
  });

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
            : "Login failed";
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
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

    async checkAuth() {
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

      if (currentState.initialized && !currentState.loading) {
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

        initializeWebSocket();
      } catch {
        // Not authenticated or token expired - try refresh
        try {
          const refreshResponse = await authAPI.post<{ user: User }>("/refresh");
          const { user } = refreshResponse.data;

          localStorage.setItem("user", JSON.stringify(user));

          set({
            isAuthenticated: true,
            user,
            loading: false,
            error: null,
            initialized: true,
          });

          initializeWebSocket();
        } catch {
          // Refresh failed - user is not authenticated
          localStorage.removeItem("user");

          set({
            isAuthenticated: false,
            user: null,
            loading: false,
            error: null,
            initialized: true,
          });
        }
      }
    },

    async logout() {
      try {
        // Backend clears all auth cookies
        await authAPI.post("/logout");
      } catch {
        // Ignore errors - clear local state anyway
      }

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
