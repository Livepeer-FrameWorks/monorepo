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
  sceneName: string;
  layerCountOne: string;
  layerCountMany: string;

  // Transitions
  transitionType: string;
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
  browserCannotCapture: string;
  browserCannotPublish: string;
  screenShareUnsupported: string;
  mediaFileUnsupported: string;
  compositorUnavailable: string;
  cameraListError: string;

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

  // Camera picker
  chooseCamera: string;

  // Overlays
  overlays: string;
  addLogo: string;
  addText: string;
  overlayDefaultText: string;
  overlayText: string;
  overlaySize: string;
  overlayFontSize: string;
  overlayColor: string;
  overlayPositionX: string;
  overlayPositionY: string;
  removeOverlay: string;
  noOverlays: string;
  overlayImageError: string;
  overlayLocalNotice: string;

  // Media files
  media: string;
  addMediaFile: string;
  playMedia: string;
  pauseMedia: string;
  loopMedia: string;
  noMedia: string;
  mediaFileError: string;
  mediaTabNotice: string;

  // Audio section
  browserProcessing: string;
  noiseGate: string;
  noiseGateThreshold: string;
  deEsser: string;
  equalizer: string;
  eqLow: string;
  eqMid: string;
  eqHigh: string;
  ducking: string;

  // Connection health
  connectionGood: string;
  connectionFair: string;
  connectionPoor: string;
  connectionDetails: string;
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
  copyWhipUrl: "Copy WHIP URL (contains the stream key)",
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
  sceneName: "Scene {number}",
  layerCountOne: "1 layer",
  layerCountMany: "{count} layers",

  // Transitions
  transitionType: "Transition",
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
  browserCannotCapture:
    "This browser can't capture a camera or screen here. Open this page over HTTPS in a current browser.",
  browserCannotPublish:
    "This browser can't publish a stream because WebRTC is not available. Use a current browser.",
  screenShareUnsupported: "This browser can't share a screen",
  mediaFileUnsupported: "This browser can't play a media file into the stream.",
  compositorUnavailable:
    "Scenes and layers are off because this browser could not start the compositor. You can still go live with one source.",
  cameraListError: "Could not list the cameras",

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

  // Camera picker
  chooseCamera: "Choose a camera",

  // Overlays
  overlays: "Overlays",
  addLogo: "Add logo",
  addText: "Add text",
  overlayDefaultText: "Your text",
  overlayText: "Text",
  overlaySize: "Size",
  overlayFontSize: "Text size",
  overlayColor: "Text color",
  overlayPositionX: "Left to right",
  overlayPositionY: "Top to bottom",
  removeOverlay: "Remove overlay",
  noOverlays: "No overlays in this scene.",
  overlayImageError: "This image could not be read.",
  overlayLocalNotice: "The image file stays in this browser. Only the finished mix is sent.",

  // Media files
  media: "Media",
  addMediaFile: "Add media file",
  playMedia: "Play",
  pauseMedia: "Pause",
  loopMedia: "Loop",
  noMedia: "No media files in the mix.",
  mediaFileError: "This file could not be played in this browser.",
  mediaTabNotice:
    "Files play from this tab and are not uploaded. Keep this tab open while a file is in the mix.",

  // Audio section
  browserProcessing: "Browser processing",
  noiseGate: "Noise gate",
  noiseGateThreshold: "Gate threshold",
  deEsser: "De-esser",
  equalizer: "EQ",
  eqLow: "Low",
  eqMid: "Mid",
  eqHigh: "High",
  ducking: "Lower other sources while the camera microphone picks up sound",

  // Connection health
  connectionGood: "Connection good",
  connectionFair: "Connection unstable",
  connectionPoor: "Connection poor",
  connectionDetails: "{bitrate} kbps, {loss}% packet loss, {rtt} ms round trip",
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
  noLayers: "Sin capas. Agrega una fuente para empezar.",
  hideLayer: "Ocultar capa",
  showLayer: "Mostrar capa",
  moveUp: "Subir",
  moveDown: "Bajar",
  deleteScene: "Eliminar escena",
  createNewScene: "Crear escena",
  sceneName: "Escena {number}",
  layerCountOne: "1 capa",
  layerCountMany: "{count} capas",
  transitionType: "Transición",
  slideLeft: "Deslizar a la izquierda",
  slideRight: "Deslizar a la derecha",
  slideUp: "Deslizar hacia arriba",
  slideDown: "Deslizar hacia abajo",
  chooseCamera: "Elegir una cámara",
  overlays: "Superposiciones",
  addLogo: "Agregar logotipo",
  addText: "Agregar texto",
  overlayDefaultText: "Tu texto",
  overlayText: "Texto",
  overlaySize: "Tamaño",
  overlayFontSize: "Tamaño del texto",
  overlayColor: "Color del texto",
  overlayPositionX: "De izquierda a derecha",
  overlayPositionY: "De arriba a abajo",
  removeOverlay: "Quitar superposición",
  noOverlays: "No hay superposiciones en esta escena.",
  overlayImageError: "No se pudo leer esta imagen.",
  overlayLocalNotice:
    "El archivo de imagen se queda en este navegador. Solo se envía la mezcla final.",
  media: "Multimedia",
  addMediaFile: "Agregar archivo multimedia",
  playMedia: "Reproducir",
  pauseMedia: "Pausar",
  loopMedia: "Repetir",
  noMedia: "No hay archivos multimedia en la mezcla.",
  mediaFileError: "Este archivo no se pudo reproducir en este navegador.",
  mediaTabNotice:
    "Los archivos se reproducen desde esta pestaña y no se suben. Mantén esta pestaña abierta mientras haya un archivo en la mezcla.",
  browserProcessing: "Procesamiento del navegador",
  noiseGate: "Puerta de ruido",
  noiseGateThreshold: "Umbral de la puerta",
  deEsser: "De-esser",
  equalizer: "EQ",
  eqLow: "Graves",
  eqMid: "Medios",
  eqHigh: "Agudos",
  ducking: "Bajar las demás fuentes mientras el micrófono de la cámara capta sonido",
  connectionGood: "Conexión buena",
  connectionFair: "Conexión inestable",
  connectionPoor: "Conexión mala",
  connectionDetails: "{bitrate} kbps, {loss}% de pérdida de paquetes, {rtt} ms de ida y vuelta",
  browserCannotCapture:
    "Este navegador no puede capturar una cámara ni una pantalla aquí. Abre esta página por HTTPS en un navegador actual.",
  browserCannotPublish:
    "Este navegador no puede publicar una transmisión porque WebRTC no está disponible. Usa un navegador actual.",
  screenShareUnsupported: "Este navegador no puede compartir la pantalla",
  mediaFileUnsupported:
    "Este navegador no puede reproducir un archivo multimedia en la transmisión.",
  compositorUnavailable:
    "Las escenas y capas están desactivadas porque este navegador no pudo iniciar el compositor. Aún puedes transmitir con una fuente.",
  cameraListError: "No se pudieron listar las cámaras",
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
  noLayers: "Aucun calque. Ajoutez une source pour commencer.",
  hideLayer: "Masquer le calque",
  showLayer: "Afficher le calque",
  moveUp: "Monter",
  moveDown: "Descendre",
  deleteScene: "Supprimer la scène",
  createNewScene: "Créer une scène",
  sceneName: "Scène {number}",
  layerCountOne: "1 calque",
  layerCountMany: "{count} calques",
  transitionType: "Transition",
  slideLeft: "Glisser vers la gauche",
  slideRight: "Glisser vers la droite",
  slideUp: "Glisser vers le haut",
  slideDown: "Glisser vers le bas",
  chooseCamera: "Choisir une caméra",
  overlays: "Incrustations",
  addLogo: "Ajouter un logo",
  addText: "Ajouter du texte",
  overlayDefaultText: "Votre texte",
  overlayText: "Texte",
  overlaySize: "Taille",
  overlayFontSize: "Taille du texte",
  overlayColor: "Couleur du texte",
  overlayPositionX: "De gauche à droite",
  overlayPositionY: "De haut en bas",
  removeOverlay: "Retirer l'incrustation",
  noOverlays: "Aucune incrustation dans cette scène.",
  overlayImageError: "Impossible de lire cette image.",
  overlayLocalNotice: "Le fichier image reste dans ce navigateur. Seul le mixage final est envoyé.",
  media: "Médias",
  addMediaFile: "Ajouter un fichier média",
  playMedia: "Lire",
  pauseMedia: "Pause",
  loopMedia: "Boucle",
  noMedia: "Aucun fichier média dans le mixage.",
  mediaFileError: "Ce fichier ne peut pas être lu dans ce navigateur.",
  mediaTabNotice:
    "Les fichiers sont lus depuis cet onglet et ne sont pas envoyés. Gardez cet onglet ouvert tant qu'un fichier est dans le mixage.",
  browserProcessing: "Traitement du navigateur",
  noiseGate: "Noise gate",
  noiseGateThreshold: "Seuil du gate",
  deEsser: "De-esser",
  equalizer: "EQ",
  eqLow: "Graves",
  eqMid: "Médiums",
  eqHigh: "Aigus",
  ducking: "Baisser les autres sources quand le micro de la caméra capte du son",
  connectionGood: "Connexion bonne",
  connectionFair: "Connexion instable",
  connectionPoor: "Connexion mauvaise",
  connectionDetails: "{bitrate} kbps, {loss} % de perte de paquets, {rtt} ms aller-retour",
  browserCannotCapture:
    "Ce navigateur ne peut pas capturer une caméra ou un écran ici. Ouvrez cette page en HTTPS dans un navigateur récent.",
  browserCannotPublish:
    "Ce navigateur ne peut pas publier de flux car WebRTC n'est pas disponible. Utilisez un navigateur récent.",
  screenShareUnsupported: "Ce navigateur ne peut pas partager l'écran",
  mediaFileUnsupported: "Ce navigateur ne peut pas lire un fichier média dans le flux.",
  compositorUnavailable:
    "Les scènes et les calques sont désactivés car ce navigateur n'a pas pu démarrer le compositeur. Vous pouvez tout de même diffuser avec une source.",
  cameraListError: "Impossible de lister les caméras",
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
  noLayers: "Keine Ebenen. Füge eine Quelle hinzu.",
  hideLayer: "Ebene ausblenden",
  showLayer: "Ebene einblenden",
  moveUp: "Nach oben",
  moveDown: "Nach unten",
  deleteScene: "Szene löschen",
  createNewScene: "Neue Szene erstellen",
  sceneName: "Szene {number}",
  layerCountOne: "1 Ebene",
  layerCountMany: "{count} Ebenen",
  transitionType: "Übergang",
  slideLeft: "Nach links schieben",
  slideRight: "Nach rechts schieben",
  slideUp: "Nach oben schieben",
  slideDown: "Nach unten schieben",
  chooseCamera: "Kamera auswählen",
  overlays: "Einblendungen",
  addLogo: "Logo hinzufügen",
  addText: "Text hinzufügen",
  overlayDefaultText: "Ihr Text",
  overlayText: "Text",
  overlaySize: "Größe",
  overlayFontSize: "Textgröße",
  overlayColor: "Textfarbe",
  overlayPositionX: "Von links nach rechts",
  overlayPositionY: "Von oben nach unten",
  removeOverlay: "Einblendung entfernen",
  noOverlays: "Keine Einblendungen in dieser Szene.",
  overlayImageError: "Dieses Bild konnte nicht gelesen werden.",
  overlayLocalNotice:
    "Die Bilddatei bleibt in diesem Browser. Gesendet wird nur die fertige Mischung.",
  media: "Medien",
  addMediaFile: "Mediendatei hinzufügen",
  playMedia: "Abspielen",
  pauseMedia: "Pause",
  loopMedia: "Wiederholen",
  noMedia: "Keine Mediendateien in der Mischung.",
  mediaFileError: "Diese Datei kann in diesem Browser nicht abgespielt werden.",
  mediaTabNotice:
    "Dateien werden aus diesem Tab abgespielt und nicht hochgeladen. Lassen Sie diesen Tab geöffnet, solange eine Datei in der Mischung ist.",
  browserProcessing: "Browser-Verarbeitung",
  noiseGate: "Noise Gate",
  noiseGateThreshold: "Gate-Schwelle",
  deEsser: "De-Esser",
  equalizer: "EQ",
  eqLow: "Tiefen",
  eqMid: "Mitten",
  eqHigh: "Höhen",
  ducking: "Andere Quellen absenken, solange das Kameramikrofon Ton aufnimmt",
  connectionGood: "Verbindung gut",
  connectionFair: "Verbindung instabil",
  connectionPoor: "Verbindung schlecht",
  connectionDetails: "{bitrate} kbps, {loss} % Paketverlust, {rtt} ms Umlaufzeit",
  browserCannotCapture:
    "Dieser Browser kann hier weder Kamera noch Bildschirm aufnehmen. Öffnen Sie diese Seite über HTTPS in einem aktuellen Browser.",
  browserCannotPublish:
    "Dieser Browser kann keinen Stream senden, weil WebRTC nicht verfügbar ist. Verwenden Sie einen aktuellen Browser.",
  screenShareUnsupported: "Dieser Browser kann den Bildschirm nicht teilen",
  mediaFileUnsupported: "Dieser Browser kann keine Mediendatei in den Stream abspielen.",
  compositorUnavailable:
    "Szenen und Ebenen sind deaktiviert, weil dieser Browser den Compositor nicht starten konnte. Sie können trotzdem mit einer Quelle live gehen.",
  cameraListError: "Die Kameras konnten nicht aufgelistet werden",
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
  noLayers: "Geen lagen. Voeg een bron toe om te beginnen.",
  hideLayer: "Laag verbergen",
  showLayer: "Laag tonen",
  moveUp: "Omhoog",
  moveDown: "Omlaag",
  deleteScene: "Scène verwijderen",
  createNewScene: "Nieuwe scène maken",
  sceneName: "Scène {number}",
  layerCountOne: "1 laag",
  layerCountMany: "{count} lagen",
  transitionType: "Overgang",
  slideLeft: "Naar links schuiven",
  slideRight: "Naar rechts schuiven",
  slideUp: "Omhoog schuiven",
  slideDown: "Omlaag schuiven",
  chooseCamera: "Kies een camera",
  overlays: "Overlays",
  addLogo: "Logo toevoegen",
  addText: "Tekst toevoegen",
  overlayDefaultText: "Uw tekst",
  overlayText: "Tekst",
  overlaySize: "Grootte",
  overlayFontSize: "Tekstgrootte",
  overlayColor: "Tekstkleur",
  overlayPositionX: "Van links naar rechts",
  overlayPositionY: "Van boven naar beneden",
  removeOverlay: "Overlay verwijderen",
  noOverlays: "Geen overlays in deze scène.",
  overlayImageError: "Deze afbeelding kon niet worden gelezen.",
  overlayLocalNotice:
    "Het afbeeldingsbestand blijft in deze browser. Alleen de eindmix wordt verzonden.",
  media: "Media",
  addMediaFile: "Mediabestand toevoegen",
  playMedia: "Afspelen",
  pauseMedia: "Pauzeren",
  loopMedia: "Herhalen",
  noMedia: "Geen mediabestanden in de mix.",
  mediaFileError: "Dit bestand kan niet worden afgespeeld in deze browser.",
  mediaTabNotice:
    "Bestanden worden vanuit dit tabblad afgespeeld en niet geüpload. Houd dit tabblad open zolang er een bestand in de mix zit.",
  browserProcessing: "Browserverwerking",
  noiseGate: "Noise gate",
  noiseGateThreshold: "Drempel van de gate",
  deEsser: "De-esser",
  equalizer: "EQ",
  eqLow: "Laag",
  eqMid: "Midden",
  eqHigh: "Hoog",
  ducking: "Andere bronnen zachter zetten zolang de cameramicrofoon geluid opvangt",
  connectionGood: "Verbinding goed",
  connectionFair: "Verbinding instabiel",
  connectionPoor: "Verbinding slecht",
  connectionDetails: "{bitrate} kbps, {loss}% pakketverlies, {rtt} ms heen en terug",
  browserCannotCapture:
    "Deze browser kan hier geen camera of scherm opnemen. Open deze pagina via HTTPS in een actuele browser.",
  browserCannotPublish:
    "Deze browser kan geen stream publiceren omdat WebRTC niet beschikbaar is. Gebruik een actuele browser.",
  screenShareUnsupported: "Deze browser kan geen scherm delen",
  mediaFileUnsupported: "Deze browser kan geen mediabestand in de stream afspelen.",
  compositorUnavailable:
    "Scènes en lagen staan uit omdat deze browser de compositor niet kon starten. Je kunt nog steeds live gaan met één bron.",
  cameraListError: "De camera's konden niet worden weergegeven",
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

/** Name for a new scene: the first numbered scene name not already in use. */
export function nextStudioSceneName(
  scenes: readonly { name: string }[],
  t: StudioTranslateFn
): string {
  const names = new Set(scenes.map((scene) => scene.name));
  let number = scenes.length + 1;
  while (names.has(t("sceneName", { number }))) number++;
  return t("sceneName", { number });
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
