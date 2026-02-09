const API_URL = import.meta.env.PUBLIC_API_URL || "";

export interface AuthUser {
  id: string;
  email: string;
  display_name: string;
  tenant_id: string;
}

export interface BotProtectionData {
  turnstileToken?: string;
  phone_number?: string;
  human_check?: string;
  behavior?: string;
}

async function authFetch(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${API_URL}/auth${path}`, {
    ...init,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
  });
}

export async function checkAuth(): Promise<AuthUser | null> {
  try {
    const res = await authFetch("/me");
    if (!res.ok) {
      // Try refresh
      const refresh = await authFetch("/refresh", { method: "POST" });
      if (!refresh.ok) return null;
      return (await refresh.json()).user ?? null;
    }
    return (await res.json()) ?? null;
  } catch {
    return null;
  }
}

export async function login(
  email: string,
  password: string,
  botProtection: BotProtectionData = {}
): Promise<{ user?: AuthUser; error?: string }> {
  try {
    const res = await authFetch("/login", {
      method: "POST",
      body: JSON.stringify({
        email,
        password,
        turnstile_token: botProtection.turnstileToken,
        phone_number: botProtection.phone_number,
        human_check: botProtection.human_check,
        behavior: botProtection.behavior,
      }),
    });
    const data = await res.json();
    if (!res.ok) return { error: data.error || "Login failed" };
    return { user: data.user };
  } catch {
    return { error: "Network error" };
  }
}

export async function register(
  email: string,
  password: string,
  displayName: string,
  botProtection: BotProtectionData = {}
): Promise<{ user?: AuthUser; error?: string }> {
  try {
    const res = await authFetch("/register", {
      method: "POST",
      body: JSON.stringify({
        email,
        password,
        display_name: displayName,
        turnstile_token: botProtection.turnstileToken,
        phone_number: botProtection.phone_number,
        human_check: botProtection.human_check,
        behavior: botProtection.behavior,
      }),
    });
    const data = await res.json();
    if (!res.ok) return { error: data.error || "Registration failed" };
    return { user: data.user };
  } catch {
    return { error: "Network error" };
  }
}

export async function walletLogin(
  address: string,
  chainType: string,
  message: string,
  signature: string
): Promise<{ user?: AuthUser; error?: string }> {
  try {
    const res = await authFetch("/wallet-login", {
      method: "POST",
      body: JSON.stringify({ address, chain_type: chainType, message, signature }),
    });
    const data = await res.json();
    if (!res.ok) return { error: data.error || "Wallet login failed" };
    return { user: data.user };
  } catch {
    return { error: "Network error" };
  }
}

export async function logout(): Promise<void> {
  try {
    await authFetch("/logout", { method: "POST" });
  } catch {
    // Best-effort
  }
}
