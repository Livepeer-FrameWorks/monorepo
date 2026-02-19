/**
 * Internationalization (i18n) — lightweight translation system.
 *
 * Provides `translate(key, fallback?)` lookup with built-in locale packs for
 * common languages. Users can pass custom `translations` to override any key,
 * or set `locale` to use a built-in pack.
 *
 * @example
 * ```typescript
 * // Built-in locale
 * createPlayer({ locale: 'es', ... });
 *
 * // Custom overrides
 * createPlayer({ translations: { play: 'Воспроизвести' }, ... });
 *
 * // Locale as base + overrides on top
 * createPlayer({ locale: 'fr', translations: { play: 'Lancer' }, ... });
 * ```
 */

/**
 * All translatable string keys.
 * Each key maps to a user-facing string used in controls, tooltips, ARIA labels,
 * and overlay messages.
 */
export interface TranslationStrings {
  // Playback controls
  play: string;
  pause: string;
  mute: string;
  unmute: string;
  volume: string;
  fullscreen: string;
  exitFullscreen: string;
  pip: string;
  loop: string;
  settings: string;
  automatic: string;
  none: string;
  live: string;
  currentTime: string;
  totalTime: string;
  seekBar: string;
  seekForward: string;
  seekBackward: string;
  skipForward: string;
  skipBackward: string;
  airplay: string;

  // Keyboard overlay feedback
  speed: string;
  speedDoubled: string;
  muted: string;
  mutedParenthetical: string;
  seekBackwardSeconds: string;
  seekForwardSeconds: string;
  toStart: string;
  toEnd: string;
  frameForward: string;
  frameBackward: string;

  // Error overlay
  reloadVideo: string;
  reloadPlayer: string;
  nextSource: string;
  ignore: string;
  playerError: string;
  chromecastError: string;

  // Casting
  stopCasting: string;
  chromecast: string;
  selectCastDevice: string;
  castingTo: string;

  // Track selection
  currentTrack: string;
  track: string;

  // Quality
  quality: string;
  auto: string;

  // Captions
  captions: string;
  captionsOff: string;

  // Playback speed
  playbackSpeed: string;
  normal: string;

  // Loading / states
  loading: string;
  buffering: string;
  streamOffline: string;
  streamEnded: string;
  noStreamAvailable: string;
  waitingForStream: string;
  waitingForSource: string;
  resolvingEndpoint: string;
  broadcasterGoLive: string;
  streamPreparing: string;
  checkingStatus: string;

  // Error / warning overlays
  retry: string;
  tryNext: string;
  dismiss: string;
  warning: string;
  error: string;
  retryConnection: string;
  unexpectedError: string;
  playbackIssue: string;

  // Context menu / UI
  hideStats: string;
  showStats: string;
  hideSettings: string;
  pictureInPicture: string;
  enableLoop: string;
  disableLoop: string;

  // Settings sections
  mode: string;
  fast: string;
  stable: string;
  language: string;

  // Stats
  playbackScore: string;
  corruptedFrames: string;
  droppedFrames: string;
  totalFrames: string;
  decodedAudio: string;
  decodedVideo: string;
  packetsLost: string;
  packetsReceived: string;
  bytesReceived: string;
  localLatency: string;
  currentBitrate: string;
  framerateIn: string;
  framerateOut: string;
}

// ============================================================================
// Default English strings
// ============================================================================

