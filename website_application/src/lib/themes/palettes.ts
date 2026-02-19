// Theme palette definitions — maps upstream LSP themes to webapp --tn-* CSS variables.
// Source of truth: ~/ddvtech/repo/lsp/modules/core/themes.js
// All values are hex strings; converted to HSL triplets at apply time.

export interface ThemePalette {
  // Surfaces
  bg: string;
  bgDark: string;
  bgHighlight: string;
  bgVisual: string;
  // Text
  fg: string;
  fgDark: string;
  fgGutter: string;
  // Meta
  terminalBlack: string;
  comment: string;
  terminal: string;
  // Accents
  blue: string;
  cyan: string;
  teal: string;
  green: string;
  yellow: string;
  orange: string;
  red: string;
  magenta: string;
  purple: string;
  // Brand derived
  accentSoft: string;
}

export interface ThemeDef {
  name: string;
  dark?: ThemePalette;
  light?: ThemePalette;
}

export type ThemeId =
  | "tokyo-night"
  | "dracula"
  | "nord"
  | "catppuccin"
  | "gruvbox"
  | "one-dark"
  | "github-dark"
  | "rose-pine"
  | "solarized"
  | "ayu-mirage";

export type ThemeMode = "dark" | "light";

export const THEME_IDS: ThemeId[] = [
  "tokyo-night",
  "dracula",
  "nord",
  "catppuccin",
  "gruvbox",
  "one-dark",
  "github-dark",
  "rose-pine",
  "solarized",
  "ayu-mirage",
];

