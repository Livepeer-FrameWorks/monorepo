import { browser } from '$app/environment';

const STORAGE_KEY = 'frameworks-sidebar-prefs';

interface SidebarPreferences {
  collapsed: boolean;
  expandedSections: string[];
}

const DEFAULT_PREFS: SidebarPreferences = {
  collapsed: false,
  expandedSections: []
};

function loadFromStorage(): SidebarPreferences {
  if (!browser) return DEFAULT_PREFS;

  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) {
      const parsed = JSON.parse(stored);
      return {
        collapsed: typeof parsed.collapsed === 'boolean' ? parsed.collapsed : DEFAULT_PREFS.collapsed,
        expandedSections: Array.isArray(parsed.expandedSections) ? parsed.expandedSections : DEFAULT_PREFS.expandedSections
      };
    }
  } catch {
    // Ignore parse errors, use defaults
  }

  return DEFAULT_PREFS;
}

function saveToStorage(prefs: SidebarPreferences): void {
  if (!browser) return;

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Ignore storage errors (quota exceeded, etc.)
  }
}

function createSidebarStore() {
  let collapsed = $state(DEFAULT_PREFS.collapsed);
  let expandedSections = $state<Set<string>>(new Set(DEFAULT_PREFS.expandedSections));
  let initialized = $state(false);

  // Load from storage on init (browser only)
  if (browser) {
    const prefs = loadFromStorage();
    collapsed = prefs.collapsed;
    expandedSections = new Set(prefs.expandedSections);
    initialized = true;
  }

  function persist() {
    saveToStorage({
      collapsed,
      expandedSections: Array.from(expandedSections)
    });
  }

  return {
    get collapsed() {
      return collapsed;
    },

    get expandedSections() {
      return expandedSections;
    },

    get initialized() {
      return initialized;
    },

    initialize() {
      if (initialized) return;
      const prefs = loadFromStorage();
      collapsed = prefs.collapsed;
      expandedSections = new Set(prefs.expandedSections);
      initialized = true;
    },

    toggleCollapsed() {
      collapsed = !collapsed;
      persist();
    },

    setCollapsed(value: boolean) {
      collapsed = value;
      persist();
    },

    expandSection(sectionKey: string) {
      expandedSections = new Set([...expandedSections, sectionKey]);
      persist();
    },

    collapseSection(sectionKey: string) {
      const next = new Set(expandedSections);
      next.delete(sectionKey);
      expandedSections = next;
      persist();
    },

    toggleSection(sectionKey: string) {
      if (expandedSections.has(sectionKey)) {
        this.collapseSection(sectionKey);
      } else {
        this.expandSection(sectionKey);
      }
    },

    isSectionExpanded(sectionKey: string): boolean {
      return expandedSections.has(sectionKey);
    },

    // Expand section without persisting (for auto-expand on navigation)
    autoExpandSection(sectionKey: string) {
      if (!expandedSections.has(sectionKey)) {
        expandedSections = new Set([...expandedSections, sectionKey]);
        // Don't persist auto-expansions - only user actions
      }
    }
  };
}

export const sidebarStore = createSidebarStore();
