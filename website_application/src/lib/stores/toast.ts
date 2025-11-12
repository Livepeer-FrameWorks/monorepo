import { writable } from "svelte/store";

type ToastType = "success" | "error" | "warning" | "info";

interface Toast {
  id: string;
  type: ToastType;
  message: string;
  duration: number;
}

function createToastStore() {
  const { subscribe, update } = writable<Toast[]>([]);

  return {
    subscribe,

    add(type: ToastType, message: string, duration: number = 5000): string {
      const id = Date.now().toString() + Math.random().toString(36).substr(2, 9);
      const toast: Toast = { id, type, message, duration };

      update((toasts) => [...toasts, toast]);

      if (duration > 0) {
        setTimeout(() => {
          this.remove(id);
        }, duration);
      }

      return id;
    },

    remove(id: string): void {
      update((toasts) => toasts.filter((toast) => toast.id !== id));
    },

    clear(): void {
      update(() => []);
    },

    success(message: string, duration?: number): string {
      return this.add("success", message, duration);
    },

    error(message: string, duration?: number): string {
      return this.add("error", message, duration);
    },

    warning(message: string, duration?: number): string {
      return this.add("warning", message, duration);
    },

    info(message: string, duration?: number): string {
      return this.add("info", message, duration);
    },
  };
}

export const toast = createToastStore();
