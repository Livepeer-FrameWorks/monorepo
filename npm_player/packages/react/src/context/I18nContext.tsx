import { useMemo, type ReactNode } from "react";
import {
  createTranslator,
  type FwLocale,
  type TranslationStrings,
} from "@livepeer-frameworks/player-core";
import { I18nContext } from "./i18n";

export interface I18nProviderProps {
  children: ReactNode;
  locale: FwLocale;
  translations?: Partial<TranslationStrings>;
}

export function I18nProvider({ children, locale, translations }: I18nProviderProps) {
  const t = useMemo(() => createTranslator({ locale, translations }), [locale, translations]);
  return <I18nContext.Provider value={t}>{children}</I18nContext.Provider>;
}
