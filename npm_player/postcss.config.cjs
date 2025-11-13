module.exports = {
  plugins: {
    '@tailwindcss/postcss': {},
    'postcss-prefix-selector': {
      prefix: '.fw-player-root',
      transform: (prefix, selector, prefixedSelector) => {
        // Only skip @-rules like @keyframes, @media, @layer
        if (selector.startsWith('@')) {
          return selector;
        }
        // Don't double-prefix the fw-player-root class itself
        if (selector === '.fw-player-root') {
          return selector;
        }
        // Everything else gets prefixed, including :root
        return prefixedSelector;
      }
    },
    autoprefixer: {}
  }
};
