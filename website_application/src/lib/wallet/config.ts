import { http, createConfig } from "wagmi";
import { mainnet, base, arbitrum } from "wagmi/chains";

export const wagmiConfig = createConfig({
  chains: [mainnet, base, arbitrum],
  // No explicit connectors - wagmi uses EIP-6963 to auto-discover
  // installed wallets with their proper names and icons
  multiInjectedProviderDiscovery: true,
  transports: {
    [mainnet.id]: http(),
    [base.id]: http(),
    [arbitrum.id]: http(),
  },
});

// Chain types that map to our backend
export const chainTypeMap: Record<number, string> = {
  [mainnet.id]: "ethereum",
  [base.id]: "base",
  [arbitrum.id]: "arbitrum",
};

export function getChainType(chainId: number): string {
  return chainTypeMap[chainId] || "ethereum";
}
