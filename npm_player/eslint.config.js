import sveltePlugin from "eslint-plugin-svelte";
import svelteParser from "svelte-eslint-parser";
import {
  globalIgnores,
  sharedTsRules,
  unusedVarsRule,
  tseslint,
  globals,
} from "../eslint.base.config.js";

export default tseslint.config(
  globalIgnores,
  {
    ignores: ["**/playground/**", "src/**", "**/*.config.js", "**/*.config.ts"],
  },

  // TypeScript base config for core package
  {
    files: ["packages/core/src/**/*.ts"],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        projectService: true,
      },
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      "@typescript-eslint": tseslint.plugin,
    },
    rules: {
      ...tseslint.configs.recommended[0]?.rules,
      ...sharedTsRules,
    },
  },

  // React package - TSX files
  {
    files: ["packages/react/src/**/*.{ts,tsx}"],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        projectService: true,
        ecmaFeatures: {
          jsx: true,
        },
      },
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      "@typescript-eslint": tseslint.plugin,
    },
    rules: {
      ...tseslint.configs.recommended[0]?.rules,
      ...sharedTsRules,
    },
  },

  // Svelte package - TypeScript files
  {
    files: ["packages/svelte/src/**/*.ts"],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        projectService: true,
      },
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      "@typescript-eslint": tseslint.plugin,
    },
    rules: {
      ...tseslint.configs.recommended[0]?.rules,
      ...sharedTsRules,
    },
  },

  // Svelte package - Svelte files
  {
    files: ["packages/svelte/src/**/*.svelte"],
    languageOptions: {
      parser: svelteParser,
      parserOptions: {
        parser: tseslint.parser,
        svelteFeatures: {
          runes: true,
        },
      },
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      svelte: sveltePlugin,
      "@typescript-eslint": tseslint.plugin,
    },
    rules: {
      ...sveltePlugin.configs.recommended.rules,
      ...unusedVarsRule,
    },
  }
);
