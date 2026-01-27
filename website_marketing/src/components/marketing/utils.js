// Shared utility functions for marketing components

export const renderSlot = (slot) => (typeof slot === "function" ? slot() : slot);

export const isProbablyExternalHref = (href) => {
  if (!href) return false;
  if (href.startsWith("#")) return false;
  if (href.startsWith("/")) return false;
  return true;
};