export const DEFAULT_TRANSLATIONS: TranslationStrings = {
  // Playback controls
  play: "Play",
  pause: "Pause",
  mute: "Mute",
  unmute: "Unmute",
  volume: "Volume",
  fullscreen: "Full screen",
  exitFullscreen: "Exit full screen",
  pip: "Picture in picture",
  loop: "Loop",
  settings: "Settings",
  automatic: "Automatic",
  none: "None",
  live: "Live",
  currentTime: "Current time",
  totalTime: "Total time",
  seekBar: "Seek bar",
  seekForward: "Seek forward",
  seekBackward: "Seek backward",
  skipForward: "+10s",
  skipBackward: "-10s",
  airplay: "AirPlay",

  // Keyboard overlay feedback
  speed: "Speed",
  speedDoubled: "Speed doubled",
  muted: "Muted",
  mutedParenthetical: "(muted)",
  seekBackwardSeconds: "- 10 seconds",
  seekForwardSeconds: "+ 10 seconds",
  toStart: "To start",
  toEnd: "To end",
  frameForward: "Frame +1",
  frameBackward: "Frame -1",

  // Error overlay
  reloadVideo: "Reload video",
  reloadPlayer: "Reload player",
  nextSource: "Next source",
  ignore: "Ignore",
  playerError: "The player has encountered a problem",
  chromecastError: "The Chromecast has encountered a problem",

  // Casting
  stopCasting: "Stop casting",
  chromecast: "Chromecast",
  selectCastDevice: "Select a device to cast to",
  castingTo: "Casting to",

  // Track selection
  currentTrack: "The current",
  track: "Track",

  // Quality
  quality: "Quality",
  auto: "Auto",

  // Captions
  captions: "Captions",
  captionsOff: "Off",

  // Playback speed
  playbackSpeed: "Playback speed",
  normal: "Normal",

  // Loading / states
  loading: "Loading...",
  buffering: "Buffering...",
  streamOffline: "Stream is offline",
  streamEnded: "Stream has ended",
  noStreamAvailable: "No stream available",
  waitingForStream: "Waiting for stream...",
  waitingForSource: "Waiting for source...",
  resolvingEndpoint: "Resolving viewing endpoint...",
  broadcasterGoLive: "The stream will start when the broadcaster goes live",
  streamPreparing: "Please wait while the stream prepares...",
  checkingStatus: "Checking stream status...",

  // Error / warning overlays
  retry: "Retry",
  tryNext: "Try Next",
  dismiss: "Dismiss",
  warning: "Warning",
  error: "Error",
  retryConnection: "Retry Connection",
  unexpectedError: "An unexpected error occurred while loading the player.",
  playbackIssue: "Playback issue",

  // Context menu / UI
  hideStats: "Hide Stats",
  showStats: "Stats",
  hideSettings: "Hide Settings",
  pictureInPicture: "Picture-in-Picture",
  enableLoop: "Enable Loop",
  disableLoop: "Disable Loop",

  // Settings sections
  mode: "Mode",
  fast: "Fast",
  stable: "Stable",
  language: "Language",

  // Stats
  playbackScore: "Playback score",
  corruptedFrames: "Corrupted frames",
  droppedFrames: "Dropped frames",
  totalFrames: "Total frames",
  decodedAudio: "Decoded audio",
  decodedVideo: "Decoded video",
  packetsLost: "Packets lost",
  packetsReceived: "Packets received",
  bytesReceived: "Bytes received",
  localLatency: "Local latency",
  currentBitrate: "Current bitrate",
  framerateIn: "Framerate in",
  framerateOut: "Framerate out",
};

// ============================================================================
// Built-in locale packs
// ============================================================================

export type FwLocale = "en" | "es" | "fr" | "de" | "nl";

