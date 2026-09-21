import { globalIgnores, createTsConfig, tseslint } from "../eslint.base.config.js";

export default tseslint.config(
  globalIgnores,
  {
    ignores: [
      "src/generated/**",
      "examples/**",
      "eslint.config.js",
      "vitest.config.ts",
      "codegen.ts",
    ],
  },
  createTsConfig(["src/**/*.ts", "test/**/*.ts"])
);
