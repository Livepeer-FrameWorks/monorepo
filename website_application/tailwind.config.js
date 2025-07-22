/** @type {import('tailwindcss').Config} */
export default {
  content: ['./src/**/*.{html,js,svelte,ts}'],
  theme: {
    extend: {
      colors: {
        // Tokyo Night color scheme - matching marketing website
        'tokyo-night': {
          'bg': '#1a1b26',
          'bg-dark': '#16161e',
          'bg-light': '#24283b',
          'fg': '#c0caf5',
          'fg-dark': '#a9b1d6',
          'fg-gutter': '#3b4261',
          'comment': '#565f89',
          'cyan': '#7dcfff',
          'blue': '#7aa2f7',
          'purple': '#9d7cd8',
          'red': '#f7768e',
          'orange': '#ff9e64',
          'yellow': '#e0af68',
          'green': '#9ece6a',
          'teal': '#73daca',
          'terminal-black': '#414868',
        }
      },
      fontFamily: {
        'mono': ['JetBrains Mono', 'Fira Code', 'monospace'],
        'sans': ['Inter', 'system-ui', 'sans-serif'],
      },
      animation: {
        'glow': 'glow 2s ease-in-out infinite alternate',
        'float': 'float 3s ease-in-out infinite',
        'pulse-slow': 'pulse 3s ease-in-out infinite',
        'gradient': 'gradient 6s ease infinite',
      },
      keyframes: {
        glow: {
          '0%': { boxShadow: '0 0 5px #7aa2f7' },
          '100%': { boxShadow: '0 0 20px #7aa2f7, 0 0 30px #7aa2f7' }
        },
        float: {
          '0%, 100%': { transform: 'translateY(0px)' },
          '50%': { transform: 'translateY(-10px)' }
        },
        gradient: {
          '0%, 100%': { backgroundPosition: '0% 50%' },
          '50%': { backgroundPosition: '100% 50%' }
        }
      },
      backgroundSize: {
        '300%': '300%',
      }
    },
  },
  plugins: [],
} 