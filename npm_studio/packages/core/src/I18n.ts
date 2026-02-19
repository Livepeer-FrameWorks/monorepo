/**
 * Studio i18n — translation system for StreamCrafter.
 *
 * Same pattern as the player's I18n.ts but with studio-specific
 * translation keys (~120 core strings, expandable by consumers).
 */

// ============================================================================
// Types
// ============================================================================

export interface StudioTranslationStrings {
  // Status
  idle: string;
  requestingPermissions: string;
  ready: string;
  connecting: string;
  live: string;
  reconnecting: string;
  reconnectingAttempt: string;
  error: string;
  destroyed: string;
  resolvingEndpoint: string;

  // Primary actions
  goLive: string;
  stopStreaming: string;

  // Secondary actions
  addCamera: string;
  cameraActive: string;
  shareScreen: string;
  settings: string;

  // Header
  streamCrafter: string;

  // Quality profiles
  professional: string;
  professionalDesc: string;
  broadcast: string;
  broadcastDesc: string;
  conference: string;
  conferenceDesc: string;

  // Sources
  primary: string;
  camera: string;
  screen: string;
  custom: string;
  mute: string;
  unmute: string;
  primaryVideoSource: string;
  setAsPrimary: string;
  removeSource: string;
  cannotRemoveWhileStreaming: string;
  hideFromComposition: string;
  showInComposition: string;

  // Preview
  addSourcePrompt: string;
  streamPreview: string;

  // Mixer
  mixer: string;
  sources: string;
  collapseMixer: string;
  expandMixer: string;

  // Context menu
  copyWhipUrl: string;
  copyStreamInfo: string;
  hideAdvanced: string;
  advanced: string;

  // Advanced panel tabs
  audio: string;
  stats: string;
  info: string;
  comp: string;
  closeAdvancedPanel: string;

  // Audio section
  masterVolume: string;
  outputLevel: string;
  audioMixing: string;
  on: string;
  off: string;
  compressorLimiterActive: string;
  processing: string;

  // Audio processing
  echoCancellation: string;
  echoCancellationDesc: string;
  noiseSuppression: string;
  noiseSuppressionDesc: string;
  autoGainControl: string;
  autoGainControlDesc: string;
  modified: string;
  sampleRate: string;
  channels: string;

  // Stats section
  connection: string;
  bitrate: string;
  video: string;
  frameRate: string;
  framesEncoded: string;
  packetsLost: string;
  rtt: string;
  iceState: string;
  waitingForStats: string;
  startStreamingForStats: string;

  // Info section
  qualityProfile: string;
  whipEndpoint: string;
  notConfigured: string;
  copyUrl: string;
  encoder: string;
  resetToProfile: string;
  videoCodec: string;
  resolution: string;
  actualResolution: string;
  framerate: string;
  actualFramerate: string;
  videoBitrate: string;
  audioCodec: string;
  audioBitrate: string;
  settingsLockedWhileStreaming: string;
  noSourcesAdded: string;

  // Encoder
  webCodecs: string;
  browser: string;
  useWebCodecs: string;
  enableWebCodecsDesc: string;
  webCodecsUnsupported: string;
  changeTakesEffect: string;

  // Compositor
  compositor: string;
  layout: string;
  display: string;
  renderer: string;
  notInitialized: string;
  performance: string;
  frameTime: string;
  gpuMemory: string;
  composition: string;
  scenes: string;
  layers: string;
  enableCompositor: string;

  // Layouts
  solo: string;
  grid: string;
  stack: string;

  // Scaling
  letterboxFit: string;
  cropFill: string;
  stretch: string;

  // Layers
  noLayers: string;
  hideLayer: string;
  showLayer: string;
  moveUp: string;
  moveDown: string;
  editOpacity: string;
  removeLayer: string;

  // Scenes
  deleteScene: string;
  createNewScene: string;

