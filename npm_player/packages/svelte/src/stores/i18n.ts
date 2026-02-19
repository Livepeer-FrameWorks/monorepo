import { writable, derived } from "svelte/store";
import { createTranslator, type FwLocale } from "@livepeer-frameworks/player-core";

export const localeStore = writable<FwLocale>("en");
export const translatorStore = derived(localeStore, ($locale) =>
  createTranslator({ locale: $locale })
);
