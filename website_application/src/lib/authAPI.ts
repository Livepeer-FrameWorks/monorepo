import axios, { type InternalAxiosRequestConfig, type AxiosResponse, type AxiosError } from "axios";

interface User {
  tenant_id?: string;
  [key: string]: unknown;
}

// API configuration for authentication endpoints
const AUTH_URL = import.meta.env.VITE_AUTH_URL || "http://localhost:18000/auth";
export { AUTH_URL };

export const authAPI = axios.create({
  baseURL: AUTH_URL,
  headers: {
    "Content-Type": "application/json",
  },
});

// Request interceptor to add auth token
authAPI.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    const token = localStorage.getItem("token");
    if (token) {
      config.headers.Authorization = `Bearer ${token}`;
    }

    // Don't add tenant_id for auth endpoints since user hasn't logged in yet
    const isAuthEndpoint =
      config.url &&
      (config.url.includes("/login") ||
        config.url.includes("/register") ||
        config.url.includes("/verify-email"));

    // Add tenant ID from user data if available (except for auth endpoints)
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
