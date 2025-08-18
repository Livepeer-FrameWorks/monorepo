import { writable } from 'svelte/store';

/**
 * @typedef {Object} Toast
 * @property {string} id - Unique identifier
 * @property {'success' | 'error' | 'warning' | 'info'} type - Toast type
 * @property {string} message - Toast message
 * @property {number} duration - Auto-dismiss duration in ms (0 = no auto-dismiss)
 */

function createToastStore() {
  /** @type {import('svelte/store').Writable<Toast[]>} */
  const { subscribe, update } = writable([]);

  return {
    subscribe,
    
    /**
     * Add a toast notification
     * @param {'success' | 'error' | 'warning' | 'info'} type
     * @param {string} message
     * @param {number} [duration=5000] - Auto-dismiss duration in ms (0 = no auto-dismiss)
     */
    add(type, message, duration = 5000) {
      const id = Date.now().toString() + Math.random().toString(36).substr(2, 9);
      const toast = { id, type, message, duration };
      
      update(toasts => [...toasts, toast]);
      
      // Auto-dismiss if duration > 0
      if (duration > 0) {
        setTimeout(() => {
          this.remove(id);
        }, duration);
      }
      
      return id;
    },
    
    /**
     * Remove a toast by ID
     * @param {string} id
     */
    remove(id) {
      update(toasts => toasts.filter(toast => toast.id !== id));
    },
    
    /**
     * Clear all toasts
     */
    clear() {
      update(() => []);
    },
    
    // Convenience methods
    success(message, duration) {
      return this.add('success', message, duration);
    },
    
    error(message, duration) {
      return this.add('error', message, duration);
    },
    
    warning(message, duration) {
      return this.add('warning', message, duration);
    },
    
    info(message, duration) {
      return this.add('info', message, duration);
    }
  };
}

export const toast = createToastStore();