export const THEME_PALETTES: Record<ThemeId, ThemeDef> = {
  "tokyo-night": {
    name: "Tokyo Night",
    dark: {
      bg: "#1a1b26",
      bgDark: "#16161e",
      bgHighlight: "#24283b",
      bgVisual: "#292e42",
      fg: "#c0caf5",
      fgDark: "#a9b1d6",
      fgGutter: "#3b4261",
      terminalBlack: "#1a1b26",
      comment: "#565f89",
      terminal: "#414868",
      blue: "#7aa2f7",
      cyan: "#7dcfff",
      teal: "#73daca",
      green: "#9ece6a",
      yellow: "#e0af68",
      orange: "#ff9e64",
      red: "#f7768e",
      magenta: "#ff007c",
      purple: "#bb9af7",
      accentSoft: "#d8c8fb",
    },
    light: {
      bg: "#d5d6db",
      bgDark: "#c8c9ce",
      bgHighlight: "#e1e2e7",
      bgVisual: "#c4c5ca",
      fg: "#343b59",
      fgDark: "#4e5772",
      fgGutter: "#9699a3",
      terminalBlack: "#d5d6db",
      comment: "#9699a3",
      terminal: "#b4b5b9",
      blue: "#2e7de9",
      cyan: "#007197",
      teal: "#118c74",
      green: "#587539",
      yellow: "#8c6c3e",
      orange: "#b15c00",
      red: "#f52a65",
      magenta: "#9854f1",
      purple: "#7847bd",
      accentSoft: "#a88ad8",
    },
  },

  dracula: {
    name: "Dracula",
    dark: {
      bg: "#282a36",
      bgDark: "#1e1f29",
      bgHighlight: "#44475a",
      bgVisual: "#383a4a",
      fg: "#f8f8f2",
      fgDark: "#bd93f9",
      fgGutter: "#6272a4",
      terminalBlack: "#282a36",
      comment: "#6272a4",
      terminal: "#44475a",
      blue: "#8be9fd",
      cyan: "#8be9fd",
      teal: "#50fa7b",
      green: "#50fa7b",
      yellow: "#f1fa8c",
      orange: "#ffb86c",
      red: "#ff5555",
      magenta: "#ff79c6",
      purple: "#bd93f9",
      accentSoft: "#d6bfff",
    },
  },

  nord: {
    name: "Nord",
    dark: {
      bg: "#2e3440",
      bgDark: "#272c36",
      bgHighlight: "#3b4252",
      bgVisual: "#434c5e",
      fg: "#eceff4",
      fgDark: "#d8dee9",
      fgGutter: "#4c566a",
      terminalBlack: "#2e3440",
      comment: "#616e88",
      terminal: "#4c566a",
      blue: "#81a1c1",
      cyan: "#88c0d0",
      teal: "#8fbcbb",
      green: "#a3be8c",
      yellow: "#ebcb8b",
      orange: "#d08770",
      red: "#bf616a",
      magenta: "#b48ead",
      purple: "#b48ead",
      accentSoft: "#c9a9c7",
    },
  },

  catppuccin: {
    name: "Catppuccin",
    dark: {
      bg: "#1e1e2e",
      bgDark: "#181825",
      bgHighlight: "#313244",
      bgVisual: "#45475a",
      fg: "#cdd6f4",
      fgDark: "#bac2de",
      fgGutter: "#585b70",
      terminalBlack: "#1e1e2e",
      comment: "#6c7086",
      terminal: "#45475a",
      blue: "#89b4fa",
      cyan: "#89dceb",
      teal: "#94e2d5",
      green: "#a6e3a1",
      yellow: "#f9e2af",
      orange: "#fab387",
      red: "#f38ba8",
      magenta: "#f5c2e7",
      purple: "#cba6f7",
      accentSoft: "#dcc0fa",
    },
    light: {
      bg: "#eff1f5",
      bgDark: "#dce0e8",
      bgHighlight: "#ccd0da",
      bgVisual: "#bcc0cc",
      fg: "#4c4f69",
      fgDark: "#5c5f77",
      fgGutter: "#9ca0b0",
      terminalBlack: "#eff1f5",
      comment: "#8c8fa1",
      terminal: "#bcc0cc",
      blue: "#1e66f5",
      cyan: "#179299",
      teal: "#179299",
      green: "#40a02b",
      yellow: "#df8e1d",
      orange: "#fe640b",
      red: "#d20f39",
      magenta: "#ea76cb",
      purple: "#8839ef",
      accentSoft: "#a85cf5",
    },
  },

  gruvbox: {
    name: "Gruvbox",
    dark: {
      bg: "#282828",
      bgDark: "#1d2021",
      bgHighlight: "#3c3836",
      bgVisual: "#504945",
      fg: "#ebdbb2",
      fgDark: "#d5c4a1",
      fgGutter: "#665c54",
      terminalBlack: "#282828",
      comment: "#928374",
      terminal: "#504945",
      blue: "#83a598",
      cyan: "#8ec07c",
      teal: "#8ec07c",
      green: "#b8bb26",
      yellow: "#fabd2f",
      orange: "#fe8019",
      red: "#fb4934",
      magenta: "#d3869b",
      purple: "#d3869b",
      accentSoft: "#e0a3b3",
    },
    light: {
      bg: "#fbf1c7",
      bgDark: "#f2e5bc",
      bgHighlight: "#ebdbb2",
      bgVisual: "#d5c4a1",
      fg: "#3c3836",
      fgDark: "#504945",
      fgGutter: "#a89984",
      terminalBlack: "#fbf1c7",
      comment: "#928374",
      terminal: "#d5c4a1",
      blue: "#076678",
      cyan: "#427b58",
      teal: "#427b58",
      green: "#79740e",
      yellow: "#b57614",
      orange: "#af3a03",
      red: "#9d0006",
      magenta: "#8f3f71",
      purple: "#8f3f71",
      accentSoft: "#a85f91",
    },
  },

  "one-dark": {
    name: "One Dark",
    dark: {
      bg: "#282c34",
      bgDark: "#21252b",
      bgHighlight: "#2c313a",
      bgVisual: "#3a3f4b",
      fg: "#abb2bf",
      fgDark: "#9da5b4",
      fgGutter: "#3e4451",
      terminalBlack: "#282c34",
      comment: "#5c6370",
      terminal: "#4b5263",
      blue: "#61afef",
      cyan: "#56b6c2",
      teal: "#56b6c2",
      green: "#98c379",
      yellow: "#e5c07b",
      orange: "#d19a66",
      red: "#e06c75",
      magenta: "#c678dd",
      purple: "#c678dd",
      accentSoft: "#d8a0ee",
    },
  },

  "github-dark": {
    name: "GitHub Dark",
    dark: {
      bg: "#0d1117",
      bgDark: "#010409",
      bgHighlight: "#161b22",
      bgVisual: "#21262d",
      fg: "#e6edf3",
      fgDark: "#8b949e",
      fgGutter: "#30363d",
      terminalBlack: "#0d1117",
      comment: "#8b949e",
      terminal: "#21262d",
      blue: "#58a6ff",
      cyan: "#39c5cf",
      teal: "#3fb950",
      green: "#3fb950",
      yellow: "#d29922",
      orange: "#d18616",
      red: "#f85149",
      magenta: "#bc8cff",
      purple: "#bc8cff",
      accentSoft: "#d0a6ff",
    },
  },

  "rose-pine": {
    name: "Rosé Pine",
    dark: {
      bg: "#191724",
      bgDark: "#13111e",
      bgHighlight: "#1f1d2e",
      bgVisual: "#26233a",
      fg: "#e0def4",
      fgDark: "#908caa",
      fgGutter: "#524f67",
      terminalBlack: "#191724",
      comment: "#6e6a86",
      terminal: "#403d52",
      blue: "#31748f",
      cyan: "#9ccfd8",
      teal: "#9ccfd8",
      green: "#31748f",
      yellow: "#f6c177",
      orange: "#ea9a97",
      red: "#eb6f92",
      magenta: "#c4a7e7",
      purple: "#c4a7e7",
      accentSoft: "#d6c0f0",
    },
  },

  solarized: {
    name: "Solarized",
    dark: {
      bg: "#002b36",
      bgDark: "#00212b",
      bgHighlight: "#073642",
      bgVisual: "#0a4050",
      fg: "#839496",
      fgDark: "#93a1a1",
      fgGutter: "#586e75",
      terminalBlack: "#002b36",
      comment: "#586e75",
      terminal: "#073642",
      blue: "#268bd2",
      cyan: "#2aa198",
      teal: "#2aa198",
      green: "#859900",
      yellow: "#b58900",
      orange: "#cb4b16",
      red: "#dc322f",
      magenta: "#d33682",
      purple: "#6c71c4",
      accentSoft: "#8a8ed8",
    },
    light: {
      bg: "#fdf6e3",
      bgDark: "#f5efdc",
      bgHighlight: "#eee8d5",
      bgVisual: "#ddd6c1",
      fg: "#657b83",
      fgDark: "#586e75",
      fgGutter: "#93a1a1",
      terminalBlack: "#fdf6e3",
      comment: "#93a1a1",
      terminal: "#eee8d5",
      blue: "#268bd2",
      cyan: "#2aa198",
      teal: "#2aa198",
      green: "#859900",
      yellow: "#b58900",
      orange: "#cb4b16",
      red: "#dc322f",
      magenta: "#d33682",
      purple: "#6c71c4",
      accentSoft: "#8a8ed8",
    },
  },

  "ayu-mirage": {
    name: "Ayu Mirage",
    dark: {
      bg: "#1f2430",
      bgDark: "#1a1e29",
      bgHighlight: "#272d38",
      bgVisual: "#2d3441",
      fg: "#cccac2",
      fgDark: "#707a8c",
      fgGutter: "#565b70",
      terminalBlack: "#1f2430",
      comment: "#5c6773",
      terminal: "#3d424d",
      blue: "#5ccfe6",
      cyan: "#5ccfe6",
      teal: "#95e6cb",
      green: "#bae67e",
      yellow: "#ffd580",
      orange: "#ffae57",
      red: "#f28779",
      magenta: "#d4bfff",
      purple: "#d4bfff",
      accentSoft: "#e2d4ff",
    },
  },
};
