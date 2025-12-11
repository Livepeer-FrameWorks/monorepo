import { useCallback, useEffect, useMemo, useState } from "react";
import type { ContentEndpoints } from "@livepeer-frameworks/player";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Header } from "@/components/Header";
import { SandboxTab } from "@/components/SandboxTab";
import { MistWorkspaceTab } from "@/components/MistWorkspaceTab";
import {
  type PlayerMode,
  type MistSettings,
  type MistEndpointId,
  type MistProfile,
  type EndpointStatus,
  STORAGE_KEYS,
  MIST_STORAGE_KEYS,
  MOCK_STREAMS,
  DEFAULT_MIST_SETTINGS,
  MIST_ENDPOINTS_BY_ID,
  buildMistContext,
  generateMistEndpointMap,
  buildMistContentEndpoints,
  interpolateTemplate,
  saveJson,
  generateProfileId,
  getInitialMistState
} from "@/lib";

export default function App(): JSX.Element {
  // Initialize mist state from localStorage
  const initialMist = useMemo(() => getInitialMistState(), []);

  // Core state
  const [mode, setMode] = useState<PlayerMode>("sandbox");
  const [selectedMockId, setSelectedMockId] = useState<string>(MOCK_STREAMS[0]?.id ?? "");
  const [networkOptIn, setNetworkOptIn] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    return window.localStorage.getItem(STORAGE_KEYS.networkOptIn) === "true";
  });

  // Mist settings state
  const [mistSettings, setMistSettings] = useState<MistSettings>(initialMist.settings);
  const [mistOverrides, setMistOverrides] = useState<Partial<Record<MistEndpointId, string>>>(initialMist.overrides);
  const [mistProfiles, setMistProfiles] = useState<MistProfile[]>(initialMist.profiles);
  const [selectedProfileId, setSelectedProfileId] = useState<string | null>(initialMist.selectedProfile);
  const [endpointStatus, setEndpointStatus] = useState<Record<MistEndpointId, EndpointStatus>>({} as Record<MistEndpointId, EndpointStatus>);
  const [copiedEndpoint, setCopiedEndpoint] = useState<MistEndpointId | null>(null);

  // Player settings state
  const [thumbnailUrl, setThumbnailUrl] = useState<string>("https://images.unsplash.com/photo-1500530855697-b586d89ba3ee?w=1200");
  const [autoplayMuted, setAutoplayMuted] = useState<boolean>(true);
  const [clickToPlay, setClickToPlay] = useState<boolean>(true);
  const [useDarkTheme, setUseDarkTheme] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    const stored = window.localStorage.getItem(STORAGE_KEYS.theme);
    if (stored) return stored === "dark";
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
  });

  // Derived values
  const selectedMock = useMemo(() => MOCK_STREAMS.find((s) => s.id === selectedMockId) ?? MOCK_STREAMS[0], [selectedMockId]);
  const mistContext = useMemo(() => buildMistContext(mistSettings), [mistSettings]);
  const mistEndpoints = useMemo(() => generateMistEndpointMap(mistContext, mistOverrides), [mistContext, mistOverrides]);
  const mistDefaultEndpoints = useMemo(() => generateMistEndpointMap(mistContext, {}), [mistContext]);
  const selectedProfile = useMemo(
    () => (selectedProfileId ? mistProfiles.find((profile) => profile.id === selectedProfileId) ?? null : null),
    [mistProfiles, selectedProfileId]
  );

  // Theme effect
  useEffect(() => {
    if (typeof document === "undefined") return;
    document.documentElement.classList.toggle("dark", useDarkTheme);
    window.localStorage.setItem(STORAGE_KEYS.theme, useDarkTheme ? "dark" : "light");
  }, [useDarkTheme]);

  // Network opt-in persistence
  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(STORAGE_KEYS.networkOptIn, networkOptIn ? "true" : "false");
  }, [networkOptIn]);

  // Mist settings persistence
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

  // Auto-select first profile if selected one is deleted
  useEffect(() => {
    if (!selectedProfileId) return;
    if (!mistProfiles.some((profile) => profile.id === selectedProfileId)) {
      setSelectedProfileId(mistProfiles[0]?.id ?? null);
    }
  }, [mistProfiles, selectedProfileId]);

  // Clean up overrides that match defaults
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

  // Clear copied indicator after timeout
  useEffect(() => {
    if (!copiedEndpoint) return;
    const timer = window.setTimeout(() => setCopiedEndpoint(null), 1500);
    return () => window.clearTimeout(timer);
  }, [copiedEndpoint]);

  // Load profile settings when profile is selected
  useEffect(() => {
    if (!selectedProfile) return;
    setMistSettings(selectedProfile.settings);
    setMistOverrides(selectedProfile.overrides ?? {});
  }, [selectedProfile]);

  // Computed player state
  const activeEndpoints = useMemo<ContentEndpoints | null>(() => {
    if (mode === "sandbox") {
      return selectedMock?.endpoints ?? null;
    }
    return buildMistContentEndpoints(mistContext, mistEndpoints);
  }, [mode, mistContext, mistEndpoints, selectedMock]);

  const contentId = mode === "sandbox" ? selectedMock?.contentId ?? "mock-stream" : mistContext.streamName || "override";
  const contentType = mode === "sandbox" ? selectedMock?.contentType ?? "live" : "live";
  const showPlayer = networkOptIn && !!activeEndpoints;

  // Handlers
  const handleMistSettingChange = useCallback(
    (key: keyof MistSettings, value: string) => {
      setMistSettings((prev) => ({ ...prev, [key]: value }));
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

  return (
    <div className="min-h-screen bg-background pb-16 text-foreground">
      <Header
        useDarkTheme={useDarkTheme}
        onThemeChange={setUseDarkTheme}
        networkOptIn={networkOptIn}
        onNetworkOptInChange={setNetworkOptIn}
      />

      <main className="container mt-10">
        <section>
          <Tabs value={mode} onValueChange={(value) => setMode(value as PlayerMode)}>
            <TabsList>
              <TabsTrigger value="sandbox">Safe presets</TabsTrigger>
              <TabsTrigger value="override">Mist workspace</TabsTrigger>
            </TabsList>

            <TabsContent value="sandbox" className="mt-6 border-none bg-transparent p-0 shadow-none">
              <SandboxTab
                mockStreams={MOCK_STREAMS}
                selectedMockId={selectedMockId}
                onMockSelect={setSelectedMockId}
                selectedMock={selectedMock}
                thumbnailUrl={thumbnailUrl}
                onThumbnailChange={setThumbnailUrl}
                clickToPlay={clickToPlay}
                onClickToPlayChange={setClickToPlay}
                autoplayMuted={autoplayMuted}
                onAutoplayMutedChange={setAutoplayMuted}
                showPlayer={showPlayer}
                networkOptIn={networkOptIn}
                activeEndpoints={activeEndpoints}
                contentId={contentId}
                contentType={contentType}
              />
            </TabsContent>

            <TabsContent value="override" className="mt-6 border-none bg-transparent p-0 shadow-none">
              <MistWorkspaceTab
                mistSettings={mistSettings}
                onSettingChange={handleMistSettingChange}
                mistProfiles={mistProfiles}
                selectedProfileId={selectedProfileId}
                onSelectProfile={handleSelectProfile}
                onSaveProfile={handleSaveProfile}
                onUpdateProfile={handleUpdateProfile}
                onDeleteProfile={handleDeleteProfile}
                onNewProfile={handleNewProfile}
                mistContext={mistContext}
                mistEndpoints={mistEndpoints}
                mistDefaultEndpoints={mistDefaultEndpoints}
                mistOverrides={mistOverrides}
                endpointStatus={endpointStatus}
                copiedEndpoint={copiedEndpoint}
                onEndpointChange={handleEndpointChange}
                onEndpointReset={handleEndpointReset}
                onCopyEndpoint={handleCopyEndpoint}
                onCheckEndpoint={handleCheckEndpoint}
                showPlayer={showPlayer}
                networkOptIn={networkOptIn}
                activeEndpoints={activeEndpoints}
                contentId={contentId}
                contentType={contentType}
                thumbnailUrl={thumbnailUrl}
                clickToPlay={clickToPlay}
                autoplayMuted={autoplayMuted}
              />
            </TabsContent>
          </Tabs>
        </section>

        <div className="seam my-10" />

        <section className="slab slab--compact">
          <div className="slab-header">
            <h3 className="slab-title">Workflow Tips</h3>
          </div>
          <div className="slab-body--padded">
            <p className="text-sm text-muted-foreground">
              Keep this playground linked to your local player package via `pnpm link` or `npm link`. When you iterate on ShadCN layouts or player state handling, the preview updates instantly with Vite&apos;s HMR.
            </p>
          </div>
          <div className="slab-actions slab-actions--row">
            <Button variant="ghost" asChild>
              <a href="https://github.com/livepeer" target="_blank" rel="noreferrer">
                Livepeer docs
              </a>
            </Button>
            <Button variant="ghost" asChild>
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
