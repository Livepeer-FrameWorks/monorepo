import { http, createConfig } from "wagmi";
import { mainnet, base, arbitrum } from "wagmi/chains";

export const wagmiConfig = createConfig({
  chains: [mainnet, base, arbitrum],
  multiInjectedProviderDiscovery: true,
  transports: {
    [mainnet.id]: http(),
    [base.id]: http(),
    [arbitrum.id]: http(),
  },
});

export const chainTypeMap: Record<number, string> = {
  [mainnet.id]: "ethereum",
  [base.id]: "base",
  [arbitrum.id]: "arbitrum",
};

export function getChainType(chainId: number): string {
  return chainTypeMap[chainId] || "ethereum";
}