const LOCALE_PACKS: Record<FwLocale, TranslationStrings> = {
  en: DEFAULT_TRANSLATIONS,

  es: {
    play: "Reproducir",
    pause: "Pausar",
    mute: "Silenciar",
    unmute: "Activar sonido",
    volume: "Volumen",
    fullscreen: "Pantalla completa",
    exitFullscreen: "Salir de pantalla completa",
    pip: "Imagen en imagen",
    loop: "Repetir",
    settings: "Ajustes",
    automatic: "Automático",
    none: "Ninguno",
    live: "En vivo",
    currentTime: "Tiempo actual",
    totalTime: "Duración total",
    seekBar: "Barra de búsqueda",
    seekForward: "Avanzar",
    seekBackward: "Retroceder",
    skipForward: "+10s",
    skipBackward: "-10s",
    airplay: "AirPlay",
    speed: "Velocidad",
    speedDoubled: "Velocidad duplicada",
    muted: "Silenciado",
    mutedParenthetical: "(silenciado)",
    seekBackwardSeconds: "- 10 segundos",
    seekForwardSeconds: "+ 10 segundos",
    toStart: "Al inicio",
    toEnd: "Al final",
    frameForward: "Cuadro +1",
    frameBackward: "Cuadro -1",
    reloadVideo: "Recargar vídeo",
    reloadPlayer: "Recargar reproductor",
    nextSource: "Siguiente fuente",
    ignore: "Ignorar",
    playerError: "El reproductor ha encontrado un problema",
    chromecastError: "Chromecast ha encontrado un problema",
    stopCasting: "Detener transmisión",
    chromecast: "Chromecast",
    selectCastDevice: "Seleccionar dispositivo",
    castingTo: "Transmitiendo a",
    currentTrack: "Actual",
    track: "Pista",
    quality: "Calidad",
    auto: "Auto",
    captions: "Subtítulos",
    captionsOff: "Desactivados",
    playbackSpeed: "Velocidad de reproducción",
    normal: "Normal",
    loading: "Cargando...",
    buffering: "Almacenando en búfer...",
    streamOffline: "La transmisión está fuera de línea",
    streamEnded: "La transmisión ha terminado",
    noStreamAvailable: "No hay transmisión disponible",
    waitingForStream: "Esperando la transmisión...",
    waitingForSource: "Esperando la fuente...",
    resolvingEndpoint: "Resolviendo punto de conexión...",
    broadcasterGoLive: "La transmisión comenzará cuando el emisor se conecte",
    streamPreparing: "Por favor espere mientras se prepara la transmisión...",
    checkingStatus: "Comprobando el estado de la transmisión...",
    retry: "Reintentar",
    tryNext: "Siguiente",
    dismiss: "Descartar",
    warning: "Advertencia",
    error: "Error",
    retryConnection: "Reintentar conexión",
    unexpectedError: "Se ha producido un error inesperado al cargar el reproductor.",
    playbackIssue: "Problema de reproducción",
    hideStats: "Ocultar estadísticas",
    showStats: "Estadísticas",
    hideSettings: "Ocultar ajustes",
    pictureInPicture: "Imagen en imagen",
    enableLoop: "Activar repetición",
    disableLoop: "Desactivar repetición",
    mode: "Modo",
    fast: "Rápido",
    stable: "Estable",
    language: "Idioma",
    playbackScore: "Puntuación de reproducción",
    corruptedFrames: "Cuadros corruptos",
    droppedFrames: "Cuadros perdidos",
    totalFrames: "Cuadros totales",
    decodedAudio: "Audio decodificado",
    decodedVideo: "Vídeo decodificado",
    packetsLost: "Paquetes perdidos",
    packetsReceived: "Paquetes recibidos",
    bytesReceived: "Bytes recibidos",
    localLatency: "Latencia local",
    currentBitrate: "Tasa de bits actual",
    framerateIn: "FPS entrada",
    framerateOut: "FPS salida",
  },

  fr: {
    play: "Lecture",
    pause: "Pause",
    mute: "Couper le son",
    unmute: "Rétablir le son",
    volume: "Volume",
    fullscreen: "Plein écran",
    exitFullscreen: "Quitter le plein écran",
    pip: "Image dans l'image",
    loop: "Boucle",
    settings: "Paramètres",
    automatic: "Automatique",
    none: "Aucun",
    live: "En direct",
    currentTime: "Temps actuel",
    totalTime: "Durée totale",
    seekBar: "Barre de recherche",
    seekForward: "Avancer",
    seekBackward: "Reculer",
    skipForward: "+10s",
    skipBackward: "-10s",
    airplay: "AirPlay",
    speed: "Vitesse",
    speedDoubled: "Vitesse doublée",
    muted: "Son coupé",
    mutedParenthetical: "(muet)",
    seekBackwardSeconds: "- 10 secondes",
    seekForwardSeconds: "+ 10 secondes",
    toStart: "Au début",
    toEnd: "À la fin",
    frameForward: "Image +1",
    frameBackward: "Image -1",
    reloadVideo: "Recharger la vidéo",
    reloadPlayer: "Recharger le lecteur",
    nextSource: "Source suivante",
    ignore: "Ignorer",
    playerError: "Le lecteur a rencontré un problème",
    chromecastError: "Le Chromecast a rencontré un problème",
    stopCasting: "Arrêter la diffusion",
    chromecast: "Chromecast",
    selectCastDevice: "Sélectionner un appareil",
    castingTo: "Diffusion vers",
    currentTrack: "Actuelle",
    track: "Piste",
    quality: "Qualité",
    auto: "Auto",
    captions: "Sous-titres",
    captionsOff: "Désactivés",
    playbackSpeed: "Vitesse de lecture",
    normal: "Normale",
    loading: "Chargement...",
    buffering: "Mise en mémoire tampon...",
    streamOffline: "Le flux est hors ligne",
    streamEnded: "Le flux est terminé",
    noStreamAvailable: "Aucun flux disponible",
    waitingForStream: "En attente du flux...",
    waitingForSource: "En attente de la source...",
    resolvingEndpoint: "Résolution du point d'accès...",
    broadcasterGoLive: "Le flux commencera lorsque le diffuseur sera en ligne",
    streamPreparing: "Veuillez patienter pendant la préparation du flux...",
    checkingStatus: "Vérification de l'état du flux...",
    retry: "Réessayer",
    tryNext: "Suivant",
    dismiss: "Fermer",
    warning: "Avertissement",
    error: "Erreur",
    retryConnection: "Réessayer la connexion",
    unexpectedError: "Une erreur inattendue s'est produite lors du chargement du lecteur.",
    playbackIssue: "Problème de lecture",
    hideStats: "Masquer les statistiques",
    showStats: "Statistiques",
    hideSettings: "Masquer les paramètres",
    pictureInPicture: "Image dans l'image",
    enableLoop: "Activer la boucle",
    disableLoop: "Désactiver la boucle",
    mode: "Mode",
    fast: "Rapide",
    stable: "Stable",
    language: "Langue",
    playbackScore: "Score de lecture",
    corruptedFrames: "Images corrompues",
    droppedFrames: "Images perdues",
    totalFrames: "Images totales",
    decodedAudio: "Audio décodé",
    decodedVideo: "Vidéo décodée",
    packetsLost: "Paquets perdus",
    packetsReceived: "Paquets reçus",
    bytesReceived: "Octets reçus",
    localLatency: "Latence locale",
    currentBitrate: "Débit actuel",
    framerateIn: "IPS entrée",
    framerateOut: "IPS sortie",
  },

  de: {
    play: "Wiedergabe",
    pause: "Pause",
    mute: "Stummschalten",
    unmute: "Ton einschalten",
    volume: "Lautstärke",
    fullscreen: "Vollbild",
    exitFullscreen: "Vollbild beenden",
    pip: "Bild im Bild",
    loop: "Wiederholen",
    settings: "Einstellungen",
    automatic: "Automatisch",
    none: "Keine",
    live: "Live",
    currentTime: "Aktuelle Zeit",
    totalTime: "Gesamtdauer",
    seekBar: "Suchleiste",
    seekForward: "Vorspulen",
    seekBackward: "Zurückspulen",
    skipForward: "+10s",
    skipBackward: "-10s",
    airplay: "AirPlay",
    speed: "Geschwindigkeit",
    speedDoubled: "Geschwindigkeit verdoppelt",
    muted: "Stumm",
    mutedParenthetical: "(stumm)",
    seekBackwardSeconds: "- 10 Sekunden",
    seekForwardSeconds: "+ 10 Sekunden",
    toStart: "Zum Anfang",
    toEnd: "Zum Ende",
    frameForward: "Bild +1",
    frameBackward: "Bild -1",
    reloadVideo: "Video neu laden",
    reloadPlayer: "Player neu laden",
    nextSource: "Nächste Quelle",
    ignore: "Ignorieren",
    playerError: "Der Player hat ein Problem festgestellt",
    chromecastError: "Chromecast hat ein Problem festgestellt",
    stopCasting: "Übertragung beenden",
    chromecast: "Chromecast",
    selectCastDevice: "Gerät auswählen",
    castingTo: "Übertragung an",
    currentTrack: "Aktuelle",
    track: "Spur",
    quality: "Qualität",
    auto: "Auto",
    captions: "Untertitel",
    captionsOff: "Aus",
    playbackSpeed: "Wiedergabegeschwindigkeit",
    normal: "Normal",
    loading: "Wird geladen...",
    buffering: "Puffern...",
    streamOffline: "Stream ist offline",
    streamEnded: "Stream ist beendet",
    noStreamAvailable: "Kein Stream verfügbar",
    waitingForStream: "Warten auf Stream...",
    waitingForSource: "Warten auf Quelle...",
    resolvingEndpoint: "Endpunkt wird aufgelöst...",
    broadcasterGoLive: "Der Stream beginnt, wenn der Sender online geht",
    streamPreparing: "Bitte warten Sie, während der Stream vorbereitet wird...",
    checkingStatus: "Stream-Status wird überprüft...",
    retry: "Erneut versuchen",
    tryNext: "Nächste",
    dismiss: "Schließen",
    warning: "Warnung",
    error: "Fehler",
    retryConnection: "Verbindung erneut versuchen",
    unexpectedError: "Beim Laden des Players ist ein unerwarteter Fehler aufgetreten.",
    playbackIssue: "Wiedergabeproblem",
    hideStats: "Statistiken ausblenden",
    showStats: "Statistiken",
    hideSettings: "Einstellungen ausblenden",
    pictureInPicture: "Bild im Bild",
    enableLoop: "Wiederholung aktivieren",
    disableLoop: "Wiederholung deaktivieren",
    mode: "Modus",
    fast: "Schnell",
    stable: "Stabil",
    language: "Sprache",
    playbackScore: "Wiedergabewertung",
    corruptedFrames: "Beschädigte Bilder",
    droppedFrames: "Verlorene Bilder",
    totalFrames: "Bilder gesamt",
    decodedAudio: "Audio dekodiert",
    decodedVideo: "Video dekodiert",
    packetsLost: "Pakete verloren",
    packetsReceived: "Pakete empfangen",
    bytesReceived: "Bytes empfangen",
    localLatency: "Lokale Latenz",
    currentBitrate: "Aktuelle Bitrate",
    framerateIn: "FPS Eingang",
    framerateOut: "FPS Ausgang",
  },

  nl: {
    play: "Afspelen",
    pause: "Pauzeren",
    mute: "Dempen",
    unmute: "Dempen opheffen",
    volume: "Volume",
    fullscreen: "Volledig scherm",
    exitFullscreen: "Volledig scherm verlaten",
    pip: "Beeld in beeld",
    loop: "Herhalen",
    settings: "Instellingen",
    automatic: "Automatisch",
    none: "Geen",
    live: "Live",
    currentTime: "Huidige tijd",
    totalTime: "Totale duur",
    seekBar: "Zoekbalk",
    seekForward: "Vooruitspoelen",
    seekBackward: "Terugspoelen",
    skipForward: "+10s",
    skipBackward: "-10s",
    airplay: "AirPlay",
    speed: "Snelheid",
    speedDoubled: "Snelheid verdubbeld",
    muted: "Gedempt",
    mutedParenthetical: "(gedempt)",
    seekBackwardSeconds: "- 10 seconden",
    seekForwardSeconds: "+ 10 seconden",
    toStart: "Naar begin",
    toEnd: "Naar einde",
    frameForward: "Frame +1",
    frameBackward: "Frame -1",
    reloadVideo: "Video herladen",
    reloadPlayer: "Speler herladen",
    nextSource: "Volgende bron",
    ignore: "Negeren",
    playerError: "De speler heeft een probleem ondervonden",
    chromecastError: "Chromecast heeft een probleem ondervonden",
    stopCasting: "Casten stoppen",
    chromecast: "Chromecast",
    selectCastDevice: "Apparaat selecteren",
    castingTo: "Casten naar",
    currentTrack: "Huidige",
    track: "Spoor",
    quality: "Kwaliteit",
    auto: "Auto",
    captions: "Ondertiteling",
    captionsOff: "Uit",
    playbackSpeed: "Afspeelsnelheid",
    normal: "Normaal",
    loading: "Laden...",
    buffering: "Bufferen...",
    streamOffline: "Stream is offline",
    streamEnded: "Stream is beëindigd",
    noStreamAvailable: "Geen stream beschikbaar",
    waitingForStream: "Wachten op stream...",
    waitingForSource: "Wachten op bron...",
    resolvingEndpoint: "Eindpunt wordt opgelost...",
    broadcasterGoLive: "De stream begint wanneer de uitzender online gaat",
    streamPreparing: "Even geduld terwijl de stream wordt voorbereid...",
    checkingStatus: "Streamstatus wordt gecontroleerd...",
    retry: "Opnieuw proberen",
    tryNext: "Volgende",
    dismiss: "Sluiten",
    warning: "Waarschuwing",
    error: "Fout",
    retryConnection: "Verbinding opnieuw proberen",
    unexpectedError: "Er is een onverwachte fout opgetreden bij het laden van de speler.",
    playbackIssue: "Afspeelprobleem",
    hideStats: "Statistieken verbergen",
    showStats: "Statistieken",
    hideSettings: "Instellingen verbergen",
    pictureInPicture: "Beeld in beeld",
    enableLoop: "Herhaling inschakelen",
    disableLoop: "Herhaling uitschakelen",
    mode: "Modus",
    fast: "Snel",
    stable: "Stabiel",
    language: "Taal",
    playbackScore: "Afspeelscore",
    corruptedFrames: "Beschadigde frames",
    droppedFrames: "Verloren frames",
    totalFrames: "Totale frames",
    decodedAudio: "Audio gedecodeerd",
    decodedVideo: "Video gedecodeerd",
    packetsLost: "Pakketten verloren",
    packetsReceived: "Pakketten ontvangen",
    bytesReceived: "Bytes ontvangen",
    localLatency: "Lokale latentie",
    currentBitrate: "Huidige bitrate",
    framerateIn: "FPS invoer",
    framerateOut: "FPS uitvoer",
  },
};