  // Transitions
  cut: string;
  fade: string;
  slideLeft: string;
  slideRight: string;
  slideUp: string;
  slideDown: string;

  // Debug
  state: string;
  active: string;
  inactive: string;
  whip: string;
  ok: string;
  notSet: string;

  // Errors
  configureWhipEndpoint: string;

  // Missing UI strings
  quality: string;
  debug: string;
  type: string;
  warning: string;
  encoderStats: string;
  videoFrames: string;
  videoPending: string;
  videoBytes: string;
  audioSamples: string;
  audioBytes: string;
  transitionDuration: string;
  setRendererHint: string;
}

export type StudioLocale = "en" | "es" | "fr" | "de" | "nl";

export type StudioTranslateFn = (
  key: keyof StudioTranslationStrings,
  vars?: Record<string, string | number>
) => string;

export interface StudioI18nConfig {
  locale?: StudioLocale;
  translations?: Partial<StudioTranslationStrings>;
}

// ============================================================================
// Default translations (English)
// ============================================================================

export const DEFAULT_STUDIO_TRANSLATIONS: StudioTranslationStrings = {
  // Status
  idle: "Idle",
  requestingPermissions: "Permissions...",
  ready: "Ready",
  connecting: "Connecting...",
  live: "Live",
  reconnecting: "Reconnecting...",
  reconnectingAttempt: "Reconnecting ({attempt}/{max})...",
  error: "Error",
  destroyed: "Destroyed",
  resolvingEndpoint: "Resolving ingest endpoint...",

  // Primary actions
  goLive: "Go Live",
  stopStreaming: "Stop Streaming",

  // Secondary actions
  addCamera: "Add Camera",
  cameraActive: "Camera active",
  shareScreen: "Share Screen",
  settings: "Settings",

  // Header
  streamCrafter: "StreamCrafter",

  // Quality profiles
  professional: "Professional",
  professionalDesc: "1080p @ 8 Mbps",
  broadcast: "Broadcast",
  broadcastDesc: "1080p @ 4.5 Mbps",
  conference: "Conference",
  conferenceDesc: "720p @ 2.5 Mbps",

  // Sources
  primary: "PRIMARY",
  camera: "Camera",
  screen: "Screen",
  custom: "Custom",
  mute: "Mute",
  unmute: "Unmute",
  primaryVideoSource: "Primary video source",
  setAsPrimary: "Set as primary video",
  removeSource: "Remove source",
  cannotRemoveWhileStreaming: "Cannot remove source while streaming",
  hideFromComposition: "Hide from composition",
  showInComposition: "Show in composition",

  // Preview
  addSourcePrompt: "Add a camera or screen to preview",
  streamPreview: "Stream preview",

  // Mixer
  mixer: "Mixer",
  sources: "Sources",
  collapseMixer: "Collapse Mixer",
  expandMixer: "Expand Mixer",

  // Context menu
  copyWhipUrl: "Copy WHIP URL",
  copyStreamInfo: "Copy Stream Info",
  hideAdvanced: "Hide Advanced",
  advanced: "Advanced",

  // Advanced panel tabs
  audio: "Audio",
  stats: "Stats",
  info: "Info",
  comp: "Comp",
  closeAdvancedPanel: "Close advanced panel",

  // Audio section
  masterVolume: "Master Volume",
  outputLevel: "Output Level",
  audioMixing: "Audio Mixing",
  on: "ON",
  off: "OFF",
  compressorLimiterActive: "Compressor + Limiter active",
  processing: "Processing",

  // Audio processing
  echoCancellation: "Echo Cancellation",
  echoCancellationDesc: "Reduce echo from speakers",
  noiseSuppression: "Noise Suppression",
  noiseSuppressionDesc: "Filter background noise",
  autoGainControl: "Auto Gain Control",
  autoGainControlDesc: "Normalize audio levels",
  modified: "Modified",
  sampleRate: "Sample Rate",
  channels: "Channels",

  // Stats section
  connection: "Connection",
  bitrate: "Bitrate",
  video: "Video",
  frameRate: "Frame Rate",
  framesEncoded: "Frames Encoded",
  packetsLost: "Packets Lost",
  rtt: "RTT",
  iceState: "ICE State",
  waitingForStats: "Waiting for stats...",
  startStreamingForStats: "Start streaming to see stats",

  // Info section
  qualityProfile: "Quality Profile",
  whipEndpoint: "WHIP Endpoint",
  notConfigured: "Not configured",
  copyUrl: "Copy URL",
  encoder: "Encoder",
  resetToProfile: "Reset to Profile",
  videoCodec: "Video Codec",
  resolution: "Resolution",
  actualResolution: "Actual Resolution",
  framerate: "Framerate",
  actualFramerate: "Actual Framerate",
  videoBitrate: "Video Bitrate",
  audioCodec: "Audio Codec",
  audioBitrate: "Audio Bitrate",
  settingsLockedWhileStreaming: "Settings locked while streaming",
  noSourcesAdded: "No sources added",

  // Encoder
  webCodecs: "WebCodecs",
  browser: "Browser",
  useWebCodecs: "Use WebCodecs",
  enableWebCodecsDesc: "Enable advanced WebCodecs encoder",
  webCodecsUnsupported: "Not available \u2014 RTCRtpScriptTransform unsupported",
  changeTakesEffect: "Change takes effect on next stream",

  // Compositor
  compositor: "Compositor",
  layout: "Layout",
  display: "Display",
  renderer: "Renderer",
  notInitialized: "Not initialized",
  performance: "Performance",
  frameTime: "Frame Time",
  gpuMemory: "GPU Memory",
  composition: "Composition",
  scenes: "Scenes",
  layers: "Layers",
  enableCompositor: "Enable Compositor",

  // Layouts
  solo: "Solo",
  grid: "Grid",
  stack: "Stack",

  // Scaling
  letterboxFit: "Letterbox (fit)",
  cropFill: "Crop (fill)",
  stretch: "Stretch",

  // Layers
  noLayers: "No layers. Add a source to get started.",
  hideLayer: "Hide layer",
  showLayer: "Show layer",
  moveUp: "Move up",
  moveDown: "Move down",
  editOpacity: "Edit opacity",
  removeLayer: "Remove layer",

  // Scenes
  deleteScene: "Delete scene",
  createNewScene: "Create new scene",

  // Transitions
  cut: "Cut",
  fade: "Fade",
  slideLeft: "Slide Left",
  slideRight: "Slide Right",
  slideUp: "Slide Up",
  slideDown: "Slide Down",

  // Debug
  state: "State",
  active: "Active",
  inactive: "Inactive",
  whip: "WHIP",
  ok: "OK",
  notSet: "Not set",

  // Errors
  configureWhipEndpoint: "Configure WHIP endpoint to stream",

  // Missing UI strings
  quality: "Quality",
  debug: "Debug",
  type: "Type",
  warning: "Warning",
  encoderStats: "Encoder Stats",
  videoFrames: "Video Frames",
  videoPending: "Video Pending",
  videoBytes: "Video Bytes",
  audioSamples: "Audio Samples",
  audioBytes: "Audio Bytes",
  transitionDuration: "Transition duration (ms)",
  setRendererHint: "Set renderer in config before starting",
};

