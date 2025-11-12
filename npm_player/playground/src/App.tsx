import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Player, type ContentEndpoints, type OutputEndpoint } from "@livepeer-frameworks/player";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Alert } from "@/components/ui/alert";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";
import { Check, Clipboard, Loader2, Plus, Trash } from "lucide-react";

type PlayerMode = "sandbox" | "override";

type MockStream = {
  id: string;
  label: string;
  description: string;
  endpoints: ContentEndpoints;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
};

const MOCK_STREAMS: MockStream[] = [
  {
    id: "mux-hls",
    label: "Mux Test HLS",
    description: "Public x36xhzz multi-bitrate HLS demo feed served by Mux.",
    contentId: "mux-demo-hls",
    contentType: "live",
    endpoints: {
      primary: {
        nodeId: "mock-mux-hls",
        protocol: "HLS",
        url: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
        outputs: {
          HLS: {
            protocol: "HLS",
            url: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
            capabilities: {
              supportsSeek: true,
              supportsQualitySwitch: true,
              hasAudio: true,
              hasVideo: true,
              codecs: ["avc1.4d001f", "mp4a.40.2"]
            }
          }
        }
      },
      fallbacks: []
    }
  },
  {
    id: "akamai-dash",
    label: "Akamai DASH Big Buck Bunny",
    description: "Reference DASH stream used for QA. Helpful for manifest inspection.",
    contentId: "bbb-dash",
    contentType: "clip",
    endpoints: {
      primary: {
        nodeId: "mock-akamai-dash",
        protocol: "DASH",
        url: "https://dash.akamaized.net/envivio/EnvivioDash3/manifest.mpd",
        outputs: {
          DASH: {
            protocol: "DASH",
            url: "https://dash.akamaized.net/envivio/EnvivioDash3/manifest.mpd",
            capabilities: {
              supportsSeek: true,
              supportsQualitySwitch: true,
              hasAudio: true,
              hasVideo: true,
              codecs: ["avc1.4d4028", "mp4a.40.2"]
            }
          }
        }
      },
      fallbacks: []
    }
  }
];

const STORAGE_KEYS = {
  theme: "fw.player.playground.theme",
  networkOptIn: "fw.player.playground.network"
} as const;

const MIST_STORAGE_KEYS = {
  settings: "fw.player.playground.mist.settings",
  overrides: "fw.player.playground.mist.overrides",
  profiles: "fw.player.playground.mist.profiles",
  selectedProfile: "fw.player.playground.mist.selectedProfile"
} as const;

type MistEndpointId = "whep" | "whip" | "hls" | "dash" | "mp4" | "mistHtml" | "playerJs" | "rtmp" | "srt";

type MistSettings = {
  baseUrl: string;
  viewerPath: string;
  streamName: string;
  label: string;
  authToken?: string;
  ingestApp?: string;
  ingestUser?: string;
  ingestPassword?: string;
};

type MistProfile = {
  id: string;
  name: string;
  settings: MistSettings;
  overrides: Partial<Record<MistEndpointId, string>>;
};

type MistEndpointDefinition = {
  id: MistEndpointId;
  label: string;
  category: "ingest" | "playback";
  hint?: string;
  build: (ctx: MistContext) => string;
};

type MistContext = {
  base: string;
  apiBase: string;
  viewerBase: string;
  streamName: string;
  authToken?: string;
  host?: string;
  ingestApp?: string;
};

type EndpointStatus = "idle" | "checking" | "ok" | "error";

const DEFAULT_MIST_SETTINGS: MistSettings = {
  baseUrl: "https://mist.dev.local",
  viewerPath: "/",
  streamName: "demo-stream",
  label: "Local Mist"
};

const MIST_ENDPOINTS: MistEndpointDefinition[] = [
  {
    id: "whip",
    label: "WHIP ingest",
    category: "ingest",
    hint: "Use for browser capture or tools that publish via WebRTC ingest.",
    build: ({ apiBase, streamName, authToken }) => {
      const base = `${apiBase}/whip/${streamName}`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "rtmp",
    label: "RTMP ingest",
    category: "ingest",
    hint: "OBS / FFmpeg; append ?token= if your Mist node expects auth.",
    build: ({ host, ingestApp, streamName, authToken }) => {
      const app = ingestApp ? ingestApp.replace(/^\//, "") : "live";
      const url = `rtmp://${host ?? "localhost"}/${app}/${streamName}`;
      return authToken ? `${url}?token=${authToken}` : url;
    }
  },
  {
    id: "srt",
    label: "SRT ingest",
    category: "ingest",
    hint: "Use with FFmpeg SRT caller mode.",
    build: ({ host, streamName, authToken }) => {
      const base = `srt://${host ?? "localhost"}:9000?streamid=#!::r=${streamName}`;
      return authToken ? `${base},token=${authToken}` : base;
    }
  },
  {
    id: "mistHtml",
    label: "Mist HTML player",
    category: "playback",
    hint: "Embed-safe HTML wrapper; ideal for copying into the APP for manual QA.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/view/${streamName}.html`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "playerJs",
    label: "player.js script",
    category: "playback",
    hint: "Use if you need raw Mist player JS for custom embeds.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/player.js?stream=${streamName}`;
      return authToken ? `${base}&token=${authToken}` : base;
    }
  },
  {
    id: "whep",
    label: "WHEP playback",
    category: "playback",
    hint: "Feeds the upgraded player’s WebRTC path.",
    build: ({ apiBase, streamName, authToken }) => {
      const base = `${apiBase}/whep/${streamName}`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "hls",
    label: "HLS manifest",
    category: "playback",
    hint: "Good for simple HTML5 testing or proxying through CDN.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/hls/${streamName}/index.m3u8`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "dash",
    label: "MPEG-DASH manifest",
    category: "playback",
    hint: "For players that prefer DASH (Shaka, Exo).",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/dash/${streamName}/manifest.mpd`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "mp4",
    label: "Progressive MP4",
    category: "playback",
    hint: "Great for quick sanity checks on recorded VOD clips.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/vod/${streamName}.mp4`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  }
];

const MIST_ENDPOINTS_BY_ID = MIST_ENDPOINTS.reduce<Record<MistEndpointId, MistEndpointDefinition>>((acc, def) => {
  acc[def.id] = def;
  return acc;
}, {} as Record<MistEndpointId, MistEndpointDefinition>);

function sanitizeBaseUrl(input: string): string {
  const trimmed = input.trim();
  if (!trimmed) return "http://localhost:4242";
  if (!/^https?:\/\//i.test(trimmed)) {
    return `https://${trimmed.replace(/^\/+/, "")}`;
  }
  return trimmed.replace(/\/+$/, "");
}

function normalizeViewerPath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed || trimmed === "/") return "";
  const withoutLeading = trimmed.replace(/^\/+/, "");
  const withoutTrailing = withoutLeading.replace(/\/+$/, "");
  return withoutTrailing ? `/${withoutTrailing}` : "";
}

