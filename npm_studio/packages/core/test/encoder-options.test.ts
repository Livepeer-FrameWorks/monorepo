import { afterEach, describe, expect, it, vi } from "vitest";

import {
  encoderSettingRows,
  encoderSettingValues,
  encoderSummaryRows,
  isEncoderSettingOverridden,
  withEncoderSetting,
} from "../src/core/encoderOptions";
import { sendableVideoCodecs, videoCodecOrder, WhipClient } from "../src/core/WhipClient";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("encoder settings", () => {
  it("shows the profile's values until a setting is overridden", () => {
    expect(encoderSettingValues("broadcast", undefined)).toEqual({
      resolution: "1920x1080",
      framerate: 30,
      videoBitrate: 2_500_000,
      audioBitrate: 128_000,
      codec: "auto",
    });
  });

  it("overrides a setting and drops the override when the profile's value is chosen again", () => {
    let overrides = withEncoderSetting("broadcast", undefined, "videoBitrate", "6000000");
    overrides = withEncoderSetting("broadcast", overrides, "resolution", "1280x720");
    expect(overrides).toEqual({ video: { bitrate: 6_000_000, width: 1280, height: 720 } });
    expect(isEncoderSettingOverridden(overrides, "resolution")).toBe(true);

    overrides = withEncoderSetting("broadcast", overrides, "resolution", "1920x1080");
    overrides = withEncoderSetting("broadcast", overrides, "videoBitrate", 2_500_000);
    expect(overrides).toEqual({});
    expect(isEncoderSettingOverridden(overrides, "videoBitrate")).toBe(false);
  });

  it("draws the selects from the profile and marks the overridden ones", () => {
    const overrides = withEncoderSetting("conference", undefined, "framerate", 60);
    const rows = encoderSettingRows("conference", overrides, ["vp9", "h264"], "Auto");

    expect(rows.map((r) => [r.setting, r.value, r.overridden])).toEqual([
      ["codec", "auto", false],
      ["resolution", "1280x720", false],
      ["framerate", "60", true],
      ["videoBitrate", "1500000", false],
      ["audioBitrate", "128000", false],
    ]);
    expect(rows[0].options.map((o) => o.label)).toEqual(["Auto", "VP9", "H.264"]);
  });

  it("leaves out the codec select when the browser offers one codec, and keeps off-list values", () => {
    const rows = encoderSettingRows(
      "broadcast",
      { video: { bitrate: 3_000_000 } },
      ["h264"],
      "Auto"
    );

    expect(rows.map((r) => r.setting)).not.toContain("codec");
    const bitrate = rows.find((r) => r.setting === "videoBitrate")!;
    expect(bitrate.value).toBe("3000000");
    expect(bitrate.options[0]).toEqual({ value: "3000000", label: "3 Mbps" });
  });

  it("keeps a codec choice until Auto is chosen", () => {
    const overrides = withEncoderSetting("broadcast", undefined, "codec", "h264");
    expect(overrides).toEqual({ video: { codec: "h264" } });
    expect(encoderSettingValues("broadcast", overrides).codec).toBe("h264");
    expect(withEncoderSetting("broadcast", overrides, "codec", "auto")).toEqual({});
  });
});

describe("encoder summary", () => {
  it("shows the profile's config with nothing marked overridden", () => {
    expect(encoderSummaryRows("broadcast", undefined, "Auto")).toEqual([
      { labelKey: "videoCodec", value: "Auto", overridden: false },
      { labelKey: "resolution", value: "1920×1080", overridden: false },
      { labelKey: "framerate", value: "30 fps", overridden: false },
      { labelKey: "videoBitrate", value: "2.5 Mbps", overridden: false },
      { labelKey: "audioCodec", value: "opus", overridden: false },
      { labelKey: "audioBitrate", value: "128 kbps", overridden: false },
    ]);
  });

  it("shows each override and marks only the settings it changes", () => {
    const rows = encoderSummaryRows(
      "broadcast",
      { video: { codec: "vp9", width: 1280, height: 720 }, audio: { bitrate: 64_000 } },
      "Auto"
    );
    const byKey = Object.fromEntries(rows.map((row) => [row.labelKey, row]));
    expect(byKey.videoCodec).toEqual({ labelKey: "videoCodec", value: "VP9", overridden: true });
    expect(byKey.resolution).toEqual({
      labelKey: "resolution",
      value: "1280×720",
      overridden: true,
    });
    expect(byKey.framerate.overridden).toBe(false);
    expect(byKey.videoBitrate.overridden).toBe(false);
    expect(byKey.audioBitrate).toEqual({
      labelKey: "audioBitrate",
      value: "64 kbps",
      overridden: true,
    });
  });
});

describe("video codec preference", () => {
  it("offers AV1, VP9, then H.264 unless one is preferred", () => {
    expect(videoCodecOrder()).toEqual(["video/AV1", "video/VP9", "video/H264"]);
    expect(videoCodecOrder("h264")).toEqual(["video/H264", "video/AV1", "video/VP9"]);
  });

  it("puts the preferred codec first on the video transceiver", () => {
    vi.stubGlobal("RTCRtpSender", {
      getCapabilities: () => ({
        codecs: [
          { mimeType: "video/VP8" },
          { mimeType: "video/H264" },
          { mimeType: "video/VP9" },
          { mimeType: "video/AV1" },
        ],
      }),
    });
    const setCodecPreferences = vi.fn();
    const pc = {
      getTransceivers: () => [{ setCodecPreferences, sender: { track: { kind: "video" } } }],
    };
    const client = new WhipClient({
      whipUrl: "https://edge.example/whip",
      preferredVideoCodec: "h264",
    });
    (client as unknown as { preferCodecs(pc: unknown): void }).preferCodecs(pc);

    expect(
      setCodecPreferences.mock.calls[0][0].map((c: { mimeType: string }) => c.mimeType)
    ).toEqual(["video/H264", "video/AV1", "video/VP9"]);
  });

  it("lists only the codecs the browser can send", () => {
    vi.stubGlobal("RTCRtpSender", {
      getCapabilities: () => ({ codecs: [{ mimeType: "video/H264" }, { mimeType: "video/VP9" }] }),
    });
    expect(sendableVideoCodecs()).toEqual(["vp9", "h264"]);
  });

  it("takes the codec from the encoder overrides before the config", () => {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/whip",
      videoCodec: "vp9",
    });
    const preferred = () =>
      (
        controller as unknown as { preferredVideoCodec(): string | undefined }
      ).preferredVideoCodec();
    expect(preferred()).toBe("vp9");
    controller.setEncoderOverrides({ video: { codec: "h264" } });
    expect(preferred()).toBe("h264");
    controller.destroy();
  });
});
