import { http, createConfig } from 'wagmi';
import { mainnet, base, arbitrum } from 'wagmi/chains';
import { injected, walletConnect } from 'wagmi/connectors';

// WalletConnect project ID - get from https://cloud.walletconnect.com
const WALLET_CONNECT_PROJECT_ID = import.meta.env.PUBLIC_WALLET_CONNECT_PROJECT_ID || '';

export const wagmiConfig = createConfig({
	chains: [mainnet, base, arbitrum],
	connectors: [
		injected(),
		...(WALLET_CONNECT_PROJECT_ID
			? [
					walletConnect({
						projectId: WALLET_CONNECT_PROJECT_ID
					})
				]
			: [])
	],
	transports: {
		[mainnet.id]: http(),
		[base.id]: http(),
		[arbitrum.id]: http()
	}
});

// Chain types that map to our backend
export const chainTypeMap: Record<number, string> = {
	[mainnet.id]: 'ethereum',
	[base.id]: 'base',
	[arbitrum.id]: 'arbitrum'
};

export function getChainType(chainId: number): string {
	return chainTypeMap[chainId] || 'ethereum';
}
