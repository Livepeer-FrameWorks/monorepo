/** @type {import('tailwindcss').Config} */
export default {
  content: ["./src/**/*.{html,js,svelte,ts}"],
  theme: {
    extend: {
      colors: {
        // Semantic status colors
        success: "hsl(var(--tn-green) / <alpha-value>)",
        warning: "hsl(var(--tn-yellow) / <alpha-value>)",
        "warning-alt": "hsl(var(--tn-orange) / <alpha-value>)",
        info: "hsl(var(--tn-cyan) / <alpha-value>)",
        error: "hsl(var(--tn-red) / <alpha-value>)",

        // Brand accents
        accent: "hsl(var(--tn-blue) / <alpha-value>)",
        "accent-purple": "hsl(var(--tn-purple) / <alpha-value>)",
        "accent-teal": "hsl(var(--tn-teal) / <alpha-value>)",

        // Tokyo Night color scheme - now using HSL tokens for alpha transparency
        "tokyo-night": {
          bg: "hsl(var(--tn-bg) / <alpha-value>)",
          "bg-dark": "hsl(var(--tn-bg-dark) / <alpha-value>)",
          "bg-light": "hsl(var(--tn-bg-highlight) / <alpha-value>)",
          "bg-highlight": "hsl(var(--tn-bg-highlight) / <alpha-value>)",
          "bg-visual": "hsl(var(--tn-bg-visual) / <alpha-value>)",
          fg: "hsl(var(--tn-fg) / <alpha-value>)",
          "fg-dark": "hsl(var(--tn-fg-dark) / <alpha-value>)",
          "fg-gutter": "hsl(var(--tn-fg-gutter) / <alpha-value>)",
          comment: "hsl(var(--tn-comment) / <alpha-value>)",
          terminal: "hsl(var(--tn-terminal) / <alpha-value>)",
          cyan: "hsl(var(--tn-cyan) / <alpha-value>)",
          blue: "hsl(var(--tn-blue) / <alpha-value>)",
          purple: "hsl(var(--tn-purple) / <alpha-value>)",
          red: "hsl(var(--tn-red) / <alpha-value>)",
          orange: "hsl(var(--tn-orange) / <alpha-value>)",
          yellow: "hsl(var(--tn-yellow) / <alpha-value>)",
          green: "hsl(var(--tn-green) / <alpha-value>)",
          teal: "hsl(var(--tn-teal) / <alpha-value>)",
          magenta: "hsl(var(--tn-magenta) / <alpha-value>)",
          surface: "hsl(var(--tn-bg-highlight) / <alpha-value>)",
          selection: "hsl(var(--tn-bg-visual) / <alpha-value>)",
        },
      },
      fontFamily: {
        mono: ["JetBrains Mono", "Fira Code", "monospace"],
        sans: ["Inter", "system-ui", "sans-serif"],
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
      boxShadow: {
        brand: "0 24px 48px hsl(var(--tn-bg-dark) / 0.45)",
        "brand-soft": "0 20px 40px hsl(var(--tn-bg-dark) / 0.25)",
        "brand-subtle": "0 12px 28px hsl(var(--tn-bg-dark) / 0.18)",
        "brand-strong": "0 32px 64px hsl(var(--tn-bg-dark) / 0.6)",
        "inset-brand": "inset 0 1px 0 hsl(var(--tn-fg) / 0.05)",
      },
      animation: {
        float: "float 3s ease-in-out infinite",
        "pulse-slow": "pulse 3s ease-in-out infinite",
        gradient: "gradient 6s ease infinite",
      },
      keyframes: {
        float: {
          "0%, 100%": { transform: "translateY(0px)" },
          "50%": { transform: "translateY(-10px)" },
        },
        gradient: {
          "0%, 100%": { backgroundPosition: "0% 50%" },
          "50%": { backgroundPosition: "100% 50%" },
        },
      },
      backgroundSize: {
        "300%": "300%",
      },
    },
  },
  plugins: [],
};
