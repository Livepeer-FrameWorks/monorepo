const API_URL = (import.meta.env.PUBLIC_API_URL || import.meta.env.VITE_GATEWAY_URL || "").replace(
  /\/$/,
  ""
);

export interface AuthUser {
  id: string;
  email: string;
  display_name: string;
  tenant_id: string;
}

export interface AuthSnapshot {
  user: AuthUser | null;
  loading: boolean;
  initialized: boolean;
}

type AuthListener = (snapshot: AuthSnapshot) => void;

interface AuthCoordinator {
  snapshot: AuthSnapshot;
  listeners: Set<AuthListener>;
  inFlight: Promise<AuthUser | null> | null;
}

type AuthGlobal = typeof globalThis & {
  __frameworksDocsAuth?: AuthCoordinator;
};

function coordinator(): AuthCoordinator {
  const root = globalThis as AuthGlobal;
  root.__frameworksDocsAuth ??= {
    snapshot: { user: null, loading: true, initialized: false },
    listeners: new Set<AuthListener>(),
    inFlight: null,
  };
  return root.__frameworksDocsAuth;
}

function publish(snapshot: AuthSnapshot): void {
  const state = coordinator();
  const authChanged =
    state.snapshot.initialized !== snapshot.initialized ||
    state.snapshot.user?.id !== snapshot.user?.id;
  state.snapshot = snapshot;
  for (const listener of state.listeners) listener(snapshot);
  if (authChanged && typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent("docs-auth-change", { detail: { user: snapshot.user } }));
  }
}

export function subscribeAuth(listener: AuthListener): () => void {
  const state = coordinator();
  state.listeners.add(listener);
  listener(state.snapshot);
  return () => state.listeners.delete(listener);
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

export async function checkAuth(force = false): Promise<AuthUser | null> {
  const state = coordinator();
  if (state.inFlight) return state.inFlight;
  if (!force && state.snapshot.initialized) return state.snapshot.user;

  publish({ ...state.snapshot, loading: true });
  state.inFlight = (async () => {
    let user: AuthUser | null = null;
    try {
      const res = await authFetch("/me");
      if (res.ok) {
        user = (await res.json()) ?? null;
      } else {
        const refresh = await authFetch("/refresh", { method: "POST" });
        if (refresh.ok) user = (await refresh.json()).user ?? null;
      }
    } catch {
      user = null;
    }
    publish({ user, loading: false, initialized: true });
    return user;
  })();

  try {
    return await state.inFlight;
  } finally {
    state.inFlight = null;
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
    publish({ user: data.user ?? null, loading: false, initialized: true });
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
    publish({ user: data.user ?? null, loading: false, initialized: true });
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
    publish({ user: data.user ?? null, loading: false, initialized: true });
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
  publish({ user: null, loading: false, initialized: true });
}