// ============================================================================
// Locale packs
// ============================================================================

const LOCALE_ES: StudioTranslationStrings = {
  ...DEFAULT_STUDIO_TRANSLATIONS,
  idle: "Inactivo",
  requestingPermissions: "Permisos...",
  ready: "Listo",
  connecting: "Conectando...",
  live: "En vivo",
  reconnecting: "Reconectando...",
  reconnectingAttempt: "Reconectando ({attempt}/{max})...",
  error: "Error",
  destroyed: "Destruido",
  resolvingEndpoint: "Resolviendo endpoint...",
  goLive: "Transmitir",
  stopStreaming: "Detener",
  addCamera: "Agregar c\u00e1mara",
  cameraActive: "C\u00e1mara activa",
  shareScreen: "Compartir pantalla",
  settings: "Configuraci\u00f3n",
  streamCrafter: "StreamCrafter",
  professional: "Profesional",
  broadcast: "Transmisi\u00f3n",
  conference: "Conferencia",
  primary: "PRINCIPAL",
  camera: "C\u00e1mara",
  screen: "Pantalla",
  custom: "Personalizado",
  mute: "Silenciar",
  unmute: "Activar sonido",
  removeSource: "Eliminar fuente",
  addSourcePrompt: "Agrega una c\u00e1mara o pantalla para vista previa",
  streamPreview: "Vista previa",
  mixer: "Mezclador",
  sources: "Fuentes",
  audio: "Audio",
  stats: "Estad\u00edsticas",
  info: "Info",
  masterVolume: "Volumen maestro",
  outputLevel: "Nivel de salida",
  echoCancellation: "Cancelaci\u00f3n de eco",
  noiseSuppression: "Supresi\u00f3n de ruido",
  autoGainControl: "Control de ganancia",
  connection: "Conexi\u00f3n",
  bitrate: "Tasa de bits",
  video: "V\u00eddeo",
  frameRate: "Fotogramas",
  qualityProfile: "Perfil de calidad",
  encoder: "Codificador",
  compositor: "Compositor",
  layout: "Disposici\u00f3n",
  layers: "Capas",
  scenes: "Escenas",
  cut: "Corte",
  fade: "Fundido",
  quality: "Calidad",
  debug: "Depuración",
  type: "Tipo",
  warning: "Advertencia",
  encoderStats: "Estadísticas del codificador",
  videoFrames: "Cuadros de vídeo",
  videoPending: "Vídeo pendiente",
  videoBytes: "Bytes de vídeo",
  audioSamples: "Muestras de audio",
  audioBytes: "Bytes de audio",
  transitionDuration: "Duración de transición (ms)",
  setRendererHint: "Configure el renderizador antes de iniciar",
};

