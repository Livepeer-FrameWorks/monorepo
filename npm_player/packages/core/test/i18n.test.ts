import { describe, it, expect } from "vitest";
import {
  DEFAULT_TRANSLATIONS,
  createTranslator,
  translate,
  getLocalePack,
  getLocaleDisplayName,
  getAvailableLocales,
  type TranslationStrings,
} from "../src/core/I18n";

describe("createTranslator — resolution precedence", () => {
  // The documented order (I18n.ts:697-702) is the contract under test:
  // overrides > locale pack > DEFAULT_TRANSLATIONS > fallback arg > raw key.
  it("falls back to DEFAULT_TRANSLATIONS when no config is given", () => {
    const t = createTranslator();
    expect(t("play")).toBe(DEFAULT_TRANSLATIONS.play);
    expect(t("pause")).toBe(DEFAULT_TRANSLATIONS.pause);
  });

  it("uses the built-in locale pack over the English default", () => {
    const t = createTranslator({ locale: "es" });
    expect(t("play")).toBe("Reproducir");
    // A key the Spanish pack does not override still resolves via DEFAULT_TRANSLATIONS.
    expect(t("play")).not.toBe(DEFAULT_TRANSLATIONS.play);
  });

  it("lets user overrides win over both the locale pack and the default", () => {
    const t = createTranslator({
      locale: "es",
      translations: { play: "GO" },
    });
    expect(t("play")).toBe("GO");
  });

  it("an empty-string override still wins (presence is checked with `key in`, not truthiness)", () => {
    // Regression guard: the resolver uses `key in overrides`, so a deliberately
    // blank label must NOT silently fall through to the locale/default value.
    const t = createTranslator({ translations: { play: "" } });
    expect(t("play")).toBe("");
  });

  it("returns the fallback argument for an unknown key, then the raw key", () => {
    const t = createTranslator();
    const unknown = "totallyMadeUpKey" as keyof TranslationStrings;
    expect(t(unknown, "FB")).toBe("FB");
    expect(t(unknown)).toBe(unknown);
  });

  it("ignores an unset locale (undefined locale → default pack)", () => {
    const t = createTranslator({ locale: undefined });
    expect(t("mute")).toBe(DEFAULT_TRANSLATIONS.mute);
  });
});

describe("translate — one-shot", () => {
  it("matches the closure form for the same key/config", () => {
    expect(translate("play", { locale: "es" })).toBe(createTranslator({ locale: "es" })("play"));
  });

  it("honors the fallback argument", () => {
    const unknown = "nope" as keyof TranslationStrings;
    expect(translate(unknown, undefined, "FB")).toBe("FB");
  });
});

describe("locale helpers", () => {
  it("getAvailableLocales lists every built-in pack", () => {
    expect(getAvailableLocales().sort()).toEqual(["de", "en", "es", "fr", "nl"]);
  });

  it("getLocaleDisplayName returns the native name and echoes unknown locales", () => {
    expect(getLocaleDisplayName("en")).toBe("English");
    expect(getLocaleDisplayName("nl")).toBe("Nederlands");
    // Defensive fallthrough: an off-list locale echoes its own code.
    expect(getLocaleDisplayName("xx" as never)).toBe("xx");
  });

  it("getLocalePack returns a populated pack and DEFAULT_TRANSLATIONS for unknown locales", () => {
    expect(getLocalePack("es").play).toBe("Reproducir");
    expect(getLocalePack("en")).toBe(DEFAULT_TRANSLATIONS);
    expect(getLocalePack("xx" as never)).toBe(DEFAULT_TRANSLATIONS);
  });
});
