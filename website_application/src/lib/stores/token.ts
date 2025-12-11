let accessToken: string | null = null;

export function updateAuthToken(token: string | null): void {
  accessToken = token;
}

export function getAccessToken(): string | null {
  return accessToken;
}
