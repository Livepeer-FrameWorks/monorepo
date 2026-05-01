import { describe, expect, it } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import IdleScreen from "../src/components/IdleScreen";

describe("IdleScreen", () => {
  it("shows raw error text instead of only generic stream status", () => {
    render(<IdleScreen status="ERROR" message="Stream error" error="HTTP 503" />);

    expect(screen.getByText("HTTP 503")).toBeTruthy();
    expect(screen.queryByText("Stream error")).toBeNull();
  });

  it("shows diagnostic details when supplied separately", () => {
    render(
      <IdleScreen
        status="ERROR"
        message="Connection failed"
        error="HTTP 503"
        details="GET /json_stream.js returned 503"
      />
    );

    expect(screen.getByText("HTTP 503")).toBeTruthy();
    expect(screen.getByText("GET /json_stream.js returned 503")).toBeTruthy();
  });
});
