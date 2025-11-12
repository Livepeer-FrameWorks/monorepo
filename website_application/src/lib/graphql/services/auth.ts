import { authAPI } from "../../authAPI.js";
import { resetGraphQLClient, updateAuthToken } from "../client.js";

interface User {
  id: string;
  email: string;
  name?: string;
  tenant_id?: string;
  [key: string]: unknown;
}

interface AuthResponse {
  token: string;
  user: User;
}

interface CheckAuthResult {
  isAuthenticated: boolean;
  user: User | null;
  token?: string;
}

export const authService = {
  // REST-based auth operations (login/register happen before GraphQL token)
  async login(email: string, password: string): Promise<AuthResponse> {
    try {
      const response = await authAPI.post<AuthResponse>("/login", { email, password });
      const { token, user } = response.data;

      // Update GraphQL client with new token
      updateAuthToken(token);

      // Store user data
      localStorage.setItem("user", JSON.stringify(user));

      return { token, user };
    } catch (error) {
      console.error("Login failed:", error);
      throw error;
    }
  },

  async register(email: string, password: string, name: string): Promise<AuthResponse> {
    try {
      const response = await authAPI.post<AuthResponse>("/register", {
        email,
        password,
        name,
      });
      const { token, user } = response.data;

      // Update GraphQL client with new token
      updateAuthToken(token);

      // Store user data
      localStorage.setItem("user", JSON.stringify(user));

      return { token, user };
    } catch (error) {
      console.error("Registration failed:", error);
      throw error;
    }
  },

  async logout(): Promise<void> {
    // Clear localStorage data
    localStorage.removeItem("token");
    localStorage.removeItem("user");

    // Reset GraphQL client
    resetGraphQLClient();

    // Let the caller handle navigation
  },

  // Check if user is authenticated - avoid unnecessary /me API calls
  async checkAuth(forceValidation: boolean = false): Promise<CheckAuthResult> {
    const token = localStorage.getItem("token");
    const storedUserData = localStorage.getItem("user");

    if (!token) {
      return { isAuthenticated: false, user: null };
    }

    // If we have cached user data and aren't forcing validation, use it
    if (!forceValidation && storedUserData) {
      try {
        const user: User = JSON.parse(storedUserData);
        return { isAuthenticated: true, user, token };
      } catch (error) {
        console.error("Failed to parse stored user data:", error);
        // Fall through to validation if cached data is corrupted
      }
    }

    try {
      // Only validate token with /me endpoint when necessary
      const response = await authAPI.get<User>("/me", {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      });

      const user = response.data;

      // Update stored user data
      localStorage.setItem("user", JSON.stringify(user));

      return { isAuthenticated: true, user, token };
    } catch (error) {
      console.error("Token validation failed:", error);
      // Token is invalid, clear auth data
      this.logout();
      return { isAuthenticated: false, user: null };
    }
  },
};
