/**
 * UrlUtils - URL manipulation utilities
 *
 * Based on MistMetaPlayer's urlappend functionality.
 * Provides helpers for appending query parameters to URLs.
 */

/**
 * Append query parameters to a URL
 * Handles URLs that already have query parameters
 *
 * @param url - Base URL
 * @param params - Parameters to append (string or object)
 * @returns URL with appended parameters
 *
 * @example
 * ```ts
 * appendUrlParams('https://example.com/video.m3u8', 'token=abc&session=123')
 * // => 'https://example.com/video.m3u8?token=abc&session=123'
 *
 * appendUrlParams('https://example.com/video.m3u8?existing=param', 'token=abc')
 * // => 'https://example.com/video.m3u8?existing=param&token=abc'
 *
 * appendUrlParams('https://example.com/video.m3u8', { token: 'abc', session: '123' })
 * // => 'https://example.com/video.m3u8?token=abc&session=123'
 * ```
 */
export function appendUrlParams(
  url: string,
  params: string | Record<string, string | number | boolean | undefined | null>
): string {
  if (!params) {
    return url;
  }

  // Convert object to query string
  let queryString: string;
  if (typeof params === 'object') {
    const entries = Object.entries(params)
      .filter(([, value]) => value !== undefined && value !== null)
      .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);

    if (entries.length === 0) {
      return url;
    }
    queryString = entries.join('&');
  } else {
    queryString = params;
    // Strip leading ? or & if present
    if (queryString.startsWith('?') || queryString.startsWith('&')) {
      queryString = queryString.slice(1);
    }
  }

  if (!queryString) {
    return url;
  }

  // Determine separator (? or &)
  const separator = url.includes('?') ? '&' : '?';
  return `${url}${separator}${queryString}`;
}

/**
 * Parse query parameters from a URL
 *
 * @param url - URL to parse
 * @returns Object with query parameters
 */
export function parseUrlParams(url: string): Record<string, string> {
  const params: Record<string, string> = {};

  try {
    const urlObj = new URL(url);
    urlObj.searchParams.forEach((value, key) => {
      params[key] = value;
    });
  } catch {
    // If URL parsing fails, try manual parsing
    const queryIndex = url.indexOf('?');
    if (queryIndex === -1) {
      return params;
    }

    const queryString = url.slice(queryIndex + 1);
    const pairs = queryString.split('&');
    for (const pair of pairs) {
      const [key, value] = pair.split('=');
      if (key) {
        params[decodeURIComponent(key)] = value ? decodeURIComponent(value) : '';
      }
    }
  }

  return params;
}

/**
 * Remove query parameters from a URL
 *
 * @param url - URL to strip
 * @returns URL without query parameters
 */
export function stripUrlParams(url: string): string {
  const queryIndex = url.indexOf('?');
  return queryIndex === -1 ? url : url.slice(0, queryIndex);
}

/**
 * Build a URL with query parameters
 *
 * @param baseUrl - Base URL
 * @param params - Query parameters
 * @returns Complete URL
 */
export function buildUrl(baseUrl: string, params: Record<string, string | number | boolean | undefined | null>): string {
  return appendUrlParams(stripUrlParams(baseUrl), params);
}

/**
 * Check if URL uses secure protocol (https/wss)
 */
export function isSecureUrl(url: string): boolean {
  return url.startsWith('https://') || url.startsWith('wss://');
}

/**
 * Convert HTTP URL to WebSocket URL
 * http:// -> ws://
 * https:// -> wss://
 */
export function httpToWs(url: string): string {
  return url.replace(/^http/, 'ws');
}

/**
 * Convert WebSocket URL to HTTP URL
 * ws:// -> http://
 * wss:// -> https://
 */
export function wsToHttp(url: string): string {
  return url.replace(/^ws/, 'http');
}

/**
 * Ensure URL uses the same protocol as the current page
 * Useful for avoiding mixed content issues
 */
export function matchPageProtocol(url: string): string {
  if (typeof window === 'undefined') {
    return url;
  }

  const pageIsSecure = window.location.protocol === 'https:';
  const urlIsSecure = isSecureUrl(url);

  if (pageIsSecure && !urlIsSecure) {
    // Upgrade to secure
    return url.replace(/^http:/, 'https:').replace(/^ws:/, 'wss:');
  }

  if (!pageIsSecure && urlIsSecure) {
    // Downgrade to insecure (not recommended, but avoids issues)
    return url.replace(/^https:/, 'http:').replace(/^wss:/, 'ws:');
  }

  return url;
}

export default {
  appendUrlParams,
  parseUrlParams,
  stripUrlParams,
  buildUrl,
  isSecureUrl,
  httpToWs,
  wsToHttp,
  matchPageProtocol,
};