const LOCALE_FR: StudioTranslationStrings = {
  ...DEFAULT_STUDIO_TRANSLATIONS,
  idle: "Inactif",
  requestingPermissions: "Permissions...",
  ready: "Pr\u00eat",
  connecting: "Connexion...",
  live: "En direct",
  reconnecting: "Reconnexion...",
  reconnectingAttempt: "Reconnexion ({attempt}/{max})...",
  error: "Erreur",
  goLive: "Lancer le direct",
  stopStreaming: "Arr\u00eater",
  addCamera: "Ajouter cam\u00e9ra",
  cameraActive: "Cam\u00e9ra active",
  shareScreen: "\u00c9cran partag\u00e9",
  settings: "Param\u00e8tres",
  professional: "Professionnel",
  broadcast: "Diffusion",
  conference: "Conf\u00e9rence",
  primary: "PRINCIPAL",
  camera: "Cam\u00e9ra",
  screen: "\u00c9cran",
  custom: "Personnalis\u00e9",
  mute: "Couper le son",
  unmute: "R\u00e9activer le son",
  removeSource: "Supprimer la source",
  addSourcePrompt: "Ajoutez une cam\u00e9ra ou un \u00e9cran",
  streamPreview: "Aper\u00e7u du flux",
  mixer: "Mixeur",
  sources: "Sources",
  audio: "Audio",
  stats: "Statistiques",
  info: "Info",
  masterVolume: "Volume principal",
  outputLevel: "Niveau de sortie",
  echoCancellation: "Annulation d'\u00e9cho",
  noiseSuppression: "Suppression du bruit",
  autoGainControl: "Contr\u00f4le de gain",
  connection: "Connexion",
  bitrate: "D\u00e9bit",
  video: "Vid\u00e9o",
  frameRate: "Images/s",
  qualityProfile: "Profil de qualit\u00e9",
  encoder: "Encodeur",
  compositor: "Compositeur",
  layout: "Disposition",
  layers: "Calques",
  scenes: "Sc\u00e8nes",
  cut: "Coupe",
  fade: "Fondu",
  quality: "Qualité",
  debug: "Débogage",
  type: "Type",
  warning: "Avertissement",
  encoderStats: "Statistiques de l'encodeur",
  videoFrames: "Images vidéo",
  videoPending: "Vidéo en attente",
  videoBytes: "Octets vidéo",
  audioSamples: "Échantillons audio",
  audioBytes: "Octets audio",
  transitionDuration: "Durée de transition (ms)",
  setRendererHint: "Configurer le moteur de rendu avant de démarrer",
};

