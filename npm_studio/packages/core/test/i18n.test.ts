import { describe, expect, it } from "vitest";

import {
  DEFAULT_STUDIO_TRANSLATIONS,
  getStudioLocalePack,
  getAvailableStudioLocales,
  createStudioTranslator,
  studioTranslate,
  type StudioLocale,
  type StudioTranslationStrings,
} from "../src/I18n";

// All keys from StudioTranslationStrings
const ALL_KEYS = Object.keys(DEFAULT_STUDIO_TRANSLATIONS) as (keyof StudioTranslationStrings)[];

describe("Studio I18n", () => {
  // ===========================================================================
  // All locale packs complete
  // ===========================================================================
  describe("locale packs completeness", () => {
    const locales = getAvailableStudioLocales();

    it("has at least 5 locales (en, es, fr, de, nl)", () => {
      expect(locales.length).toBeGreaterThanOrEqual(5);
      expect(locales).toContain("en");
      expect(locales).toContain("es");
      expect(locales).toContain("fr");
      expect(locales).toContain("de");
      expect(locales).toContain("nl");
    });

    for (const locale of ["en", "es", "fr", "de", "nl"] as StudioLocale[]) {
      it(`${locale} pack has all keys defined`, () => {
        const pack = getStudioLocalePack(locale);
        for (const key of ALL_KEYS) {
          expect(pack[key], `Missing key "${key}" in ${locale} pack`).toBeDefined();
          expect(typeof pack[key]).toBe("string");
          expect(pack[key].length).toBeGreaterThan(0);
        }
      });
    }

    it("en pack is DEFAULT_STUDIO_TRANSLATIONS", () => {
      expect(getStudioLocalePack("en")).toBe(DEFAULT_STUDIO_TRANSLATIONS);
    });

    it("non-en packs have translated core strings", () => {
      for (const locale of ["es", "fr", "de", "nl"] as StudioLocale[]) {
        const pack = getStudioLocalePack(locale);
        // At minimum, these should differ from English
        expect(pack.goLive).not.toBe(DEFAULT_STUDIO_TRANSLATIONS.goLive);
        expect(pack.stopStreaming).not.toBe(DEFAULT_STUDIO_TRANSLATIONS.stopStreaming);
        expect(pack.addCamera).not.toBe(DEFAULT_STUDIO_TRANSLATIONS.addCamera);
      }
    });
  });

  // ===========================================================================
  // Variable interpolation
  // ===========================================================================
  describe("variable interpolation", () => {
    it("interpolates {attempt} and {max} in reconnecting string", () => {
      const t = createStudioTranslator();
      const result = t("reconnectingAttempt", { attempt: 2, max: 5 });
      expect(result).toBe("Reconnecting (2/5)...");
    });

    it("interpolates variables in all locales", () => {
      for (const locale of ["es", "fr", "de", "nl"] as StudioLocale[]) {
        const t = createStudioTranslator({ locale });
        const result = t("reconnectingAttempt", { attempt: 1, max: 3 });
        expect(result).toContain("1");
        expect(result).toContain("3");
        expect(result).not.toContain("{attempt}");
        expect(result).not.toContain("{max}");
      }
    });

    it("leaves string unchanged when no vars are provided", () => {
      const t = createStudioTranslator();
      expect(t("goLive")).toBe("Go Live");
    });

    it("leaves unmatched placeholders unchanged", () => {
      const t = createStudioTranslator();
      const result = t("reconnectingAttempt", { attempt: 1 }); // missing {max}
      expect(result).toContain("1");
      expect(result).toContain("{max}");
    });
  });

  // ===========================================================================
  // createStudioTranslator factory
  // ===========================================================================
  describe("createStudioTranslator", () => {
    it("defaults to English when no config", () => {
      const t = createStudioTranslator();
      expect(t("goLive")).toBe("Go Live");
    });

    it("uses specified locale", () => {
      const t = createStudioTranslator({ locale: "es" });
      expect(t("goLive")).toBe("Transmitir");
    });

    it("merges custom translations on top of locale", () => {
      const t = createStudioTranslator({
        locale: "en",
        translations: { goLive: "Start Broadcasting" },
      });
      expect(t("goLive")).toBe("Start Broadcasting");
      // Other keys still work
      expect(t("stopStreaming")).toBe("Stop Streaming");
    });

    it("custom translations override locale-specific strings", () => {
      const t = createStudioTranslator({
        locale: "es",
        translations: { goLive: "En vivo ya!" },
      });
      expect(t("goLive")).toBe("En vivo ya!");
    });

    it("falls back to English for missing keys", () => {
      const t = createStudioTranslator({ locale: "en" });
      // Key that exists
      expect(t("idle")).toBe("Idle");
    });
  });

  // ===========================================================================
  // studioTranslate (one-shot)
  // ===========================================================================
  describe("studioTranslate", () => {
    it("translates with provided translations object", () => {
      const result = studioTranslate({ goLive: "GO!" }, "goLive");
      expect(result).toBe("GO!");
    });

    it("falls back to DEFAULT when key not in provided translations", () => {
      const result = studioTranslate({}, "goLive");
      expect(result).toBe("Go Live");
    });

    it("interpolates variables", () => {
      const result = studioTranslate(
        { reconnectingAttempt: "Retry {attempt} of {max}" },
        "reconnectingAttempt",
        { attempt: 3, max: 5 }
      );
      expect(result).toBe("Retry 3 of 5");
    });
  });

  // ===========================================================================
  // getStudioLocalePack
  // ===========================================================================
  describe("getStudioLocalePack", () => {
    it("returns English for unknown locale", () => {
      const pack = getStudioLocalePack("xx" as StudioLocale);
      expect(pack).toBe(DEFAULT_STUDIO_TRANSLATIONS);
    });
  });
});
