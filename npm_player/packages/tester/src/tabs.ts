import type { KeyboardEvent } from "react";

/**
 * Arrow-key, Home and End handling for a tablist whose tabs select on focus, as the
 * WAI-ARIA tabs pattern describes. Pair it with tabIndex 0 on the selected tab and -1 on the rest.
 */
export function tabListKeyDown<T>(items: readonly T[], selected: T, select: (item: T) => void) {
  return (event: KeyboardEvent<HTMLElement>) => {
    const index = items.indexOf(selected);
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = (index + 1) % items.length;
        break;
      case "ArrowLeft":
        next = (index - 1 + items.length) % items.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    select(items[next]);
    const tabs = event.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]');
    tabs[next]?.focus();
  };
}
