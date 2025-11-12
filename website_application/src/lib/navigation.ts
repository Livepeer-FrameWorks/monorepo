export interface NavigationItem {
  name: string;
  href?: string;
  icon: string;
  active?: boolean | string;
  description?: string;
  tier?: string;
  badge?: string;
  external?: boolean;
  children?: Record<string, NavigationItem>;
}

export interface RouteInfo {
  path: string;
  name: string;
  parent: string;
  description?: string;
}

export interface Breadcrumb {
  name: string;
  href?: string;
}

// Navigation configuration for FrameWorks webapp
// Features marked as 'active' are implemented, 'soon' shows coming soon, 'disabled' is hidden
//
// FUTURE TIER SYSTEM:
// When features are implemented, we'll add tier restrictions:
// - tier: 'Free' - Available to all users
// - tier: 'Pro' - Requires Pro subscription
// - tier: 'Enterprise' - Enterprise features only
// - badge: 'New', 'Popular', 'Beta' - Special feature highlights

export const navigationConfig: Record<string, NavigationItem> = {
  // Main Dashboard - Always visible when authenticated
  dashboard: {
    name: "Dashboard",
    href: "/",
    icon: "BarChart3",
    active: true,
    description: "Overview of your streams and analytics",
  },

  // Core Streaming Features
  streaming: {
    name: "Streaming",
    icon: "Video",
    children: {
      overview: {
        name: "Stream Overview",
        href: "/streams",
        icon: "Monitor",
        active: true,
        description: "Manage your live streams",
      },
      browser: {
        name: "Browser Streaming",
        href: "/streams/browser",
        icon: "Globe",
        active: "soon",
        description: "Stream directly from your browser with WebRTC",
      },
      settings: {
        name: "Stream Settings",
        href: "/streams/settings",
        icon: "Settings",
        active: "soon",
        description: "Configure transcoding, recording, and stream options",
      },
      composer: {
        name: "Stream Composer",
        href: "/streams/composer",
        icon: "Clapperboard",
        active: "soon",
        description: "Multi-stream compositing with PiP and overlays",
      },
    },
  },

  // Media Management
  media: {
    name: "Media",
    icon: "FolderOpen",
    children: {
      clips: {
        name: "Clips",
        href: "/clips",
        icon: "Scissors",
        active: true,
        description: "Create and manage stream clips",
      },
      recordings: {
        name: "Recordings",
        href: "/recordings",
        icon: "Film",
        active: true,
        description: "Access your stream recordings",
      },
    },
  },

  // Analytics & Insights
  analytics: {
    name: "Analytics",
    icon: "TrendingUp",
    children: {
      overview: {
        name: "Analytics Overview",
        href: "/analytics",
        icon: "BarChart3",
        active: true,
        description: "View comprehensive streaming analytics",
      },
      realtime: {
        name: "Real-time Stats",
        href: "/analytics/realtime",
        icon: "Zap",
        active: true,
        description: "Live viewer and performance metrics",
      },
      geographic: {
        name: "Geographic Analytics",
        href: "/analytics/geographic",
        icon: "Globe2",
        active: true,
        description: "View viewer distribution and regional metrics",
      },
      usage: {
        name: "Usage Analytics",
        href: "/analytics/usage",
        icon: "Activity",
        active: true,
        description: "Track resource usage and performance metrics",
      },
    },
  },

  // Infrastructure Management
  infrastructure: {
    name: "Infrastructure",
    icon: "Building2",
    children: {
      nodes: {
        name: "Node Management",
        href: "/nodes",
        icon: "Server",
        active: true,
        description: "Manage your Edge nodes worldwide",
      },
      devices: {
        name: "Device Discovery",
        href: "/devices",
        icon: "Camera",
        active: "soon",
        description: "Auto-discover and configure AV devices",
      },
      network: {
        name: "Network Status",
        href: "/infrastructure/network",
        icon: "Wifi",
        active: "soon",
        description: "Monitor network health and performance",
      },
    },
  },

  // AI & Automation
  ai: {
    name: "AI & Automation",
    icon: "Bot",
    children: {
      processing: {
        name: "AI Processing",
        href: "/ai",
        icon: "Brain",
        active: "soon",
        description: "Real-time AI analysis and processing",
      },
      transcription: {
        name: "Live Transcription",
        href: "/ai/transcription",
        icon: "FileText",
        active: "soon",
        description: "Automatic speech-to-text for streams",
      },
    },
  },

  // Account & Settings
  account: {
    name: "Account",
    icon: "User",
    children: {
      profile: {
        name: "Profile Settings",
        href: "/account/profile",
        icon: "Settings",
        active: "soon",
        description: "Manage your account profile and preferences",
      },
      billing: {
        name: "Billing & Plans",
        href: "/account/billing",
        icon: "CreditCard",
        active: true,
        description: "Manage billing, subscriptions, and payment methods",
      },
      notifications: {
        name: "Notifications",
        href: "/account/notifications",
        icon: "Bell",
        active: "soon",
        description: "Configure alerts and notification preferences",
      },
    },
  },

  // Team & Collaboration
  team: {
    name: "Team",
    icon: "Users",
    children: {
      members: {
        name: "Team Members",
        href: "/team",
        icon: "UserPlus",
        active: "soon",
        description: "Invite and manage team members",
      },
      permissions: {
        name: "Permissions",
        href: "/team/permissions",
        icon: "Shield",
        active: "soon",
        description: "Configure role-based access control",
      },
      activity: {
        name: "Team Activity",
        href: "/team/activity",
        icon: "ScrollText",
        active: "soon",
        description: "View team member activity and logs",
      },
    },
  },

  // Developer Tools
  developer: {
    name: "Developer",
    icon: "Code2",
    children: {
      api: {
        name: "API & Keys",
        href: "/developer/api",
        icon: "BookOpen",
        active: true,
        description: "API reference and manage API keys",
      },
      webhooks: {
        name: "Webhooks",
        href: "/developer/webhooks",
        icon: "Link",
        active: "soon",
        description: "Configure event notifications and integrations",
      },
      sdk: {
        name: "SDKs & Libraries",
        href: "/developer/sdk",
        icon: "Package",
        active: "soon",
        description: "Download SDKs and integration libraries",
      },
    },
  },

  // Support & Community
  support: {
    name: "Support",
    icon: "MessageCircle",
    children: {
      help: {
        name: "Help Center",
        href: "/support/help",
        icon: "HelpCircle",
        active: "soon",
        description: "Browse documentation and tutorials",
      },
      tickets: {
        name: "Support Tickets",
        href: "/support/tickets",
        icon: "Ticket",
        active: "soon",
        description: "Get help from our support team",
      },
    },
  },
};

