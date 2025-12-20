const path = require('path');

module.exports = {
  plugins: {
    // postcss-import MUST be first to resolve @import statements before other plugins
    'postcss-import': {
      resolve: (id) => {
        // Map package CSS imports to source files during dev
        const cssAliases = {
          '@livepeer-frameworks/player-core/player.css':
            path.resolve(__dirname, 'packages/core/src/styles/player.css'),
          '@livepeer-frameworks/player-react/player.css':
            path.resolve(__dirname, 'packages/react/src/player.css'),
          '@livepeer-frameworks/player-svelte/player.css':
            path.resolve(__dirname, 'packages/svelte/src/player.css'),
          '@livepeer-frameworks/streamcrafter-core/streamcrafter.css':
            path.resolve(__dirname, '../npm_studio/packages/core/src/styles/streamcrafter.css'),
          '@livepeer-frameworks/streamcrafter-react/streamcrafter.css':
            path.resolve(__dirname, '../npm_studio/packages/react/src/streamcrafter.css'),
          '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css':
            path.resolve(__dirname, '../npm_studio/packages/svelte/src/streamcrafter.css'),
        };
        return cssAliases[id] || id;
      }
    },
    '@tailwindcss/postcss': {},
    'postcss-prefix-selector': {
      prefix: '.fw-player-root',
      transform: (prefix, selector, prefixedSelector) => {
        // Skip @-rules like @keyframes, @media, @layer
        if (selector.startsWith('@')) {
          return selector;
        }
        // Don't prefix .fw-player-root itself
        if (selector === '.fw-player-root') {
          return selector;
        }
        // Don't prefix .fw-player-surface - it's on the SAME element as .fw-player-root,
        // and it defines CSS variables that child elements inherit
        if (selector === '.fw-player-surface') {
          return selector;
        }
        // Everything else gets prefixed for internal scoping
        return prefixedSelector;
      }
    },
    autoprefixer: {}
  }
};
