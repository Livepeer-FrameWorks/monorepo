import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Merge Tailwind class names with variant conditionals.
 */
export function cn(...inputs) {
  return twMerge(clsx(inputs))
}
