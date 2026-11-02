import type { ComplexAttributeConverter } from "lit";

/**
 * A boolean attribute that is on unless it says "false". Lit's Boolean type
 * treats any present attribute as true, so `show-producer-panel="false"` would
 * otherwise turn the feature on.
 */
export const defaultTrueBoolean: ComplexAttributeConverter<boolean> = {
  fromAttribute(value: string | null): boolean {
    return value === null || value.trim().toLowerCase() !== "false";
  },
  toAttribute(value: boolean): string {
    return value ? "" : "false";
  },
};
