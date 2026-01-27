/**
 * Shared ESLint configuration for the monorepo.
 * Import the pieces you need in each workspace's eslint.config.js.
 */
import globals from "globals";
import tseslint from "typescript-eslint";
import prettier from "eslint-config-prettier";

// Shared rules for unused variables
export const unusedVarsRule = {
  "@typescript-eslint/no-unused-vars": [
    "error",
    {
      argsIgnorePattern: "^_",
      varsIgnorePattern: "^_",
    },
  ],
};

// Common files to ignore across all projects
export const globalIgnores = {
  ignores: [
    "**/node_modules/**",
    "**/dist/**",
    "**/build/**",
    "**/.svelte-kit/**",
    "**/$houdini/**",
    "**/coverage/**",
    "**/*.min.js",
  ],
};

// Shared TypeScript rules
export const sharedTsRules = {
  ...unusedVarsRule,
  "@typescript-eslint/no-explicit-any": "warn",
  "@typescript-eslint/consistent-type-imports": "error",
  "prefer-const": "error",
  "no-var": "error",
};

// Base TypeScript config
export function createTsConfig(files, extraRules = {}) {
  return {
    files,
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        projectService: true,
      },
      globals: {
        ...globals.browser,
        ...globals.node,
      },
    },
    plugins: {
      "@typescript-eslint": tseslint.plugin,
    },
    rules: {
      ...tseslint.configs.recommended[0]?.rules,
      ...sharedTsRules,
      ...extraRules,
    },
  };
}

// Re-export commonly used configs
export { globals, tseslint, prettier };
