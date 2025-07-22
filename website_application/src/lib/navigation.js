// @ts-check

/**
 * @typedef {Object} NavigationItem
 * @property {string} name
 * @property {string} [href]
 * @property {string} icon
 * @property {boolean | string} [active]
 * @property {string} [description]
 * @property {string} [tier] - Future: 'Free', 'Pro', 'Enterprise' for feature gating
 * @property {string} [badge] - Future: Custom badges for special features
 * @property {boolean} [external]
 * @property {Object.<string, NavigationItem>} [children]
 */

/**
 * @typedef {Object} RouteInfo
 * @property {string} path
 * @property {string} name
 * @property {string} parent
 * @property {string} [description]
 */

// Navigation configuration for FrameWorks webapp
// Features marked as 'active' are implemented, 'soon' shows coming soon, 'disabled' is hidden
//
// FUTURE TIER SYSTEM:
// When features are implemented, we'll add tier restrictions:
// - tier: 'Free' - Available to all users
// - tier: 'Pro' - Requires Pro subscription  
// - tier: 'Enterprise' - Enterprise features only
// - badge: 'New', 'Popular', 'Beta' - Special feature highlights

/** @type {Object.<string, NavigationItem>} */
export const navigationConfig = {
  // Main Dashboard - Always visible when authenticated
  dashboard: {
    name: 'Dashboard',
    href: '/',
    icon: '📊',
    active: true,
    description: 'Overview of your streams and analytics'
  },

  // Core Streaming Features
  streaming: {
    name: 'Streaming',
    icon: '🎥',
    children: {
      overview: {
        name: 'Stream Overview',
        href: '/streams',
        icon: '📺',
        active: true,
        description: 'Manage your live streams'
      },
      browser: {
        name: 'Browser Streaming',
        href: '/streams/browser',
        icon: '🌐',
        active: 'soon',
        description: 'Stream directly from your browser with WebRTC'
      },
      settings: {
        name: 'Stream Settings',
        href: '/streams/settings',
        icon: '⚙️',
        active: 'soon',
        description: 'Configure transcoding, recording, and stream options'
      },
      composer: {
        name: 'Stream Composer',
        href: '/streams/composer',
        icon: '🎬',
        active: 'soon',
        description: 'Multi-stream compositing with PiP and overlays'
      }
    }
  },

  // Media Management
  media: {
    name: 'Media',
    icon: '📁',
    children: {
      clips: {
        name: 'Clips',
        href: '/clips',
        icon: '✂️',
        active: 'soon',
        description: 'Create and manage stream clips'
      },
      recordings: {
        name: 'Recordings',
        href: '/recordings',
        icon: '🎞️',
        active: 'soon',
        description: 'Access your stream recordings'
      }
    }
  },

  // Analytics & Insights
  analytics: {
    name: 'Analytics',
    icon: '📈',
    children: {
      overview: {
        name: 'Analytics Overview',
        href: '/analytics',
        icon: '📊',
        active: true,
        description: 'View comprehensive streaming analytics'
      },
      realtime: {
        name: 'Real-time Stats',
        href: '/analytics/realtime',
        icon: '⚡',
        active: true,
        description: 'Live viewer and performance metrics'
      },
      geographic: {
        name: 'Geographic Analytics',
        href: '/analytics/geographic',
        icon: '🌍',
        active: true,
        description: 'View viewer distribution and regional metrics'
      },
      usage: {
        name: 'Usage Analytics',
        href: '/analytics/usage',
        icon: '📊',
        active: true,
        description: 'Track resource usage and performance metrics'
      }
    }
  },

  // Infrastructure Management
  infrastructure: {
    name: 'Infrastructure',
    icon: '🏗️',
    children: {
      nodes: {
        name: 'Node Management',
        href: '/nodes',
        icon: '🖥️',
        active: 'soon',
        description: 'Manage your Edge nodes worldwide'
      },
      devices: {
        name: 'Device Discovery',
        href: '/devices',
        icon: '📹',
        active: 'soon',
        description: 'Auto-discover and configure AV devices'
      },
      network: {
        name: 'Network Status',
        href: '/infrastructure/network',
        icon: '📡',
        active: 'soon',
        description: 'Monitor network health and performance'
      }
    }
  },

  // AI & Automation
  ai: {
    name: 'AI & Automation',
    icon: '🤖',
    children: {
      processing: {
        name: 'AI Processing',
        href: '/ai',
        icon: '🧠',
        active: 'soon',
        description: 'Real-time AI analysis and processing'
      },
      transcription: {
        name: 'Live Transcription',
        href: '/ai/transcription',
        icon: '📝',
        active: 'soon',
        description: 'Automatic speech-to-text for streams'
      }
    }
  },

  // Account & Settings
  account: {
    name: 'Account',
    icon: '👤',
    children: {
      profile: {
        name: 'Profile Settings',
        href: '/account/profile',
        icon: '⚙️',
        active: 'soon',
        description: 'Manage your account profile and preferences'
      },
      billing: {
        name: 'Billing & Plans',
        href: '/account/billing',
        icon: '💳',
        active: true,
        description: 'Manage billing, subscriptions, and payment methods'
      },
      notifications: {
        name: 'Notifications',
        href: '/account/notifications',
        icon: '🔔',
        active: 'soon',
        description: 'Configure alerts and notification preferences'
      }
    }
  },

  // Team & Collaboration
  team: {
    name: 'Team',
    icon: '👥',
    children: {
      members: {
        name: 'Team Members',
        href: '/team',
        icon: '👤',
        active: 'soon',
        description: 'Invite and manage team members'
      },
      permissions: {
        name: 'Permissions',
        href: '/team/permissions',
        icon: '🔐',
        active: 'soon',
        description: 'Configure role-based access control'
      },
      activity: {
        name: 'Team Activity',
        href: '/team/activity',
        icon: '📋',
        active: 'soon',
        description: 'View team member activity and logs'
      }
    }
  },

  // Developer Tools
  developer: {
    name: 'Developer',
    icon: '⚡',
    children: {
      api: {
        name: 'API & Keys',
        href: '/developer/api',
        icon: '📚',
        active: true,
        description: 'API reference and manage API keys'
      },
      webhooks: {
        name: 'Webhooks',
        href: '/developer/webhooks',
        icon: '🔗',
        active: 'soon',
        description: 'Configure event notifications and integrations'
      },
      sdk: {
        name: 'SDKs & Libraries',
        href: '/developer/sdk',
        icon: '📦',
        active: 'soon',
        description: 'Download SDKs and integration libraries'
      }
    }
  },

  // Support & Community
  support: {
    name: 'Support',
    icon: '💬',
    children: {
      help: {
        name: 'Help Center',
        href: '/support/help',
        icon: '❓',
        active: 'soon',
        description: 'Browse documentation and tutorials'
      },
      tickets: {
        name: 'Support Tickets',
        href: '/support/tickets',
        icon: '🎫',
        active: 'soon',
        description: 'Get help from our support team'
      }
    }
  }
};

