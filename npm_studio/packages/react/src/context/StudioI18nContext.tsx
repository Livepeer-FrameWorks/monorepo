import React, { createContext, useContext, useMemo, type ReactNode } from "react";
import {
  createStudioTranslator,
  type StudioTranslateFn,
  type StudioLocale,
} from "@livepeer-frameworks/streamcrafter-core";

const StudioI18nContext = createContext<StudioTranslateFn | null>(null);

const DEFAULT_T = createStudioTranslator({ locale: "en" });

export interface StudioI18nProviderProps {
  children: ReactNode;
  locale: StudioLocale;
}

export function StudioI18nProvider({ children, locale }: StudioI18nProviderProps) {
  const t = useMemo(() => createStudioTranslator({ locale }), [locale]);
  return <StudioI18nContext.Provider value={t}>{children}</StudioI18nContext.Provider>;
}

/**
 * Hook to access the studio translation function.
 * Falls back to English when used outside a StudioI18nProvider.
 */
export function useStudioTranslate(): StudioTranslateFn {
  return useContext(StudioI18nContext) ?? DEFAULT_T;
}
