import { get } from "svelte/store";
import {
  fragment,
  AuthSessionFieldsStore,
  SecurityActivityFieldsStore,
  GetAuthSessionsStore,
  GetSecurityActivityStore,
  RevokeAuthSessionStore,
  RevokeOtherAuthSessionsStore,
  RenameAuthSessionStore,
  ChangePasswordStore,
  LinkEmailStore,
  LinkWalletStore,
  UnlinkWalletStore,
  type AuthSessionFields$data,
  type SecurityActivityFields$data,
} from "$houdini";
import { refreshAuthSession } from "$lib/auth/refresh";
import { authAPI } from "$lib/authAPI";

export type SecuritySession = AuthSessionFields$data;
export type SecurityActivity = SecurityActivityFields$data;
export interface LinkedWallet {
  id: string;
  address: string;
  createdAt: string | null;
  lastAuthAt?: string | null;
}
export interface SecurityIdentity {
  email: string;
  emailVerified: boolean;
  wallets: LinkedWallet[];
}

class SecurityActionError extends Error {
  constructor(
    message: string,
    readonly code?: string
  ) {
    super(message);
  }
}
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object")
    throw new Error("The security service returned no result. Try again.");
  return value as Record<string, unknown>;
}
function message(value: unknown): string {
  return typeof value === "string" && value ? value : "The security request failed. Try again.";
}
interface ResponseErrors {
  errors?: readonly { message: string; extensions?: unknown }[] | null;
}

// Only this explicit pre-execution refusal can replay a mutation after renewal.
// A timeout or service failure may already have committed and is never retried.
export async function securityAction<T extends ResponseErrors>(
  run: () => Promise<T>,
  select: (response: T) => unknown
): Promise<Record<string, unknown>> {
  async function attempt() {
    const response = await run();
    if (response.errors?.length) {
      const error = response.errors[0];
      const extensions =
        error.extensions && typeof error.extensions === "object"
          ? (error.extensions as Record<string, unknown>)
          : {};
      throw new SecurityActionError(
        error.message,
        typeof extensions.code === "string" ? extensions.code : undefined
      );
    }
    const value = record(select(response));
    if (["AuthError", "ValidationError", "NotFoundError"].includes(String(value.__typename))) {
      throw new SecurityActionError(
        message(value.message),
        typeof value.code === "string" ? value.code : undefined
      );
    }
    return value;
  }
  try {
    return await attempt();
  } catch (error) {
    if (!(error instanceof SecurityActionError) || error.code !== "SESSION_REFRESH_REQUIRED")
      throw error;
    const renewed = await refreshAuthSession();
    if (renewed !== "ok")
      throw new Error(
        renewed === "transient"
          ? "Session renewal is unavailable. Try again."
          : "Sign in again to manage your security."
      );
    return await attempt();
  }
}

export async function loadSecuritySessions(after?: string | null) {
  const response = await new GetAuthSessionsStore().fetch({
    variables: { first: 50, after },
    policy: "NetworkOnly",
  });
  if (response.errors?.length) throw new Error(response.errors[0].message);
  if (!response.data) throw new Error("Could not load signed-in sessions.");
  const page = response.data.authSessionsConnection;
  const fields = new AuthSessionFieldsStore();
  return {
    nodes: page.nodes.map((node) => get(fragment(node, fields))),
    pageInfo: page.pageInfo,
    totalCount: page.totalCount,
  };
}
export async function loadSecurityActivity(after?: string | null) {
  const response = await new GetSecurityActivityStore().fetch({
    variables: { first: 25, after },
    policy: "NetworkOnly",
  });
  if (response.errors?.length) throw new Error(response.errors[0].message);
  if (!response.data) throw new Error("Could not load security activity.");
  const page = response.data.securityActivityConnection;
  const fields = new SecurityActivityFieldsStore();
  return {
    nodes: page.nodes.map((node) => get(fragment(node, fields))),
    pageInfo: page.pageInfo,
    totalCount: page.totalCount,
  };
}
export async function loadSecurityIdentity(): Promise<SecurityIdentity> {
  const [profile, wallets] = await Promise.all([
    authAPI.get<{ email?: string | null; is_verified?: boolean; email_verified?: boolean }>("/me"),
    authAPI.get<{ wallets: LinkedWallet[] }>("/me/wallets"),
  ]);
  return {
    email: profile.data.email ?? "",
    emailVerified: profile.data.is_verified === true || profile.data.email_verified === true,
    wallets: wallets.data.wallets,
  };
}
export async function revokeSecuritySession(id: string): Promise<boolean> {
  const result = await securityAction(
    () => new RevokeAuthSessionStore().mutate({ id }),
    (response) => response.data?.revokeAuthSession
  );
  if (result.success !== true) throw new Error("The session was not signed out. Try again.");
  return result.current === true;
}
export async function revokeOtherSecuritySessions(): Promise<number> {
  const result = await securityAction(
    () => new RevokeOtherAuthSessionsStore().mutate(null),
    (response) => response.data?.revokeOtherAuthSessions
  );
  if (typeof result.revokedCount !== "number")
    throw new Error("The service did not confirm sign-out. Try again.");
  return result.revokedCount;
}
export async function renameSecuritySession(id: string, label: string): Promise<void> {
  const result = await securityAction(
    () => new RenameAuthSessionStore().mutate({ id, label }),
    (response) => response.data?.renameAuthSession
  );
  if (result.id !== id) throw new Error("The session was not renamed. Try again.");
}
export async function changeSecurityPassword(
  currentPassword: string,
  newPassword: string
): Promise<number> {
  const result = await securityAction(
    () => new ChangePasswordStore().mutate({ currentPassword, newPassword }),
    (response) => response.data?.changePassword
  );
  if (result.success !== true || typeof result.revokedCount !== "number")
    throw new Error("The password change was not confirmed. Try again.");
  return result.revokedCount;
}
export async function linkSecurityEmail(email: string, password: string): Promise<void> {
  const result = await securityAction(
    () => new LinkEmailStore().mutate({ input: { email, password } }),
    (response) => response.data?.linkEmail
  );
  if (result.success !== true) throw new Error(message(result.message));
}
export async function linkSecurityWallet(input: {
  address: string;
  message: string;
  signature: string;
}): Promise<void> {
  const result = await securityAction(
    () => new LinkWalletStore().mutate({ input }),
    (response) => response.data?.linkWallet
  );
  if (typeof result.id !== "string") throw new Error(message(result.message));
}
export async function unlinkSecurityWallet(walletId: string): Promise<void> {
  const result = await securityAction(
    () => new UnlinkWalletStore().mutate({ walletId }),
    (response) => response.data?.unlinkWallet
  );
  if (result.success !== true) throw new Error(message(result.message));
}

export { securityTimestamp } from "$lib/utils/security-time";
export const securityMethodLabels: Record<SecuritySession["authMethod"], string> = {
  PASSWORD: "Password",
  WALLET: "Wallet",
  BROWSER_HANDOFF: "Browser approval",
  DEVICE_CODE: "Device code",
  UNKNOWN: "Unknown",
};
export const securityActivityLabels: Record<SecurityActivity["kind"], string> = {
  SIGN_IN_SUCCEEDED: "Signed in",
  SIGN_IN_FAILED: "Sign-in failed",
  SESSION_REVOKED: "Session signed out",
  SESSIONS_REVOKED: "Sessions ended",
  PASSWORD_CHANGED: "Password changed",
  PASSWORD_RESET: "Password reset",
  WALLET_LINKED: "Wallet linked",
  WALLET_UNLINKED: "Wallet unlinked",
};