const LOCALE_DE: StudioTranslationStrings = {
  ...DEFAULT_STUDIO_TRANSLATIONS,
  idle: "Bereit",
  requestingPermissions: "Berechtigungen...",
  ready: "Fertig",
  connecting: "Verbinden...",
  live: "Live",
  reconnecting: "Wiederverbinden...",
  reconnectingAttempt: "Wiederverbinden ({attempt}/{max})...",
  error: "Fehler",
  goLive: "Live gehen",
  stopStreaming: "Stoppen",
  addCamera: "Kamera hinzuf\u00fcgen",
  cameraActive: "Kamera aktiv",
  shareScreen: "Bildschirm teilen",
  settings: "Einstellungen",
  professional: "Professionell",
  broadcast: "\u00dcbertragung",
  conference: "Konferenz",
  primary: "PRIM\u00c4R",
  camera: "Kamera",
  screen: "Bildschirm",
  custom: "Benutzerdefiniert",
  mute: "Stummschalten",
  unmute: "Ton aktivieren",
  removeSource: "Quelle entfernen",
  addSourcePrompt: "Kamera oder Bildschirm hinzuf\u00fcgen",
  streamPreview: "Stream-Vorschau",
  mixer: "Mixer",
  sources: "Quellen",
  audio: "Audio",
  stats: "Statistiken",
  info: "Info",
  masterVolume: "Hauptlautst\u00e4rke",
  outputLevel: "Ausgabepegel",
  echoCancellation: "Echounterdr\u00fcckung",
  noiseSuppression: "Rauschunterdr\u00fcckung",
  autoGainControl: "Verst\u00e4rkungsregelung",
  connection: "Verbindung",
  bitrate: "Bitrate",
  video: "Video",
  frameRate: "Bildrate",
  qualityProfile: "Qualit\u00e4tsprofil",
  encoder: "Encoder",
  compositor: "Kompositor",
  layout: "Layout",
  layers: "Ebenen",
  scenes: "Szenen",
  cut: "Schnitt",
  fade: "\u00dcberblendung",
  quality: "Qualit\u00e4t",
  debug: "Debug",
  type: "Typ",
  warning: "Warnung",
  encoderStats: "Encoder-Statistiken",
  videoFrames: "Videobilder",
  videoPending: "Video ausstehend",
  videoBytes: "Video-Bytes",
  audioSamples: "Audio-Samples",
  audioBytes: "Audio-Bytes",
  transitionDuration: "\u00dcbergangsdauer (ms)",
  setRendererHint: "Renderer vor dem Start konfigurieren",
};