// Helper function to get route information
/**
 * @param {string} path
 * @returns {RouteInfo | null}
 */
export function getRouteInfo(path) {
  // Handle dashboard
  if (path === '/') {
    return {
      path: '/',
      name: 'Dashboard',
      parent: 'root'
    };
  }

  // Search through navigation config
  for (const [sectionKey, section] of Object.entries(navigationConfig)) {
    if (section.children) {
      for (const [childKey, child] of Object.entries(section.children)) {
        if (child.href === path) {
          return {
            path: child.href,
            name: child.name,
            parent: section.name,
            description: child.description
          };
        }
      }
    }
  }

  return null;
}

// Get all available routes
/**
 * @returns {RouteInfo[]}
 */
export function getAllRoutes() {
  const routes = [
    {
      path: '/',
      name: 'Dashboard',
      parent: 'root'
    }
  ];

  for (const [sectionKey, section] of Object.entries(navigationConfig)) {
    if (section.children) {
      for (const [childKey, child] of Object.entries(section.children)) {
        if (child.href) {
          /** @type {RouteInfo} */
          const route = {
            path: child.href,
            name: child.name,
            parent: section.name
          };
          if (child.description) {
            route.description = child.description;
          }
          routes.push(route);
        }
      }
    }
  }

  return routes;
}

// Get navigation breadcrumbs for a path
/**
 * @param {string} path
 * @returns {Array<{name: string, href?: string}>}
 */
export function getBreadcrumbs(path) {
  const breadcrumbs = [];

  if (path === '/') {
    return [{ name: 'Dashboard' }];
  }

  // Find the route in navigation
  for (const [sectionKey, section] of Object.entries(navigationConfig)) {
    if (section.children) {
      for (const [childKey, child] of Object.entries(section.children)) {
        if (child.href === path) {
          breadcrumbs.push({ name: 'Dashboard', href: '/' });
          breadcrumbs.push({ name: section.name });
          breadcrumbs.push({ name: child.name });
          return breadcrumbs;
        }
      }
    }
  }

  return [{ name: 'Dashboard', href: '/' }];
} 