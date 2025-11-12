import { writable } from "svelte/store";
import { authAPI } from "$lib/authAPI.js";
import { authService } from "$lib/graphql/services/auth.js";
import { initializeWebSocket, disconnectWebSocket } from "./realtime.js";
import type { Stream } from "$lib/graphql/generated/types";

interface User {
  id: string;
  email: string;
  tenant_id: string;
  email_verified: boolean;
}

interface BotProtectionData {
  turnstileToken?: string;
}

interface UserWithStreams {
  user: User;
  streams: Stream[];
}

interface AuthState {
  isAuthenticated: boolean;
  user: UserWithStreams | null;
  token?: string | null;
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
    token: null,
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
        const response = await authAPI.post<{ token: string; user: User }>("/login", {
          email,
          password,
          ...botProtectionData,
        });
        const { token, user } = response.data;

        localStorage.setItem("token", token);
        authAPI.defaults.headers.common["Authorization"] = `Bearer ${token}`;

        localStorage.setItem("user", JSON.stringify(user));

        set({
          isAuthenticated: true,
          user: { user, streams: [] },
          loading: false,
          error: null,
          initialized: true,
        });

        initializeWebSocket(token);

        return { success: true };
      } catch (error: unknown) {
        const errorMessage =
          (error && typeof error === "object" && "response" in error &&
           error.response && typeof error.response === "object" &&
           "data" in error.response && error.response.data &&
           typeof error.response.data === "object" && "error" in error.response.data &&
           typeof error.response.data.error === "string")
            ? error.response.data.error
            : "Login failed";
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },

    async register(
      email: string,
      password: string,
      botProtectionData: BotProtectionData = {}
    ): Promise<LoginResponse> {
      update((state) => ({ ...state, loading: true, error: null }));

      try {
        await authAPI.post("/register", {
          email,
          password,
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
          (error && typeof error === "object" && "response" in error &&
           error.response && typeof error.response === "object" &&
           "data" in error.response && error.response.data &&
           typeof error.response.data === "object" && "error" in error.response.data &&
           typeof error.response.data.error === "string")
            ? error.response.data.error
            : "Registration failed";
        update((state) => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },

    updateStreams(streams: Stream[]) {
      update((state) => {
        if (!state.user) return state;

        return {
          ...state,
          user: {
            ...state.user,
            streams: streams,
          },
        };
      });
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
        const authResult = await authService.checkAuth();

        if (authResult.isAuthenticated && authResult.user) {
          set({
            isAuthenticated: true,
            user: { user: authResult.user, streams: [] },
            token: authResult.token,
            loading: false,
            error: null,
            initialized: true,
          });

          if (authResult.token) {
            initializeWebSocket(authResult.token);
          }
        } else {
          set({
            isAuthenticated: false,
            user: null,
            token: null,
            loading: false,
            error: null,
            initialized: true,
          });
        }
      } catch (error) {
        console.error("Auth check failed:", error);
        set({
          isAuthenticated: false,
          user: null,
          token: null,
          loading: false,
          error: "Authentication check failed",
          initialized: true,
        });
      }
    },

    logout() {
      localStorage.removeItem("token");
      localStorage.removeItem("user");
      delete authAPI.defaults.headers.common["Authorization"];

      disconnectWebSocket();

      set({
        isAuthenticated: false,
        user: null,
        loading: false,
        error: null,
        initialized: true,
      });
    },
  };
}

export const auth = createAuthStore();
