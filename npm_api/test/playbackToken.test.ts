import { createPublicKey, verify } from "node:crypto";

import { describe, expect, it } from "vitest";

import {
  playbackTokenSigningInput,
  signPlaybackToken,
  type PlaybackTokenOptions,
} from "../src/playbackToken.js";
import { loadFixture } from "./fixtures.js";

interface JWTFixture {
  key: { kid: string; privateKeyPem: string; publicKeyPem: string };
  cases: Array<{
    name: string;
    input: Omit<PlaybackTokenOptions, "privateKeyPem" | "kid">;
    signingInput: string;
  }>;
  rejected: Array<{ name: string; input: Omit<PlaybackTokenOptions, "privateKeyPem" | "kid"> }>;
  tokens: Record<string, string>;
}

const fixture = loadFixture<JWTFixture>("playback_jwt.json");

function verifiesUnderFixtureKey(token: string): boolean {
  const [header, payload, signature] = token.split(".");
  return verify(
    "sha256",
    Buffer.from(`${header}.${payload}`),
    { key: createPublicKey(fixture.key.publicKeyPem), dsaEncoding: "ieee-p1363" },
    Buffer.from(signature!, "base64url")
  );
}

describe("playback token signing (sdk_conformance/playback_jwt.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const token = await signPlaybackToken({
        ...tc.input,
        kid: fixture.key.kid,
        privateKeyPem: fixture.key.privateKeyPem,
      });
      const parts = token.split(".");
      expect(`${parts[0]}.${parts[1]}`).toBe(tc.signingInput);
      expect(Buffer.from(parts[2]!, "base64url")).toHaveLength(64);
      expect(verifiesUnderFixtureKey(token)).toBe(true);
    });
  }

  for (const tc of fixture.rejected) {
    it(`rejects: ${tc.name}`, () => {
      expect(() => playbackTokenSigningInput({ ...tc.input, kid: fixture.key.kid })).toThrow(
        TypeError
      );
    });
  }

  it("the TypeScript token in the fixture was signed by this SDK over the claims case", () => {
    const token = fixture.tokens.typescript;
    expect(token, "sdk_conformance/playback_jwt.json tokens.typescript").toBeTruthy();
    const [header, payload] = token!.split(".");
    expect(`${header}.${payload}`).toBe(fixture.cases[1]!.signingInput);
    expect(verifiesUnderFixtureKey(token!)).toBe(true);
  });
});
