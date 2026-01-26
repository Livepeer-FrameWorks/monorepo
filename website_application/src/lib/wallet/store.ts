/**
 * Wallet store for Svelte 5 - manages wallet connection state
 */
import { getAccount, watchAccount, disconnect, signMessage } from 'wagmi/actions';
import { wagmiConfig, getChainType } from './config';

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
	if (typeof window === 'undefined') return;

	initializeFromAccount();

	unwatchAccount = watchAccount(wagmiConfig, {
		onChange(account) {
			address = account.address;
			chainId = account.chainId;
			isConnected = account.isConnected;
			isConnecting = account.isConnecting;
		}
	});
}

export function cleanupWalletWatcher() {
	if (unwatchAccount) {
		unwatchAccount();
		unwatchAccount = null;
	}
}

// Generate a message for wallet signing (matches backend format)
export function generateAuthMessage(): string {
	const timestamp = new Date().toISOString();
	const nonce = crypto.randomUUID();
	return `FrameWorks Login\nTimestamp: ${timestamp}\nNonce: ${nonce}`;
}

// Sign a message with the connected wallet
export async function signAuthMessage(): Promise<{ message: string; signature: string } | null> {
	if (!isConnected || !address) {
		error = 'No wallet connected';
		return null;
	}

	try {
		error = null;
		const message = generateAuthMessage();
		const signature = await signMessage(wagmiConfig, { message });
		return { message, signature };
	} catch (err) {
		error = err instanceof Error ? err.message : 'Failed to sign message';
		return null;
	}
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
		error = err instanceof Error ? err.message : 'Failed to disconnect';
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
			return chainId ? getChainType(chainId) : 'ethereum';
		},
		get isConnected() {
			return isConnected;
		},
		get isConnecting() {
			return isConnecting;
		},
		get error() {
			return error;
		}
	};
}

export { address, chainId, isConnected, isConnecting, error };
