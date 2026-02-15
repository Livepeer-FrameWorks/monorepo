import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";
import terser from "@rollup/plugin-terser";

const isDevelopment = process.env.NODE_ENV === "development";

const external = [
  "@livepeer-frameworks/player-core",
  /^@livepeer-frameworks\/player-core\//,
  "lit",
  /^lit\//,
];

const commonPlugins = [
  commonjs({
    preferBuiltins: false,
    include: /node_modules/,
    requireReturnsDefault: "auto",
    defaultIsModuleExports: "auto",
  }),
  resolve(),
];

export default [
  // ESM (unbundled for tree-shaking)
  {
    input: {
      index: "src/index.ts",
      define: "src/define.ts",
    },
    external,
    output: {
      dir: "dist/esm",
      format: "esm",
      sourcemap: !isDevelopment,
      preserveModules: true,
      preserveModulesRoot: "src",
    },
    plugins: [
      ...commonPlugins,
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/esm",
      }),
    ],
  },
  // CJS (unbundled for tree-shaking)
  {
    input: {
      index: "src/index.ts",
      define: "src/define.ts",
    },
    external,
    output: {
      dir: "dist/cjs",
      format: "cjs",
      sourcemap: !isDevelopment,
      exports: "named",
      preserveModules: true,
      preserveModulesRoot: "src",
    },
    plugins: [
      ...commonPlugins,
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/cjs",
      }),
    ],
  },
  // IIFE (fully bundled for CDN <script> tag)
  {
    input: "src/iife-entry.ts",
    output: {
      file: "dist/fw-player.iife.js",
      format: "iife",
      name: "FwPlayer",
      sourcemap: false,
      inlineDynamicImports: true,
    },
    plugins: [
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
        requireReturnsDefault: "auto",
        defaultIsModuleExports: "auto",
      }),
      resolve({ browser: true }),
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist",
      }),
      !isDevelopment && terser(),
    ],
  },
];
