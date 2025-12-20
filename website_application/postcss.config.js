import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default {
  plugins: {
    // postcss-import MUST be first to resolve @import statements before other plugins
    "postcss-import": {
      resolve: (id) => {
        // Map package CSS imports to source files during dev
        const cssAliases = {
          '@livepeer-frameworks/player-core/player.css':
            path.resolve(__dirname, '../npm_player/packages/core/src/styles/player.css'),
          '@livepeer-frameworks/player-svelte/player.css':
            path.resolve(__dirname, '../npm_player/packages/svelte/src/player.css'),
          '@livepeer-frameworks/streamcrafter-core/streamcrafter.css':
            path.resolve(__dirname, '../npm_studio/packages/core/src/styles/streamcrafter.css'),
          '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css':
            path.resolve(__dirname, '../npm_studio/packages/svelte/src/streamcrafter.css'),
        };
        return cssAliases[id] || id;
      }
    },
    "@tailwindcss/postcss": {},
  },
};