// ============================================================================
// Translation Engine
// ============================================================================

export interface I18nConfig {
  /** Built-in locale pack to use as base. Defaults to "en". */
  locale?: FwLocale;
  /** Custom translation overrides (applied on top of the locale pack). */
  translations?: Partial<TranslationStrings>;
}

/**
 * Resolved translation function. Created once from config, then used
 * throughout the player lifecycle.
 */
export type TranslateFn = (key: keyof TranslationStrings, fallback?: string) => string;

const LOCALE_DISPLAY_NAMES: Record<FwLocale, string> = {
  en: "English",
  es: "Español",
  fr: "Français",
  de: "Deutsch",
  nl: "Nederlands",
};

/** Get the human-readable display name for a locale. */
export function getLocaleDisplayName(locale: FwLocale): string {
  return LOCALE_DISPLAY_NAMES[locale] ?? locale;
}

/** Get the list of available built-in locales. */
export function getAvailableLocales(): FwLocale[] {
  return Object.keys(LOCALE_PACKS) as FwLocale[];
}

/** Get the full translation pack for a built-in locale. */
export function getLocalePack(locale: FwLocale): TranslationStrings {
  return LOCALE_PACKS[locale] ?? DEFAULT_TRANSLATIONS;
}

/**
 * Create a translate function from an i18n config.
 *
 * Resolution order:
 * 1. `config.translations[key]` (user overrides)
 * 2. `LOCALE_PACKS[config.locale][key]` (built-in locale)
 * 3. `DEFAULT_TRANSLATIONS[key]` (English fallback)
 * 4. `fallback` argument
 * 5. The raw `key` string
 */
export function createTranslator(config?: I18nConfig): TranslateFn {
  const localePack = config?.locale ? LOCALE_PACKS[config.locale] : undefined;
  const overrides = config?.translations;

  return (key: keyof TranslationStrings, fallback?: string): string => {
    if (overrides && key in overrides) {
      return overrides[key]!;
    }
    if (localePack && key in localePack) {
      return localePack[key];
    }
    if (key in DEFAULT_TRANSLATIONS) {
      return DEFAULT_TRANSLATIONS[key];
    }
    return fallback ?? key;
  };
}

/**
 * Simple one-shot translate. For repeated use, prefer `createTranslator()`.
 */
export function translate(
  key: keyof TranslationStrings,
  config?: I18nConfig,
  fallback?: string
): string {
  return createTranslator(config)(key, fallback);
}
