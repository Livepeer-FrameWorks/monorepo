import { describe, it, expect } from "vitest";
import {
  appendUrlParams,
  parseUrlParams,
  stripUrlParams,
  buildUrl,
  isSecureUrl,
  httpToWs,
  wsToHttp,
} from "../src/core/UrlUtils";

describe("appendUrlParams", () => {
  it("returns original URL for empty params", () => {
    expect(appendUrlParams("https://example.com/video.m3u8", "")).toBe(
      "https://example.com/video.m3u8"
    );
  });

  it("appends string params to URL without query", () => {
    expect(appendUrlParams("https://example.com/video.m3u8", "token=abc")).toBe(
      "https://example.com/video.m3u8?token=abc"
    );
  });

  it("appends string params to URL with existing query", () => {
    expect(appendUrlParams("https://example.com/video.m3u8?existing=param", "token=abc")).toBe(
      "https://example.com/video.m3u8?existing=param&token=abc"
    );
  });

  it("strips leading ? from params", () => {
    expect(appendUrlParams("https://example.com/video.m3u8", "?token=abc")).toBe(
      "https://example.com/video.m3u8?token=abc"
    );
  });

  it("strips leading & from params", () => {
    expect(appendUrlParams("https://example.com/video.m3u8", "&token=abc")).toBe(
      "https://example.com/video.m3u8?token=abc"
    );
  });

  it("handles object params", () => {
    expect(
      appendUrlParams("https://example.com/video.m3u8", { token: "abc", session: "123" })
    ).toBe("https://example.com/video.m3u8?token=abc&session=123");
  });

  it("filters null and undefined values from object params", () => {
    expect(
      appendUrlParams("https://example.com/video.m3u8", {
        token: "abc",
        empty: null,
        missing: undefined,
      })
    ).toBe("https://example.com/video.m3u8?token=abc");
  });

  it("encodes special characters in object params", () => {
    const result = appendUrlParams("https://example.com/video.m3u8", { q: "hello world" });
    expect(result).toContain("q=hello%20world");
  });

  it("handles boolean and number values", () => {
    expect(appendUrlParams("https://example.com", { enabled: true, count: 42 })).toBe(
      "https://example.com?enabled=true&count=42"
    );
  });

  it("returns original URL for empty object params", () => {
    expect(appendUrlParams("https://example.com", {})).toBe("https://example.com");
  });
});

describe("parseUrlParams", () => {
  it("parses query parameters from URL", () => {
    expect(parseUrlParams("https://example.com?foo=bar&baz=qux")).toEqual({
      foo: "bar",
      baz: "qux",
    });
  });

  it("returns empty object for URL without params", () => {
    expect(parseUrlParams("https://example.com/path")).toEqual({});
  });

  it("handles URL-encoded values", () => {
    expect(parseUrlParams("https://example.com?q=hello%20world")).toEqual({
      q: "hello world",
    });
  });

  it("handles empty values", () => {
    expect(parseUrlParams("https://example.com?empty=")).toEqual({ empty: "" });
  });

  it("handles relative URLs via fallback", () => {
    expect(parseUrlParams("/path?foo=bar")).toEqual({ foo: "bar" });
  });
});

describe("stripUrlParams", () => {
  it("removes query parameters", () => {
    expect(stripUrlParams("https://example.com/video.m3u8?token=abc")).toBe(
      "https://example.com/video.m3u8"
    );
  });

  it("returns original URL if no params", () => {
    expect(stripUrlParams("https://example.com/video.m3u8")).toBe("https://example.com/video.m3u8");
  });
});

describe("buildUrl", () => {
  it("builds URL from base and params", () => {
    expect(buildUrl("https://example.com", { token: "abc" })).toBe("https://example.com?token=abc");
  });

  it("replaces existing params with new ones", () => {
    expect(buildUrl("https://example.com?old=param", { new: "value" })).toBe(
      "https://example.com?new=value"
    );
  });
});

describe("isSecureUrl", () => {
  it("returns true for https URLs", () => {
    expect(isSecureUrl("https://example.com")).toBe(true);
  });

  it("returns true for wss URLs", () => {
    expect(isSecureUrl("wss://example.com")).toBe(true);
  });

  it("returns false for http URLs", () => {
    expect(isSecureUrl("http://example.com")).toBe(false);
  });

  it("returns false for ws URLs", () => {
    expect(isSecureUrl("ws://example.com")).toBe(false);
  });
});

describe("httpToWs", () => {
  it("converts http to ws", () => {
    expect(httpToWs("http://example.com")).toBe("ws://example.com");
  });

  it("converts https to wss", () => {
    expect(httpToWs("https://example.com")).toBe("wss://example.com");
  });

  it("preserves path and query", () => {
    expect(httpToWs("https://example.com/path?query=value")).toBe(
      "wss://example.com/path?query=value"
    );
  });
});

describe("wsToHttp", () => {
  it("converts ws to http", () => {
    expect(wsToHttp("ws://example.com")).toBe("http://example.com");
  });

  it("converts wss to https", () => {
    expect(wsToHttp("wss://example.com")).toBe("https://example.com");
  });

  it("preserves path and query", () => {
    expect(wsToHttp("wss://example.com/path?query=value")).toBe(
      "https://example.com/path?query=value"
    );
  });
});
