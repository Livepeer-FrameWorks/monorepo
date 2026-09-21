/**
 * Local signing of viewer playback tokens for streams, VOD assets, and clips
 * with a JWT playback policy. The token is an ES256 JWS carrying the claims
 * the gateway's playback verifier checks: kid in the header (one of the
 * policy's allowed kids), exp (required), and optionally nbf, aud (matched
 * against requiredAudience), and custom claims (matched exactly against
 * requiredClaimsJson).
 */

export interface PlaybackTokenOptions {
  /** The PKCS#8 PEM private key createSigningKey returned. */
  privateKeyPem: string;
  /** The signing key's kid. */
  kid: string;
  /** Expiry as a Date or unix seconds. Set this or expiresIn. */
  expiresAt?: Date | number;
  /** Lifetime in seconds from issuedAt. */
  expiresIn?: number;
  /** Issue time as a Date or unix seconds; defaults to now. */
  issuedAt?: Date | number;
  /** Earliest use as a Date or unix seconds. */
  notBefore?: Date | number;
  /** Your viewer's identifier. */
  subject?: string;
  /** Audience; one value is sent as a string, several as an array. */
  audience?: string | ReadonlyArray<string>;
  /** Custom claims. They may not set exp, iat, nbf, aud, or sub. */
  claims?: Readonly<Record<string, unknown>>;
}

const registeredClaims = ["exp", "iat", "nbf", "aud", "sub"];

function unixSeconds(value: Date | number): number {
  return value instanceof Date ? Math.floor(value.getTime() / 1000) : Math.floor(value);
}

/** JSON with object keys sorted at every level, so every SDK signs the same bytes. */
export function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(canonicalJSON).join(",")}]`;
  }
  if (value !== null && typeof value === "object") {
    const obj = value as Record<string, unknown>;
    return `{${Object.keys(obj)
      .filter((k) => obj[k] !== undefined)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonicalJSON(obj[k])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

function base64url(bytes: Uint8Array): string {
  let binary = "";
  for (const b of bytes) {
    binary += String.fromCharCode(b);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function pemToDer(pem: string): Uint8Array {
  const m = /-----BEGIN PRIVATE KEY-----([\s\S]+?)-----END PRIVATE KEY-----/.exec(pem);
  if (!m?.[1]) {
    throw new TypeError(
      "privateKeyPem must be a PKCS#8 PEM (BEGIN PRIVATE KEY), as createSigningKey returns it"
    );
  }
  const binary = atob(m[1].replace(/\s+/g, ""));
  const der = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    der[i] = binary.charCodeAt(i);
  }
  return der;
}

/** The header and payload segments of a playback token, before signing. */
export function playbackTokenSigningInput(
  options: Omit<PlaybackTokenOptions, "privateKeyPem">
): string {
  if (!options.kid) {
    throw new TypeError("kid is required");
  }
  const issuedAt =
    options.issuedAt === undefined ? Math.floor(Date.now() / 1000) : unixSeconds(options.issuedAt);
  let exp: number;
  if (options.expiresAt !== undefined) {
    exp = unixSeconds(options.expiresAt);
  } else if (options.expiresIn !== undefined) {
    exp = issuedAt + Math.floor(options.expiresIn);
  } else {
    throw new TypeError(
      "expiresAt or expiresIn is required: the playback verifier rejects tokens without exp"
    );
  }
  const payload: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(options.claims ?? {})) {
    if (registeredClaims.includes(name)) {
      throw new TypeError(`claims may not set ${name}; use the matching option`);
    }
    payload[name] = value;
  }
  payload.exp = exp;
  payload.iat = issuedAt;
  if (options.notBefore !== undefined) {
    payload.nbf = unixSeconds(options.notBefore);
  }
  if (options.subject !== undefined) {
    payload.sub = options.subject;
  }
  if (options.audience !== undefined) {
    const aud = typeof options.audience === "string" ? [options.audience] : [...options.audience];
    payload.aud = aud.length === 1 ? aud[0] : aud;
  }
  const encoder = new TextEncoder();
  const header = { alg: "ES256", kid: options.kid, typ: "JWT" };
  return `${base64url(encoder.encode(canonicalJSON(header)))}.${base64url(encoder.encode(canonicalJSON(payload)))}`;
}

/** Signs a viewer playback token with a tenant signing key (ES256). */
export async function signPlaybackToken(options: PlaybackTokenOptions): Promise<string> {
  const signingInput = playbackTokenSigningInput(options);
  const key = await crypto.subtle.importKey(
    "pkcs8",
    pemToDer(options.privateKeyPem) as BufferSource,
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["sign"]
  );
  // WebCrypto returns the raw r||s signature JWS uses.
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    key,
    new TextEncoder().encode(signingInput) as BufferSource
  );
  return `${signingInput}.${base64url(new Uint8Array(signature))}`;
}
