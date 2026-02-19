import { useMemo, type ReactNode } from "react";
import { createTranslator, type FwLocale } from "@livepeer-frameworks/player-core";
import { I18nContext } from "./i18n";

export interface I18nProviderProps {
  children: ReactNode;
  locale: FwLocale;
}

export function I18nProvider({ children, locale }: I18nProviderProps) {
  const t = useMemo(() => createTranslator({ locale }), [locale]);
  return <I18nContext.Provider value={t}>{children}</I18nContext.Provider>;
}
