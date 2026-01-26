import { authAPI } from "../../authAPI.js";
import { updateAuthToken, getAccessToken } from "$lib/stores/token.js";

// Houdini automatically clears cache on client recreation
function resetGraphQLClient(): void {
  updateAuthToken(null);
  localStorage.removeItem("user");
}

interface User {
  id: string;
  email: string;
  name?: string;
  tenant_id?: string;
  email_verified?: boolean;
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
  async refreshToken(): Promise<AuthResponse | null> {
    try {
      const response = await authAPI.post<AuthResponse>("/refresh");
      const { token, user } = response.data;
      updateAuthToken(token);
      localStorage.setItem("user", JSON.stringify(user));
      return { token, user };
    } catch {
      return null;
    }
  },

  // REST-based auth operations (login/register happen before GraphQL token)
  async login(email: string, password: string): Promise<AuthResponse> {
    try {
      const response = await authAPI.post<AuthResponse>("/login", {
        email,
        password,
      });
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

  async register(
    email: string,
    password: string,
    name: string,
  ): Promise<AuthResponse> {
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
    try {
      await authAPI.post("/logout");
    } catch {
      // Ignore
    }
    // Clear localStorage data
    localStorage.removeItem("user");

    // Reset GraphQL client
    resetGraphQLClient();

    // Let the caller handle navigation
  },

  // Check if user is authenticated - avoid unnecessary /me API calls
  async checkAuth(forceValidation: boolean = false): Promise<CheckAuthResult> {
    let token = getAccessToken();
    const storedUserData = localStorage.getItem("user");

    if (!token) {
      // Try refreshing token
      const refreshed = await this.refreshToken();
      if (refreshed) {
        token = refreshed.token;
      } else {
        return { isAuthenticated: false, user: null };
      }
    }

    // If we have cached user data and aren't forcing validation, use it
    if (!forceValidation && storedUserData) {
      try {
        const user: User = JSON.parse(storedUserData);
        return { isAuthenticated: true, user, token: token! };
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

      return { isAuthenticated: true, user, token: token! };
    } catch (error) {
      console.error("Token validation failed:", error);

      // Try refreshing one last time
      const refreshed = await this.refreshToken();
      if (refreshed) {
        return {
          isAuthenticated: true,
          user: refreshed.user,
          token: refreshed.token,
        };
      }

      // Token is invalid, clear auth data
      this.logout();
      return { isAuthenticated: false, user: null };
    }
  },

  async updateProfile(firstName: string, lastName: string): Promise<void> {
    await authAPI.patch("/me", { first_name: firstName, last_name: lastName });
    // Update local storage cache to reflect change
    const storedUserData = localStorage.getItem("user");
    if (storedUserData) {
      const user = JSON.parse(storedUserData);
      user.first_name = firstName;
      user.last_name = lastName;
      localStorage.setItem("user", JSON.stringify(user));
    }
  },

  async updateNewsletter(subscribe: boolean): Promise<void> {
    await authAPI.post("/me/newsletter", { subscribe });
  },
};
