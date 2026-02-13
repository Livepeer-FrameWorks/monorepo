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

const dynamicRoutes: Array<{
  pattern: RegExp;
  route: Omit<RouteInfo, "path">;
  breadcrumb: Breadcrumb[];
}> = [
  {
    pattern: /^\/streams\/[^/]+$/,
    route: { name: "Stream Details", parent: "Content" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Content" },
      { name: "Streams", href: "/streams" },
      { name: "Stream Details" },
    ],
  },
  {
    pattern: /^\/streams\/[^/]+\/analytics$/,
    route: { name: "Stream Analytics", parent: "Content" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Content" },
      { name: "Streams", href: "/streams" },
      { name: "Stream Analytics" },
    ],
  },
  {
    pattern: /^\/streams\/[^/]+\/health$/,
    route: { name: "Stream Health", parent: "Content" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Content" },
      { name: "Streams", href: "/streams" },
      { name: "Stream Health" },
    ],
  },
  {
    pattern: /^\/messages\/[^/]+$/,
    route: { name: "Conversation", parent: "Support" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Support" },
      { name: "Messages", href: "/messages" },
      { name: "Conversation" },
    ],
  },
  {
    pattern: /^\/nodes\/[^/]+$/,
    route: { name: "Node Details", parent: "Infrastructure" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Infrastructure" },
      { name: "Nodes", href: "/nodes" },
      { name: "Node Details" },
    ],
  },
  {
    pattern: /^\/infrastructure\/[^/]+$/,
    route: { name: "Cluster Details", parent: "Infrastructure" },
    breadcrumb: [
      { name: "Dashboard", href: "/" },
      { name: "Infrastructure" },
      { name: "Overview", href: "/infrastructure" },
      { name: "Cluster Details" },
    ],
  },
];

