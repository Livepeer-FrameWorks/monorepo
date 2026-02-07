import { getAccount, watchAccount, disconnect, signMessage } from "wagmi/actions";
import { wagmiConfig, getChainType } from "./wallet-config";

let address: `0x${string}` | undefined;
let chainId: number | undefined;
let isConnected = false;
let unwatchAccount: (() => void) | null = null;

function initializeFromAccount() {
  const account = getAccount(wagmiConfig);
  address = account.address;
  chainId = account.chainId;
  isConnected = account.isConnected;
}

export function setupWalletWatcher() {
  if (typeof window === "undefined") return;

  initializeFromAccount();

  unwatchAccount = watchAccount(wagmiConfig, {
    onChange(account) {
      address = account.address;
      chainId = account.chainId;
      isConnected = account.isConnected;
    },
  });
}

export function cleanupWalletWatcher() {
  if (unwatchAccount) {
    unwatchAccount();
    unwatchAccount = null;
  }
}

export function generateAuthMessage(): string {
  const timestamp = new Date().toISOString();
  const nonce = crypto.randomUUID();
  return `FrameWorks Login\nTimestamp: ${timestamp}\nNonce: ${nonce}`;
}

export async function signAuthMessage(): Promise<{
  message: string;
  signature: string;
} | null> {
  if (!isConnected || !address) return null;

  try {
    const message = generateAuthMessage();
    const signature = await signMessage(wagmiConfig, { message });
    return { message, signature };
  } catch {
    return null;
  }
}

export async function disconnectWallet() {
  try {
    await disconnect(wagmiConfig);
    address = undefined;
    chainId = undefined;
    isConnected = false;
  } catch {
    // Best-effort
  }
}

export function getWalletState() {
  return {
    address,
    chainId,
    chainType: chainId ? getChainType(chainId) : "ethereum",
    isConnected,
  };
}