export function getRouteInfo(path: string): RouteInfo | null {
  // Handle dashboard
  if (path === "/") {
    return {
      path: "/",
      name: "Dashboard",
      parent: "root",
    };
  }

  // Search through navigation config
  for (const section of Object.values(navigationConfig)) {
    if (section.children) {
      for (const child of Object.values(section.children)) {
        if (child.href === path) {
          return {
            path: child.href,
            name: child.name,
            parent: section.name,
            description: child.description,
          };
        }
      }
    }
  }

  return null;
}

export function getAllRoutes(): RouteInfo[] {
  const routes: RouteInfo[] = [
    {
      path: "/",
      name: "Dashboard",
      parent: "root",
    },
  ];

  for (const section of Object.values(navigationConfig)) {
    if (section.children) {
      for (const child of Object.values(section.children)) {
        if (child.href) {
          const route: RouteInfo = {
            path: child.href,
            name: child.name,
            parent: section.name,
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

export function getBreadcrumbs(path: string): Breadcrumb[] {
  const breadcrumbs: Breadcrumb[] = [];

  if (path === "/") {
    return [{ name: "Dashboard" }];
  }

  // Find the route in navigation
  for (const section of Object.values(navigationConfig)) {
    if (section.children) {
      for (const child of Object.values(section.children)) {
        if (child.href === path) {
          breadcrumbs.push({ name: "Dashboard", href: "/" });
          breadcrumbs.push({ name: section.name });
          breadcrumbs.push({ name: child.name });
          return breadcrumbs;
        }
      }
    }
  }

  return [{ name: "Dashboard", href: "/" }];
}