const LOCALE_NL: StudioTranslationStrings = {
  ...DEFAULT_STUDIO_TRANSLATIONS,
  idle: "Inactief",
  requestingPermissions: "Toestemmingen...",
  ready: "Gereed",
  connecting: "Verbinden...",
  live: "Live",
  reconnecting: "Opnieuw verbinden...",
  reconnectingAttempt: "Opnieuw verbinden ({attempt}/{max})...",
  error: "Fout",
  goLive: "Live gaan",
  stopStreaming: "Stoppen",
  addCamera: "Camera toevoegen",
  cameraActive: "Camera actief",
  shareScreen: "Scherm delen",
  settings: "Instellingen",
  professional: "Professioneel",
  broadcast: "Uitzending",
  conference: "Conferentie",
  primary: "PRIMAIR",
  camera: "Camera",
  screen: "Scherm",
  custom: "Aangepast",
  mute: "Dempen",
  unmute: "Dempen opheffen",
  removeSource: "Bron verwijderen",
  addSourcePrompt: "Voeg een camera of scherm toe",
  streamPreview: "Streamvoorbeeld",
  mixer: "Mixer",
  sources: "Bronnen",
  audio: "Audio",
  stats: "Statistieken",
  info: "Info",
  masterVolume: "Hoofdvolume",
  outputLevel: "Uitvoerniveau",
  echoCancellation: "Echoonderdrukking",
  noiseSuppression: "Ruisonderdrukking",
  autoGainControl: "Automatische versterking",
  connection: "Verbinding",
  bitrate: "Bitrate",
  video: "Video",
  frameRate: "Framerate",
  qualityProfile: "Kwaliteitsprofiel",
  encoder: "Encoder",
  compositor: "Compositor",
  layout: "Indeling",
  layers: "Lagen",
  scenes: "Sc\u00e8nes",
  cut: "Snede",
  fade: "Vervaging",
  quality: "Kwaliteit",
  debug: "Debug",
  type: "Type",
  warning: "Waarschuwing",
  encoderStats: "Encoder-statistieken",
  videoFrames: "Videoframes",
  videoPending: "Video in wachtrij",
  videoBytes: "Videobytes",
  audioSamples: "Audiosamples",
  audioBytes: "Audiobytes",
  transitionDuration: "Overgangsduur (ms)",
  setRendererHint: "Configureer renderer voordat u begint",
};

const STUDIO_LOCALE_PACKS: Record<StudioLocale, StudioTranslationStrings> = {
  en: DEFAULT_STUDIO_TRANSLATIONS,
  es: LOCALE_ES,
  fr: LOCALE_FR,
  de: LOCALE_DE,
  nl: LOCALE_NL,
};

// ============================================================================
// Public API
// ============================================================================

/** Get the full translation strings for a locale. */
export function getStudioLocalePack(locale: StudioLocale): StudioTranslationStrings {
  return STUDIO_LOCALE_PACKS[locale] ?? DEFAULT_STUDIO_TRANSLATIONS;
}

/** Get all available studio locale codes. */
export function getAvailableStudioLocales(): StudioLocale[] {
  return Object.keys(STUDIO_LOCALE_PACKS) as StudioLocale[];
}

/**
 * Create a translator function bound to specific translations.
 * Merges custom overrides on top of the locale pack.
 */
export function createStudioTranslator(
  config?: StudioI18nConfig
): (key: keyof StudioTranslationStrings, vars?: Record<string, string | number>) => string {
  const base = config?.locale ? getStudioLocalePack(config.locale) : DEFAULT_STUDIO_TRANSLATIONS;
  const merged = config?.translations ? { ...base, ...config.translations } : base;

  return (key, vars) => {
    let str = merged[key] ?? DEFAULT_STUDIO_TRANSLATIONS[key] ?? key;
    if (vars) {
      for (const [k, v] of Object.entries(vars)) {
        str = str.replace(`{${k}}`, String(v));
      }
    }
    return str;
  };
}

/** One-shot translate with explicit translations object. */
export function studioTranslate(
  translations: Partial<StudioTranslationStrings>,
  key: keyof StudioTranslationStrings,
  vars?: Record<string, string | number>
): string {
  let str = translations[key] ?? DEFAULT_STUDIO_TRANSLATIONS[key] ?? key;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      str = str.replace(`{${k}}`, String(v));
    }
  }
  return str;
}
