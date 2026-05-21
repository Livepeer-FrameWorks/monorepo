import { describe, expect, it } from "vitest";
import { renderSkipperMarkdown } from "./skipperMarkdown";

describe("renderSkipperMarkdown", () => {
  it("renders GFM-style tables", () => {
    const html = renderSkipperMarkdown(`| Protocol | Latency |
| --- | --- |
| WebRTC | Sub-second |
| HLS | 6-30s |`);

    expect(html).toContain("<table");
    expect(html).toContain("<thead>");
    expect(html).toContain("<tbody>");
    expect(html).toContain("<th");
    expect(html).toContain("Protocol");
    expect(html).toContain("WebRTC");
    expect(html).not.toContain("| --- |");
  });

  it("renders common markdown blocks without adding raw line breaks inside lists", () => {
    const html = renderSkipperMarkdown(`# Heading

- First
- **Second**

> Useful note

1. One
2. Two`);

    expect(html).toContain("<h1");
    expect(html).toContain("<ul");
    expect(html).toContain("<ol");
    expect(html).toContain("<blockquote");
    expect(html).toContain("<strong>Second</strong>");
  });

  it("escapes raw html while keeping safe http links", () => {
    const html = renderSkipperMarkdown(
      `<script>alert("x")</script> [docs](https://docs.example.com) [bad](javascript:alert(1))`
    );

    expect(html).toContain("&lt;script&gt;");
    expect(html).toContain('href="https://docs.example.com"');
    expect(html).not.toContain("<script>");
    expect(html).not.toContain('href="javascript:');
  });
});
