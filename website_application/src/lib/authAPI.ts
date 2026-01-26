import axios, {
  type InternalAxiosRequestConfig,
  type AxiosResponse,
  type AxiosError,
} from "axios";

interface User {
  tenant_id?: string;
  [key: string]: unknown;
}

// API configuration for authentication endpoints
const AUTH_URL = import.meta.env.VITE_AUTH_URL ?? "";
export { AUTH_URL };

export const authAPI = axios.create({
  baseURL: AUTH_URL,
  headers: {
    "Content-Type": "application/json",
  },
  withCredentials: true, // Send httpOnly cookies with every request
});

// Request interceptor - cookies handle auth automatically
// We only need to add tenant_id header for non-auth endpoints
authAPI.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    // Don't add tenant_id for auth endpoints since user hasn't logged in yet
    const isAuthEndpoint =
      config.url &&
      (config.url.includes("/login") ||
        config.url.includes("/register") ||
        config.url.includes("/verify-email") ||
        config.url.includes("/resend-verification") ||
        config.url.includes("/refresh") ||
        config.url.includes("/forgot-password") ||
        config.url.includes("/reset-password"));

    // Add tenant ID from user data if available (for profile endpoints)
    if (!isAuthEndpoint) {
      const userData = localStorage.getItem("user");
      if (userData) {
        try {
          const user: User = JSON.parse(userData);
          if (user.tenant_id) {
            config.headers["X-Tenant-ID"] = user.tenant_id;
          }
        } catch (e) {
          console.warn("Failed to parse user data from localStorage:", e);
        }
      }
    }

    return config;
  },
  (error: AxiosError) => {
    return Promise.reject(error);
  },
);

// Response interceptor to handle errors
authAPI.interceptors.response.use(
  (response: AxiosResponse) => response,
  (error: AxiosError) => {
    // Just pass through errors - let the UI components handle them
    return Promise.reject(error);
  },
);
