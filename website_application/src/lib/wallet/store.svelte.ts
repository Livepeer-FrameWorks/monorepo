/**
 * Wallet store for Svelte 5 - manages wallet connection state
 */
import { getAccount, watchAccount, disconnect, signMessage } from "wagmi/actions";
import type { Connector } from "wagmi";
import { wagmiConfig, getChainType } from "./config";

// Wallet connection state using Svelte 5 runes
let address = $state<`0x${string}` | undefined>(undefined);
let chainId = $state<number | undefined>(undefined);
let isConnected = $state(false);
let isConnecting = $state(false);
let error = $state<string | null>(null);

// Initialize from current account state
function initializeFromAccount() {
  const account = getAccount(wagmiConfig);
  address = account.address;
  chainId = account.chainId;
  isConnected = account.isConnected;
}

// Watch for account changes
let unwatchAccount: (() => void) | null = null;

export function setupWalletWatcher() {
  if (typeof window === "undefined") return;

  initializeFromAccount();

  unwatchAccount = watchAccount(wagmiConfig, {
    onChange(account) {
      address = account.address;
      chainId = account.chainId;
      isConnected = account.isConnected;
      isConnecting = account.isConnecting;
    },
  });
}

export function cleanupWalletWatcher() {
  if (unwatchAccount) {
    unwatchAccount();
    unwatchAccount = null;
  }
}

// Sign the server-issued, single-use login challenge.
export async function signAuthMessage(message: string): Promise<{
  message: string;
  signature: string;
} | null> {
  if (!isConnected || !address) {
    error = "No wallet connected";
    return null;
  }

  try {
    error = null;
    const signature = await signMessage(wagmiConfig, { message });
    return { message, signature };
  } catch (err) {
    error = err instanceof Error ? err.message : "Failed to sign message";
    return null;
  }
}

// Sign against the exact account/connector returned by connect(). This avoids
// racing the reactive account watcher during first-time wallet login.
export async function signAuthMessageForConnection(
  message: string,
  account: `0x${string}`,
  connector: Connector
): Promise<{ message: string; signature: string }> {
  error = null;
  const signature = await signMessage(wagmiConfig, { message, account, connector });
  return { message, signature };
}

// Disconnect wallet
export async function disconnectWallet() {
  try {
    await disconnect(wagmiConfig);
    address = undefined;
    chainId = undefined;
    isConnected = false;
    error = null;
  } catch (err) {
    error = err instanceof Error ? err.message : "Failed to disconnect";
  }
}

// Export reactive state getters
export function getWalletState() {
  return {
    get address() {
      return address;
    },
    get chainId() {
      return chainId;
    },
    get chainType() {
      return chainId ? getChainType(chainId) : "ethereum";
    },
    get isConnected() {
      return isConnected;
    },
    get isConnecting() {
      return isConnecting;
    },
    get error() {
      return error;
    },
  };
}
