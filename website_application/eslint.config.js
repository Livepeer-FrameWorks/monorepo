import svelte from "eslint-plugin-svelte";
import svelteParser from "svelte-eslint-parser";
import svelteConfig from "./svelte.config.js";
import {
  globalIgnores,
  sharedTsRules,
  tseslint,
  globals,
  prettier,
} from "../eslint.base.config.js";

export default tseslint.config(
  globalIgnores,
  {
    ignores: [
      "$houdini/**",
      "src/lib/components/ui/**",
      "src/lib/graphql/generated/**",
      "**/*.config.js",
      "**/*.svelte.ts",
    ],
  },
  {
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        projectService: true,
        extraFileExtensions: [".svelte"],
      },
      globals: {
        ...globals.browser,
        ...globals.node,
      },
    },
  },
  ...tseslint.configs.recommended.map((config) => ({
    ...config,
    rules: {
      ...config.rules,
      ...sharedTsRules,
    },
  })),
  ...svelte.configs.recommended,
  {
    files: ["**/*.svelte"],
    languageOptions: {
      parser: svelteParser,
      parserOptions: {
        parser: tseslint.parser,
        svelteConfig,
        svelteFeatures: {
          runes: true,
        },
      },
    },
  },
  {
    files: ["src/lib/graphql/generated/**"],
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-unused-vars": "off",
    },
  },
  prettier,
  ...svelte.configs.prettier
);
