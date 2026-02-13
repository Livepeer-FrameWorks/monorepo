import type { IconName } from "$lib/iconUtils";
import type { ButtonVariant } from "$lib/components/ui/button";

export type EmptyStateSize = "sm" | "md" | "lg";
export type EmptyStateVariant = "default" | "accent" | "subtle";

export interface EmptyStateIconProps {
  icon?: IconName;
  iconName?: IconName;
}

export interface EmptyStateContract extends EmptyStateIconProps {
  buttonVariant?: ButtonVariant;
  size?: EmptyStateSize;
  variant?: EmptyStateVariant;
}

export function resolveEmptyStateIcon({ icon, iconName }: EmptyStateIconProps): IconName {
  return iconName ?? icon ?? "FileText";
}