function buildMistContext(settings: MistSettings): MistContext {
  const base = sanitizeBaseUrl(settings.baseUrl || DEFAULT_MIST_SETTINGS.baseUrl);
  const viewerPath = normalizeViewerPath(settings.viewerPath || DEFAULT_MIST_SETTINGS.viewerPath);
  const streamName = settings.streamName.trim() || DEFAULT_MIST_SETTINGS.streamName;
  const viewerBase = `${base}${viewerPath}`;
  const apiBase = `${base}/api`;
  let host: string | undefined;
  try {
    host = new URL(base).host;
  } catch {
    host = undefined;
  }
  return {
    base,
    apiBase,
    viewerBase,
    streamName,
    authToken: settings.authToken?.trim() || undefined,
    host,
    ingestApp: settings.ingestApp?.trim() || undefined
  };
}

function interpolateTemplate(template: string, ctx: MistContext): string {
  return template
    .replace(/\{stream\}/gi, ctx.streamName)
    .replace(/\{base\}/gi, ctx.base)
    .replace(/\{viewerBase\}/gi, ctx.viewerBase)
    .replace(/\{apiBase\}/gi, ctx.apiBase)
    .replace(/\{host\}/gi, ctx.host ?? "")
    .replace(/\{app\}/gi, ctx.ingestApp ?? "live");
}

function generateMistEndpointMap(ctx: MistContext, overrides: Partial<Record<MistEndpointId, string>>): Record<MistEndpointId, string> {
  return MIST_ENDPOINTS.reduce<Record<MistEndpointId, string>>((acc, def) => {
    const override = overrides[def.id];
    const value = override && override.trim().length ? override : def.build(ctx);
    acc[def.id] = interpolateTemplate(value, ctx);
    return acc;
  }, {} as Record<MistEndpointId, string>);
}

function buildMistContentEndpoints(ctx: MistContext, endpoints: Record<MistEndpointId, string>): ContentEndpoints | null {
  const outputs: Record<string, OutputEndpoint> = {};
  const push = (key: string, id: MistEndpointId) => {
    const url = endpoints[id];
    if (!url) return;
    outputs[key] = {
      protocol: key,
      url
    } as OutputEndpoint;
  };

  push("MIST_HTML", "mistHtml");
  push("PLAYER_JS", "playerJs");
  push("WHEP", "whep");
  push("HLS", "hls");
  push("DASH", "dash");
  push("MP4", "mp4");

  const primaryUrl =
    endpoints.whep ||
    endpoints.hls ||
    endpoints.mistHtml ||
    endpoints.mp4 ||
    endpoints.dash ||
    endpoints.playerJs;

  if (!primaryUrl) {
    return null;
  }

  return {
    primary: {
      nodeId: `mist-${ctx.streamName}`,
      protocol: "custom",
      url: primaryUrl,
      outputs
    },
    fallbacks: []
  };
}

function loadJson<T>(key: string): T | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

function saveJson<T>(key: string, value: T) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // ignore storage quota errors
  }
}

function getInitialMistState(): {
  settings: MistSettings;
  overrides: Partial<Record<MistEndpointId, string>>;
  profiles: MistProfile[];
  selectedProfile: string | null;
} {
  if (typeof window === "undefined") {
    return {
      settings: DEFAULT_MIST_SETTINGS,
      overrides: {},
      profiles: [],
      selectedProfile: null
    };
  }
  const profiles = loadJson<MistProfile[]>(MIST_STORAGE_KEYS.profiles) ?? [];
  const storedSelected = window.localStorage.getItem(MIST_STORAGE_KEYS.selectedProfile);
  const selectedProfile = storedSelected ? profiles.find((p) => p.id === storedSelected) ?? null : null;
  if (selectedProfile) {
    return {
      settings: selectedProfile.settings,
      overrides: selectedProfile.overrides ?? {},
      profiles,
      selectedProfile: selectedProfile.id
    };
  }
  return {
    settings: loadJson<MistSettings>(MIST_STORAGE_KEYS.settings) ?? DEFAULT_MIST_SETTINGS,
    overrides: loadJson<Partial<Record<MistEndpointId, string>>>(MIST_STORAGE_KEYS.overrides) ?? {},
    profiles,
    selectedProfile: storedSelected && profiles.some((p) => p.id === storedSelected) ? storedSelected : null
  };
}

