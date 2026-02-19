import { createContext, useContext } from "react";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

export const I18nContext = createContext<TranslateFn | null>(null);

const DEFAULT_T = createTranslator({ locale: "en" });

/**
 * Hook to access the translation function.
 * Falls back to English when used outside an I18nProvider.
 */
export function useTranslate(): TranslateFn {
  return useContext(I18nContext) ?? DEFAULT_T;
}
