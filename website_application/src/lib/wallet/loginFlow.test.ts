import { describe, expect, it, vi } from "vitest";
import {
  connectAndLogin,
  subscribeToConnectors,
  walletErrorMessage,
  type WalletAddress,
} from "./loginFlow";

describe("wallet login flow", () => {
  it("signs with the exact account and connector returned by connect", async () => {
    const connector = { id: "late-injected" };
    const address: WalletAddress = "0x1111111111111111111111111111111111111111";
    const signChallenge = vi.fn(
      async (message: string, account: WalletAddress, selectedConnector: typeof connector) => ({
        message,
        signature: `${account}:${selectedConnector.id}`,
      })
    );
    const login = vi.fn(async () => ({ success: true }));

    const result = await connectAndLogin(connector, {
      connect: async () => ({ accounts: [address], chainId: 8453 }),
      issueChallenge: async (account, chainId) => `${account}@${chainId}`,
      signChallenge,
      login,
    });

    expect(result.success).toBe(true);
    expect(signChallenge).toHaveBeenCalledWith(`${address}@8453`, address, connector);
    expect(login).toHaveBeenCalledWith(address, `${address}@8453`, `${address}:${connector.id}`);
  });

  it("publishes connectors announced after the initial snapshot", () => {
    const snapshots: string[][] = [];
    let listener: ((connectors: readonly string[]) => void) | undefined;
    const unwatch = vi.fn();

    const stop = subscribeToConnectors<string>(
      () => [],
      (onChange) => {
        listener = onChange;
        return unwatch;
      },
      (connectors) => snapshots.push([...connectors])
    );

    listener?.(["injected-after-mount"]);
    stop();
    expect(snapshots).toEqual([[], ["injected-after-mount"]]);
    expect(unwatch).toHaveBeenCalledOnce();
  });

  it.each([
    ["User rejected the signature request", "Signature rejected. No login attempt was made."],
    ["challenge expired", "The wallet challenge expired. Request a new login signature."],
    ["challenge already used", "That wallet challenge was already used. Start the login again."],
    ["unsupported chain", "This wallet network is not supported for login."],
  ])("maps %s to an actionable error", (raw, expected) => {
    expect(walletErrorMessage(new Error(raw))).toBe(expected);
  });
});