function generateProfileId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `mist-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

export default function App(): JSX.Element {
  const initialMist = useMemo(() => getInitialMistState(), []);
  const [mode, setMode] = useState<PlayerMode>("sandbox");
  const [selectedMockId, setSelectedMockId] = useState<string>(MOCK_STREAMS[0]?.id ?? "");
  const [networkOptIn, setNetworkOptIn] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    return window.localStorage.getItem(STORAGE_KEYS.networkOptIn) === "true";
  });
  const [mistSettings, setMistSettings] = useState<MistSettings>(initialMist.settings);
  const [mistOverrides, setMistOverrides] = useState<Partial<Record<MistEndpointId, string>>>(initialMist.overrides);
  const [mistProfiles, setMistProfiles] = useState<MistProfile[]>(initialMist.profiles);
  const [selectedProfileId, setSelectedProfileId] = useState<string | null>(initialMist.selectedProfile);
  const [endpointStatus, setEndpointStatus] = useState<Record<MistEndpointId, EndpointStatus>>({});
  const [copiedEndpoint, setCopiedEndpoint] = useState<MistEndpointId | null>(null);
  const [whipPublishing, setWhipPublishing] = useState<boolean>(false);
  const [thumbnailUrl, setThumbnailUrl] = useState<string>("https://images.unsplash.com/photo-1500530855697-b586d89ba3ee?w=1200");
  const [autoplayMuted, setAutoplayMuted] = useState<boolean>(true);
  const [clickToPlay, setClickToPlay] = useState<boolean>(true);
  const [useDarkTheme, setUseDarkTheme] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    const stored = window.localStorage.getItem(STORAGE_KEYS.theme);
    if (stored) return stored === "dark";
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
});

  const selectedMock = useMemo(() => MOCK_STREAMS.find((s) => s.id === selectedMockId) ?? MOCK_STREAMS[0], [selectedMockId]);
  const mistContext = useMemo(() => buildMistContext(mistSettings), [mistSettings]);
  const mistEndpoints = useMemo(() => generateMistEndpointMap(mistContext, mistOverrides), [mistContext, mistOverrides]);
  const mistDefaultEndpoints = useMemo(() => generateMistEndpointMap(mistContext, {}), [mistContext]);
  const selectedProfile = useMemo(
    () => (selectedProfileId ? mistProfiles.find((profile) => profile.id === selectedProfileId) ?? null : null),
    [mistProfiles, selectedProfileId]
  );

  useEffect(() => {
    if (typeof document === "undefined") return;
    document.documentElement.classList.toggle("dark", useDarkTheme);
    window.localStorage.setItem(STORAGE_KEYS.theme, useDarkTheme ? "dark" : "light");
  }, [useDarkTheme]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(STORAGE_KEYS.networkOptIn, networkOptIn ? "true" : "false");
  }, [networkOptIn]);

  useEffect(() => {
    saveJson(MIST_STORAGE_KEYS.settings, mistSettings);
  }, [mistSettings]);

  useEffect(() => {
    if (Object.keys(mistOverrides).length) {
      saveJson(MIST_STORAGE_KEYS.overrides, mistOverrides);
    } else if (typeof window !== "undefined") {
      window.localStorage.removeItem(MIST_STORAGE_KEYS.overrides);
    }
  }, [mistOverrides]);

  useEffect(() => {
    saveJson(MIST_STORAGE_KEYS.profiles, mistProfiles);
  }, [mistProfiles]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (selectedProfileId) {
      window.localStorage.setItem(MIST_STORAGE_KEYS.selectedProfile, selectedProfileId);
    } else {
      window.localStorage.removeItem(MIST_STORAGE_KEYS.selectedProfile);
    }
  }, [selectedProfileId]);

  useEffect(() => {
    if (!selectedProfileId) return;
    if (!mistProfiles.some((profile) => profile.id === selectedProfileId)) {
      setSelectedProfileId(mistProfiles[0]?.id ?? null);
    }
  }, [mistProfiles, selectedProfileId]);

  useEffect(() => {
    setMistOverrides((prev) => {
      let changed = false;
      const next: Partial<Record<MistEndpointId, string>> = { ...prev };
      (Object.keys(next) as MistEndpointId[]).forEach((id) => {
        if (next[id] && next[id] === mistDefaultEndpoints[id]) {
          delete next[id];
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [mistDefaultEndpoints]);

  useEffect(() => {
    if (!copiedEndpoint) return;
    const timer = window.setTimeout(() => setCopiedEndpoint(null), 1500);
    return () => window.clearTimeout(timer);
  }, [copiedEndpoint]);

  useEffect(() => {
    if (!selectedProfile) return;
    setMistSettings(selectedProfile.settings);
    setMistOverrides(selectedProfile.overrides ?? {});
  }, [selectedProfile]);

  const activeEndpoints = useMemo<ContentEndpoints | null>(() => {
    if (mode === "sandbox") {
      return selectedMock?.endpoints ?? null;
    }
    return buildMistContentEndpoints(mistContext, mistEndpoints);
  }, [mode, mistContext, mistEndpoints, selectedMock]);

  const contentId = mode === "sandbox" ? selectedMock?.contentId ?? "mock-stream" : mistContext.streamName || "override";
  const contentType = mode === "sandbox" ? selectedMock?.contentType ?? "live" : "live";
  const showPlayer = networkOptIn && !!activeEndpoints;

  const handleMistSettingChange = useCallback(
    (key: keyof MistSettings, value: string) => {
      setMistSettings((prev) => ({
        ...prev,
        [key]: value
      }));
    },
    []
  );

  const handleEndpointChange = useCallback(
    (id: MistEndpointId, value: string) => {
      const def = MIST_ENDPOINTS_BY_ID[id];
      const defaultValue = interpolateTemplate(def.build(mistContext), mistContext);
      const trimmed = value.trim();
      setMistOverrides((prev) => {
        const next = { ...prev };
        if (!trimmed || trimmed === defaultValue) {
          if (next[id] === undefined) return prev;
          delete next[id];
          return next;
        }
        if (next[id] === trimmed) {
          return prev;
        }
        next[id] = trimmed;
        return next;
      });
    },
    [mistContext]
  );

  const handleEndpointReset = useCallback((id: MistEndpointId) => {
    setMistOverrides((prev) => {
      if (prev[id] === undefined) return prev;
      const next = { ...prev };
      delete next[id];
      return next;
    });
    setEndpointStatus((prev) => {
      if (prev[id] === undefined) return prev;
      const next = { ...prev };
      delete next[id];
      return next;
    });
  }, []);

  const handleCopyEndpoint = useCallback(
    async (id: MistEndpointId) => {
      const value = mistEndpoints[id];
      if (!value) return;
      try {
        if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(value);
        } else if (typeof document !== "undefined") {
          const textarea = document.createElement("textarea");
          textarea.value = value;
          textarea.setAttribute("readonly", "");
          textarea.style.position = "absolute";
          textarea.style.left = "-9999px";
          document.body.appendChild(textarea);
          textarea.select();
          document.execCommand("copy");
          document.body.removeChild(textarea);
        }
        setCopiedEndpoint(id);
      } catch {
        setCopiedEndpoint(null);
      }
    },
    [mistEndpoints]
  );

  const handleCheckEndpoint = useCallback(
    async (id: MistEndpointId) => {
      if (!networkOptIn) return;
      const url = mistEndpoints[id];
      if (!url) return;
      setEndpointStatus((prev) => ({ ...prev, [id]: "checking" }));
      const controller = typeof AbortController !== "undefined" ? new AbortController() : undefined;
      const timeout = controller ? window.setTimeout(() => controller.abort(), 5000) : undefined;
      try {
        const response = await fetch(url, { method: "HEAD", cache: "no-store", signal: controller?.signal });
        if (response.ok || response.type === "opaque") {
          setEndpointStatus((prev) => ({ ...prev, [id]: "ok" }));
        } else {
          throw new Error(`Status ${response.status}`);
        }
      } catch {
        try {
          const fallback = await fetch(url, { method: "GET", mode: "no-cors", cache: "no-store", signal: controller?.signal });
          if (fallback.ok || fallback.type === "opaque") {
            setEndpointStatus((prev) => ({ ...prev, [id]: "ok" }));
          } else {
            setEndpointStatus((prev) => ({ ...prev, [id]: "error" }));
          }
        } catch {
          setEndpointStatus((prev) => ({ ...prev, [id]: "error" }));
        }
      } finally {
        if (timeout) window.clearTimeout(timeout);
      }
    },
    [mistEndpoints, networkOptIn]
  );

  const handleSelectProfile = useCallback((id: string | null) => {
    setSelectedProfileId(id);
  }, []);

  const handleSaveProfile = useCallback(() => {
    const id = generateProfileId();
    const name = mistSettings.label.trim() || `Mist node ${mistProfiles.length + 1}`;
    const profile: MistProfile = {
      id,
      name,
      settings: { ...mistSettings },
      overrides: { ...mistOverrides }
    };
    setMistProfiles((prev) => [...prev, profile]);
    setSelectedProfileId(id);
  }, [mistOverrides, mistProfiles, mistSettings]);

  const handleUpdateProfile = useCallback(() => {
    if (!selectedProfile) return;
    const updatedLabel = mistSettings.label.trim() || selectedProfile.name;
    setMistProfiles((prev) =>
      prev.map((profile) =>
        profile.id === selectedProfile.id
          ? {
              ...profile,
              name: updatedLabel,
              settings: { ...mistSettings },
              overrides: { ...mistOverrides }
            }
          : profile
      )
    );
  }, [mistOverrides, mistSettings, selectedProfile]);

  const handleDeleteProfile = useCallback(() => {
    if (!selectedProfile) return;
    setMistProfiles((prev) => prev.filter((profile) => profile.id !== selectedProfile.id));
    if (selectedProfileId === selectedProfile.id) {
      setSelectedProfileId(null);
    }
  }, [selectedProfile, selectedProfileId]);

  const handleNewProfile = useCallback(() => {
    setSelectedProfileId(null);
    setMistSettings(DEFAULT_MIST_SETTINGS);
    setMistOverrides({});
  }, []);

  const ingestEndpointDefs = useMemo(() => MIST_ENDPOINTS.filter((def) => def.category === "ingest"), []);
  const playbackEndpointDefs = useMemo(() => MIST_ENDPOINTS.filter((def) => def.category === "playback"), []);

  return (
    <div className="min-h-screen bg-background pb-16 text-foreground">
      <header className="border-b border-border bg-card/50">
        <div className="container flex flex-col gap-6 py-10">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div>
              <Badge className="mb-2" variant="secondary">
                FrameWorks developer tooling
              </Badge>
              <h1 className="text-3xl font-semibold tracking-tight">Player playground</h1>
              <p className="mt-2 max-w-2xl text-muted-foreground">
                Exercise the upgraded player without touching production load balancers. Start with known-safe presets, then opt in to edge overrides when you need to validate a Mist node or Gateway response.
              </p>
            </div>
            <div className="flex items-center gap-3 rounded-md border border-border bg-card px-4 py-2">
              <Label htmlFor="theme">Dark theme</Label>
              <Switch id="theme" checked={useDarkTheme} onCheckedChange={setUseDarkTheme} aria-label="Toggle dark theme" />
            </div>
          </div>
          <Alert>
            <strong className="font-semibold text-foreground">Safety first.</strong> Networking is off by default. Enable it only when you intend to reach real infrastructure or public demo streams.
          </Alert>
          <div className="flex items-center justify-between rounded-md border border-border bg-card px-4 py-3 text-sm">
            <div className="flex flex-col gap-1">
              <span className="font-medium text-foreground">Networking opt-in</span>
              <span className="text-muted-foreground">
                When disabled, the player UI renders in a dormant state and no requests are issued.
              </span>
            </div>
            <Switch checked={networkOptIn} onCheckedChange={setNetworkOptIn} id="network-toggle" aria-label="Toggle network access" />
          </div>
        </div>
      </header>

      <main className="container mt-10 space-y-10">
        <section>
          <Tabs value={mode} onValueChange={(value) => setMode(value as PlayerMode)}>
            <TabsList>
              <TabsTrigger value="sandbox">Safe presets</TabsTrigger>
              <TabsTrigger value="override">Mist workspace</TabsTrigger>
            </TabsList>

            <TabsContent value="sandbox" className="border-none bg-transparent p-0 shadow-none">
              <div className="grid gap-6 lg:grid-cols-[320px_1fr]">
                <Card>
                  <CardHeader>
                    <CardTitle>Choose a fixture</CardTitle>
                    <CardDescription>Select from vetted public streams that stay off the FrameWorks balancers.</CardDescription>
                  </CardHeader>
                  <CardContent className="space-y-4">
                    <div className="space-y-2">
                      <Label htmlFor="mock-select">Fixture</Label>
                      <select
                        id="mock-select"
                        className="h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                        value={selectedMockId}
                        onChange={(event) => setSelectedMockId(event.target.value)}
                      >
                        {MOCK_STREAMS.map((stream) => (
                          <option key={stream.id} value={stream.id}>
                            {stream.label}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="rounded-md border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
                      {selectedMock?.description}
                    </div>
                    <Separator />
                    <div className="space-y-2">
                      <Label htmlFor="thumbnail-url">Thumbnail image</Label>
                      <Input
                        id="thumbnail-url"
                        value={thumbnailUrl}
                        onChange={(event) => setThumbnailUrl(event.target.value)}
                        placeholder="https://example.com/poster.jpg"
                      />
                      <p className="text-xs text-muted-foreground">
                        Applied as the poster image before playback. Works for both thumbnail overlay modes below.
                      </p>
                    </div>
                    <div className="flex items-center justify-between gap-2">
                      <Label htmlFor="click-to-play">Click to play</Label>
                      <Switch id="click-to-play" checked={clickToPlay} onCheckedChange={setClickToPlay} />
                    </div>
                    <div className="flex items-center justify-between gap-2">
                      <Label htmlFor="autoplay-muted">Autoplay muted</Label>
                      <Switch id="autoplay-muted" checked={autoplayMuted} onCheckedChange={setAutoplayMuted} />
                    </div>
                  </CardContent>
                </Card>

                <PlayerPreview
                  showPlayer={showPlayer}
                  networkOptIn={networkOptIn}
                  endpoints={activeEndpoints}
                  contentId={contentId}
                  contentType={contentType}
                  thumbnailUrl={thumbnailUrl}
                  clickToPlay={clickToPlay}
                  autoplayMuted={autoplayMuted}
                />
              </div>
            </TabsContent>

            <TabsContent value="override" className="border-none bg-transparent p-0 shadow-none">
              <div className="grid gap-6 xl:grid-cols-[380px_1fr]">
                <div className="space-y-6">
                  <Card>
                    <CardHeader>
                      <CardTitle>Mist node configuration</CardTitle>
                      <CardDescription>Define the base Mist URL once and let the playground derive every ingest and playback endpoint.</CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-4">
                      <div className="space-y-2">
                        <Label htmlFor="mist-label">Workspace label</Label>
                        <Input
                          id="mist-label"
                          placeholder="Local Mist"
                          value={mistSettings.label}
                          onChange={(event) => handleMistSettingChange("label", event.target.value)}
                        />
                      </div>
                      <div className="space-y-2">
                        <Label htmlFor="mist-base-url">Mist base URL</Label>
                        <Input
                          id="mist-base-url"
                          placeholder="https://mist.dev.local"
                          value={mistSettings.baseUrl}
                          onChange={(event) => handleMistSettingChange("baseUrl", event.target.value)}
                        />
                        <p className="text-xs text-muted-foreground">Include scheme + host (and port if needed). The derived endpoints reuse this origin.</p>
                      </div>
                      <div className="grid gap-4 md:grid-cols-2">
                        <div className="space-y-2">
                          <Label htmlFor="mist-viewer-path">Viewer path (optional)</Label>
                          <Input
                            id="mist-viewer-path"
                            placeholder="/stream"
                            value={mistSettings.viewerPath}
                            onChange={(event) => handleMistSettingChange("viewerPath", event.target.value)}
                          />
                        </div>
                        <div className="space-y-2">
                          <Label htmlFor="mist-stream-name">Stream name</Label>
                          <Input
                            id="mist-stream-name"
                            placeholder="demo-stream"
                            value={mistSettings.streamName}
                            onChange={(event) => handleMistSettingChange("streamName", event.target.value)}
                          />
                        </div>
                      </div>
                      <Accordion type="single" collapsible className="rounded-md border border-border bg-muted/20">
                        <AccordionItem value="mist-advanced">
                          <AccordionTrigger className="px-3">Advanced fields</AccordionTrigger>
                          <AccordionContent className="space-y-3 px-3">
                            <div className="space-y-2">
                              <Label htmlFor="mist-auth-token">Auth token (optional)</Label>
                              <Input
                                id="mist-auth-token"
                                placeholder="Playback token appended as ?token="
                                value={mistSettings.authToken ?? ""}
                                onChange={(event) => handleMistSettingChange("authToken", event.target.value)}
                              />
                              <p className="text-xs text-muted-foreground">Token is appended to every derived URL so you can test gated nodes safely.</p>
                            </div>
                            <div className="space-y-2">
                              <Label htmlFor="mist-ingest-app">RTMP / SRT application</Label>
                              <Input
                                id="mist-ingest-app"
                                placeholder="live"
                                value={mistSettings.ingestApp ?? ""}
                                onChange={(event) => handleMistSettingChange("ingestApp", event.target.value)}
                              />
                              <p className="text-xs text-muted-foreground">Used when building RTMP and SRT ingest strings.</p>
                            </div>
                          </AccordionContent>
                        </AccordionItem>
                      </Accordion>
                    </CardContent>
                  </Card>

                  <Card>
                    <CardHeader>
                      <CardTitle>Saved Mist profiles</CardTitle>
                      <CardDescription>Capture frequently used nodes (local Docker, staging, etc.) and switch between them instantly.</CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-4">
                      <div className="space-y-2">
                        <Label htmlFor="mist-profile-select">Active profile</Label>
                        <select
                          id="mist-profile-select"
                          className="h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                          value={selectedProfileId ?? ""}
                          onChange={(event) => handleSelectProfile(event.target.value ? event.target.value : null)}
                        >
                          <option value="">Unsaved workspace</option>
                          {mistProfiles.map((profile) => (
                            <option key={profile.id} value={profile.id}>
                              {profile.name}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button size="sm" variant="secondary" onClick={handleSaveProfile}>
                          Save as new
                        </Button>
                        <Button size="sm" variant="outline" onClick={handleUpdateProfile} disabled={!selectedProfile}>
                          Update profile
                        </Button>
                        <Button size="sm" variant="outline" onClick={handleDeleteProfile} disabled={!selectedProfile}>
                          Delete
                        </Button>
                        <Button size="sm" variant="ghost" onClick={handleNewProfile}>
                          Reset workspace
                        </Button>
                      </div>
                    </CardContent>
                  </Card>

                  <Card>
                    <CardHeader>
                      <CardTitle>WHIP publish helper</CardTitle>
                      <CardDescription>Push your webcam/mic directly into Mist without leaving the playground. Great for fast E2E checks.</CardDescription>
                    </CardHeader>
                    <CardContent>
                      <WhipPublisher endpoint={mistEndpoints.whip} enabled={networkOptIn} />
                    </CardContent>
                  </Card>
                </div>

                <div className="space-y-6">
                  <Card>
                    <CardHeader>
                      <CardTitle>Derived endpoints</CardTitle>
                      <CardDescription>
                        Copy, tweak, and validate every ingest/egress URL. Use tokens like{" "}
                        <code className="rounded bg-muted px-1 font-mono text-xs">{'{stream}'}</code> to keep them dynamic.
                      </CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-6">
                      <div className="space-y-3">
                        <div className="flex items-center justify-between">
                          <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Ingest</h3>
                          <span className="text-xs text-muted-foreground">Feed Mist via OBS, FFmpeg, or browser capture.</span>
                        </div>
                        <div className="space-y-4">
                          {ingestEndpointDefs.map((def) => (
                            <EndpointRow
                              key={def.id}
                              definition={def}
                              value={mistOverrides[def.id] ?? mistDefaultEndpoints[def.id] ?? ""}
                              resolvedValue={mistEndpoints[def.id]}
                              isCustom={mistOverrides[def.id] !== undefined}
                              onChange={(val) => handleEndpointChange(def.id, val)}
                              onReset={() => handleEndpointReset(def.id)}
                              onCopy={() => handleCopyEndpoint(def.id)}
                              status={endpointStatus[def.id]}
                              showCheck={false}
                              checking={endpointStatus[def.id] === "checking"}
                              copied={copiedEndpoint === def.id}
                              disabled={!mistEndpoints[def.id]}
                              networkOptIn={networkOptIn}
                            />
                          ))}
                        </div>
                      </div>
                      <Separator />
                      <div className="space-y-3">
                        <div className="flex items-center justify-between">
                          <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Playback</h3>
                          <span className="text-xs text-muted-foreground">Validate the outputs your apps and the new player will consume.</span>
                        </div>
                        <div className="space-y-4">
                          {playbackEndpointDefs.map((def) => (
                            <EndpointRow
                              key={def.id}
                              definition={def}
                              value={mistOverrides[def.id] ?? mistDefaultEndpoints[def.id] ?? ""}
                              resolvedValue={mistEndpoints[def.id]}
                              isCustom={mistOverrides[def.id] !== undefined}
                              onChange={(val) => handleEndpointChange(def.id, val)}
                              onReset={() => handleEndpointReset(def.id)}
                              onCopy={() => handleCopyEndpoint(def.id)}
                              status={endpointStatus[def.id]}
                              showCheck
                              checking={endpointStatus[def.id] === "checking"}
                              copied={copiedEndpoint === def.id}
                              onCheck={() => handleCheckEndpoint(def.id)}
                              disabled={!mistEndpoints[def.id]}
                              networkOptIn={networkOptIn}
                            />
                          ))}
                        </div>
                      </div>
                    </CardContent>
                  </Card>

                  <PlayerPreview
                    showPlayer={showPlayer}
                    networkOptIn={networkOptIn}
                    endpoints={activeEndpoints}
                    contentId={contentId}
                    contentType={contentType}
                    thumbnailUrl={thumbnailUrl}
                    clickToPlay={clickToPlay}
                    autoplayMuted={autoplayMuted}
                  />
                </div>
              </div>
            </TabsContent>
          </Tabs>
        </section>

        <section className="rounded-lg border border-border bg-card/40 p-6">
          <h2 className="text-xl font-semibold">Workflow tips</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            Keep this playground linked to your local player package via `pnpm link` or `npm link`. When you iterate on ShadCN layouts or player state handling, the preview updates instantly with Vite&apos;s HMR.
          </p>
          <div className="mt-4 flex flex-wrap gap-2 text-sm">
            <Button variant="outline" asChild>
              <a href="https://github.com/livepeer" target="_blank" rel="noreferrer">
                Livepeer docs
              </a>
            </Button>
            <Button variant="outline" asChild>
              <a href="https://ui.shadcn.com" target="_blank" rel="noreferrer">
                ShadCN UI reference
              </a>
            </Button>
          </div>
        </section>
      </main>
    </div>
  );
}

type PlayerPreviewProps = {
  showPlayer: boolean;
  networkOptIn: boolean;
  endpoints: ContentEndpoints | null;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
  thumbnailUrl: string;
  clickToPlay: boolean;
  autoplayMuted: boolean;
};

function PlayerPreview({
  showPlayer,
  networkOptIn,
  endpoints,
  contentId,
  contentType,
  thumbnailUrl,
  clickToPlay,
  autoplayMuted
}: PlayerPreviewProps) {
  return (
    <Card className="flex flex-col">
      <CardHeader>
        <CardTitle>Player output</CardTitle>
        <CardDescription>
          {showPlayer ? "Rendering with live player logic." : "Enable networking and configure an endpoint to mount the player."}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex-1 space-y-4">
        <div className={cn("relative aspect-video overflow-hidden rounded-md border border-border bg-muted", !showPlayer && "flex items-center justify-center")}>
          {showPlayer && endpoints ? (
            <Player
              contentId={contentId}
              contentType={contentType}
              endpoints={endpoints}
              thumbnailUrl={thumbnailUrl}
              options={{
                autoplay: autoplayMuted || !clickToPlay,
                muted: autoplayMuted,
                controls: true,
                stockControls: true
              }}
            />
          ) : (
            <span className="text-sm text-muted-foreground">
              {networkOptIn ? "Add a URL or choose a fixture to begin." : "Networking disabled. Toggle it on to exercise the player."}
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

type EndpointRowProps = {
  definition: MistEndpointDefinition;
  value: string;
  resolvedValue?: string;
  isCustom: boolean;
  onChange: (value: string) => void;
  onReset: () => void;
  onCopy: () => void;
  onCheck?: () => void;
  status?: EndpointStatus;
  showCheck: boolean;
  checking: boolean;
  copied: boolean;
  disabled: boolean;
  networkOptIn: boolean;
};

function EndpointRow({
  definition,
  value,
  resolvedValue,
  isCustom,
  onChange,
  onReset,
  onCopy,
  onCheck,
  status,
  showCheck,
  checking,
  copied,
  disabled,
  networkOptIn
}: EndpointRowProps) {
  const statusPill = (() => {
    if (status === "checking") {
      return (
        <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          Checking
        </span>
      );
    }
    if (status === "ok") {
      return <span className="inline-flex items-center rounded-full bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-600 dark:text-green-300">Reachable</span>;
    }
    if (status === "error") {
      return <span className="inline-flex items-center rounded-full bg-red-500/10 px-2 py-0.5 text-xs font-medium text-red-600 dark:text-red-300">Error</span>;
    }
    return null;
  })();

  return (
    <div className="space-y-2 rounded-md border border-border/60 p-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-foreground">{definition.label}</p>
          {definition.hint && <p className="text-xs text-muted-foreground">{definition.hint}</p>}
          {isCustom && <span className="mt-1 inline-flex rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:bg-amber-900/40 dark:text-amber-200">Custom</span>}
        </div>
        {statusPill}
      </div>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <Input
          value={value}
          onChange={(event) => onChange(event.target.value)}
          className="flex-1 font-mono text-xs"
          spellCheck={false}
          autoCorrect="off"
        />
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" size="icon" onClick={onCopy} disabled={disabled}>
            {copied ? <Check className="h-4 w-4" /> : <Clipboard className="h-4 w-4" />}
          </Button>
          {showCheck && onCheck && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onCheck}
              disabled={disabled || checking || !networkOptIn}
            >
              {checking ? (
                <>
                  <Loader2 className="mr-1 h-4 w-4 animate-spin" />
                  Checking…
                </>
              ) : (
                "Check"
              )}
            </Button>
          )}
          {isCustom && (
            <Button type="button" variant="ghost" size="sm" onClick={onReset}>
              Reset
            </Button>
          )}
        </div>
      </div>
      {resolvedValue && resolvedValue !== value && (
        <p className="text-xs text-muted-foreground">
          Resolved:&nbsp;
          <span className="font-mono">{resolvedValue}</span>
        </p>
      )}
    </div>
  );
}

type WhipPublisherProps = {
  endpoint?: string;
  enabled: boolean;
};

function WhipPublisher({ endpoint, enabled }: WhipPublisherProps) {
  const [status, setStatus] = useState<"idle" | "starting" | "publishing" | "error">("idle");
  const [error, setError] = useState<string | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const resourceRef = useRef<string | null>(null);

  const stopPublishing = useCallback(async () => {
    pcRef.current?.close();
    pcRef.current = null;
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
    if (resourceRef.current) {
      try {
        await fetch(resourceRef.current, { method: "DELETE" });
      } catch {
        // ignore
      } finally {
        resourceRef.current = null;
      }
    }
    setStatus("idle");
  }, []);

  const startPublishing = useCallback(async () => {
    if (!enabled) {
      setError("Enable networking before starting a WHIP session.");
      return;
    }
    if (!endpoint) {
      setError("Fill out the Mist base URL and stream name to generate a WHIP endpoint.");
      return;
    }

    setStatus("starting");
    setError(null);

    try {
      if (typeof navigator === "undefined" || !navigator.mediaDevices?.getUserMedia) {
        throw new Error("MediaDevices API unavailable in this environment.");
      }

      const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
      streamRef.current = stream;

      if (videoRef.current) {
        videoRef.current.srcObject = stream;
        await videoRef.current.play().catch(() => undefined);
      }

      const pc = new RTCPeerConnection();
      stream.getTracks().forEach((track) => pc.addTrack(track, stream));
      pcRef.current = pc;

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);

      const response = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/sdp" },
        body: offer.sdp ?? "",
        cache: "no-store"
      });

      if (!response.ok) {
        throw new Error(`WHIP init failed: ${response.status} ${response.statusText}`);
      }

      const answer = await response.text();
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
      resourceRef.current = response.headers.get("Location");
      setStatus("publishing");
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setError(message);
      await stopPublishing();
      setStatus("error");
    }
  }, [enabled, endpoint, stopPublishing]);

  useEffect(() => {
    return () => {
      void stopPublishing();
    };
  }, [stopPublishing]);

  const isDisabled = !enabled || !endpoint;

  return (
    <div className="space-y-3">
      {!enabled && (
        <Alert variant="warning">
          Toggle networking on to publish. The helper will not attempt any requests while disabled.
        </Alert>
      )}
      {enabled && !endpoint && (
        <Alert variant="info">
          Provide a Mist base URL and stream name to generate a WHIP endpoint automatically.
        </Alert>
      )}
      <div className="aspect-video overflow-hidden rounded-md border border-dashed border-border/60 bg-muted/40">
        <video ref={videoRef} playsInline muted className="h-full w-full object-cover" />
      </div>
      <div className="flex flex-wrap gap-2">
        <Button onClick={startPublishing} disabled={status === "starting" || status === "publishing" || isDisabled}>
          {status === "starting" ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" /> Starting…
            </>
          ) : status === "publishing" ? (
            "Publishing"
          ) : (
            "Start capture"
          )}
        </Button>
        <Button variant="outline" onClick={() => void stopPublishing()} disabled={status !== "publishing"}>
          Stop
        </Button>
        {resourceRef.current && (
          <Button variant="ghost" size="sm" asChild>
            <a href={resourceRef.current} target="_blank" rel="noreferrer">
              Session resource
            </a>
          </Button>
        )}
      </div>
      {endpoint && (
        <p className="text-xs text-muted-foreground">
          WHIP endpoint:&nbsp;<span className="font-mono">{endpoint}</span>
        </p>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}
