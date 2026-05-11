import { writable, derived } from "svelte/store";
import {
  createTranslator,
  type FwLocale,
  type TranslationStrings,
} from "@livepeer-frameworks/player-core";

export const localeStore = writable<FwLocale>("en");
export const translationsStore = writable<Partial<TranslationStrings> | undefined>(undefined);
export const translatorStore = derived(
  [localeStore, translationsStore],
  ([$locale, $translations]) => createTranslator({ locale: $locale, translations: $translations })
);
