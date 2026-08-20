export type WalletAddress = `0x${string}`;

export type WalletLoginResult = {
  success: boolean;
  error?: string;
};

type LoginFlowDependencies<Connector> = {
  connect: (connector: Connector) => Promise<{
    accounts: readonly WalletAddress[];
    chainId: number;
  }>;
  issueChallenge: (address: WalletAddress, chainId: number) => Promise<string>;
  signChallenge: (
    message: string,
    address: WalletAddress,
    connector: Connector
  ) => Promise<{ message: string; signature: string }>;
  login: (address: WalletAddress, message: string, signature: string) => Promise<WalletLoginResult>;
};

export async function connectAndLogin<Connector>(
  connector: Connector,
  dependencies: LoginFlowDependencies<Connector>
): Promise<WalletLoginResult> {
  const connection = await dependencies.connect(connector);
  const address = connection.accounts[0];
  if (!address) {
    throw new Error("No account connected");
  }
  const message = await dependencies.issueChallenge(address, connection.chainId);
  const signed = await dependencies.signChallenge(message, address, connector);
  return dependencies.login(address, signed.message, signed.signature);
}

export function subscribeToConnectors<Connector>(
  getCurrent: () => readonly Connector[],
  watch: (onChange: (connectors: readonly Connector[]) => void) => () => void,
  onChange: (connectors: readonly Connector[]) => void
): () => void {
  onChange(getCurrent());
  return watch(onChange);
}

export function walletErrorMessage(error: unknown): string {
  const raw = error instanceof Error ? error.message : "Wallet connection failed";
  const normalized = raw.toLowerCase();
  if (normalized.includes("user rejected") || normalized.includes("user denied")) {
    return normalized.includes("signature")
      ? "Signature rejected. No login attempt was made."
      : "Wallet connection rejected. Try again when you are ready.";
  }
  if (normalized.includes("connector") && normalized.includes("not found")) {
    return "Wallet extension is no longer available. Reload it and try again.";
  }
  if (normalized.includes("expired")) {
    return "The wallet challenge expired. Request a new login signature.";
  }
  if (normalized.includes("already used") || normalized.includes("replay")) {
    return "That wallet challenge was already used. Start the login again.";
  }
  if (normalized.includes("unsupported") && normalized.includes("chain")) {
    return "This wallet network is not supported for login.";
  }
  return raw;
}
