import globals from "globals";
import prettier from "eslint-config-prettier";
import svelte from "eslint-plugin-svelte";
import svelteParser from "svelte-eslint-parser";
import tseslint from "typescript-eslint";
import svelteConfig from "./svelte.config.js";

export default tseslint.config(
  {
    ignores: [
      "build",
      ".svelte-kit",
      "node_modules",
      "dist",
      "$houdini/**",
      "src/lib/components/ui/**",
      "src/lib/graphql/generated/**",
      "eslint.config.js",
      "postcss.config.js",
      "svelte.config.js",
      "tailwind.config.js",
      "houdini.config.js",
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
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
        },
      ],
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
  ...svelte.configs.prettier,
);
