import { writable, derived } from "svelte/store";
import { createStudioTranslator, type StudioLocale } from "@livepeer-frameworks/streamcrafter-core";

export const studioLocaleStore = writable<StudioLocale>("en");
export const studioTranslatorStore = derived(studioLocaleStore, ($locale) =>
  createStudioTranslator({ locale: $locale })
);