function normalizePath(path: string): string {
  const [withoutQuery] = path.split(/[?#]/, 1);
  if (!withoutQuery) {
    return "/";
  }
  if (withoutQuery !== "/" && withoutQuery.endsWith("/")) {
    return withoutQuery.slice(0, -1);
  }
  return withoutQuery;
}

function findDynamicRoute(path: string) {
  return dynamicRoutes.find((route) => route.pattern.test(path));
}

// Navigation configuration for FrameWorks webapp

export const navigationConfig: Record<string, NavigationItem> = {
  // Main Dashboard - Always visible when authenticated
  dashboard: {
    name: "Dashboard",
    href: "/",
    icon: "LayoutDashboard",
    active: true,
    description: "Quick overview with KPIs and contextual hints",
  },

  // Content - Streaming & Media
  content: {
    name: "Content",
    icon: "Video",
    children: {
      streams: {
        name: "Streams",
        href: "/streams",
        icon: "Radio",
        active: true,
        description: "Manage your live streams",
      },
      library: {
        name: "Library",
        href: "/library",
        icon: "FolderOpen",
        active: true,
        description: "Clips, recordings, and VOD assets in one place",
      },
      goLive: {
        name: "Go Live",
        href: "/go-live",
        icon: "Globe",
        active: true,
        description: "Stream directly from your browser with WebRTC",
      },
      composer: {
        name: "Composer",
        href: "/composer",
        icon: "Clapperboard",
        active: "soon",
        description: "Compose multiple input streams with picture-in-picture layouts",
      },
    },
  },

  // Analytics & Insights
  analytics: {
    name: "Analytics",
    icon: "BarChart3",
    children: {
      overview: {
        name: "Overview",
        href: "/analytics",
        icon: "ChartLine",
        active: true,
        description: "Real-time metrics and streaming analytics overview",
      },
      audience: {
        name: "Audience",
        href: "/analytics/audience",
        icon: "Globe2",
        active: true,
        description: "Geographic distribution, viewer sessions, and routing",
      },
      usage: {
        name: "Usage & Costs",
        href: "/analytics/usage",
        icon: "Gauge",
        active: true,
        description: "Usage, storage, transcoding, and cost breakdown",
      },
    },
  },

  // Infrastructure Management
  infrastructure: {
    name: "Infrastructure",
    icon: "Building2",
    children: {
      overview: {
        name: "Overview",
        href: "/infrastructure",
        icon: "Server",
        active: true,
        description: "Monitor clusters, nodes, and system health in real-time",
      },
      nodes: {
        name: "Nodes",
        href: "/nodes",
        icon: "HardDrive",
        active: true,
        description: "Manage your Edge nodes and capacity",
      },
      clusters: {
        name: "Clusters",
        href: "/infrastructure/clusters",
        icon: "Server",
        active: true,
        description: "Manage cluster connections and browse the marketplace",
      },
      federation: {
        name: "Federation",
        href: "/infrastructure/federation",
        icon: "Globe",
        active: true,
        description: "Cross-cluster topology, peering, and federation traffic",
      },
      devices: {
        name: "Devices",
        href: "/devices",
        icon: "Camera",
        active: "soon",
        description: "Manage your fleet of remote AV devices",
      },
    },
  },

  // AI & Automation
  ai: {
    name: "AI & Automation",
    icon: "Bot",
    children: {
      processing: {
        name: "Computer Vision",
        href: "/ai/vision",
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
      daydream: {
        name: "Daydream",
        href: "/ai/daydream",
        icon: "Sparkles",
        active: "soon",
        description: "Live video-to-video generative effects for streams",
      },
    },
  },

  // Account & Settings
  account: {
    name: "Account",
    icon: "User",
    children: {
      settings: {
        name: "Settings",
        href: "/settings",
        icon: "Settings",
        active: true,
        description: "Manage profile and notifications",
      },
      billing: {
        name: "Billing & Plans",
        href: "/account/billing",
        icon: "CreditCard",
        active: true,
        description: "Manage billing, subscriptions, and payment methods",
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
        name: "API",
        href: "/developer/api",
        icon: "Key",
        active: true,
        description: "Manage API keys for programmatic access",
      },
      webhooks: {
        name: "Webhooks",
        href: "/developer/webhooks",
        icon: "Link",
        active: "soon",
        description: "Configure event notifications and external integrations",
      },
      sdks: {
        name: "SDKs & Libraries",
        href: "/developer/sdks",
        icon: "Package",
        active: true,
        description: "Player and Studio SDKs for React, Svelte, and vanilla JS",
      },
    },
  },

  // Support & Community
  support: {
    name: "Support",
    icon: "MessageCircle",
    children: {
      skipper: {
        name: "Skipper",
        href: "/skipper",
        icon: "Bot",
        active: true,
        description: "AI video consultant for diagnostics, analytics, and stream help",
      },
      messages: {
        name: "Messages",
        href: "/messages",
        icon: "MessageSquare",
        active: true,
        description: "Contact support and view conversation history",
      },
    },
  },
};

export function getRouteInfo(path: string): RouteInfo | null {
  const normalizedPath = normalizePath(path);

  // Handle dashboard
  if (normalizedPath === "/") {
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
        if (child.href === normalizedPath) {
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

  const dynamicRoute = findDynamicRoute(normalizedPath);
  if (dynamicRoute) {
    return {
      path: normalizedPath,
      ...dynamicRoute.route,
    };
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
  const normalizedPath = normalizePath(path);
  const breadcrumbs: Breadcrumb[] = [];

  if (normalizedPath === "/") {
    return [{ name: "Dashboard" }];
  }

  // Find the route in navigation
  for (const section of Object.values(navigationConfig)) {
    if (section.children) {
      for (const child of Object.values(section.children)) {
        if (child.href === normalizedPath) {
          breadcrumbs.push({ name: "Dashboard", href: "/" });
          breadcrumbs.push({ name: section.name });
          breadcrumbs.push({ name: child.name });
          return breadcrumbs;
        }
      }
    }
  }

  const dynamicRoute = findDynamicRoute(normalizedPath);
  if (dynamicRoute) {
    return dynamicRoute.breadcrumb;
  }

  return [{ name: "Dashboard", href: "/" }];
}
