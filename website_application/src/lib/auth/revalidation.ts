export interface TrailingRevalidator {
  schedule(): void;
  cancel(): void;
}

export function createTrailingRevalidator(
  revalidate: () => void,
  delayMs = 250
): TrailingRevalidator {
  let timer: ReturnType<typeof setTimeout> | null = null;

  return {
    schedule() {
      if (timer !== null) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = null;
        revalidate();
      }, delayMs);
    },
    cancel() {
      if (timer !== null) clearTimeout(timer);
      timer = null;
    },
  };
}
