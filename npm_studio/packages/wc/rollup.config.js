import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";
import terser from "@rollup/plugin-terser";
import copy from "rollup-plugin-copy";

const isDevelopment = process.env.NODE_ENV === "development";

const external = [
  "@livepeer-frameworks/streamcrafter-core",
  /^@livepeer-frameworks\/streamcrafter-core\//,
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
      file: "dist/fw-streamcrafter.iife.js",
      format: "iife",
      name: "FwStreamCrafter",
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
      copy({
        targets: [
          {
            src: "../core/dist/workers/compositor.worker.js",
            dest: "dist/workers",
          },
          {
            src: "../core/dist/workers/encoder.worker.js",
            dest: "dist/workers",
          },
          {
            src: "../core/dist/workers/rtcTransform.worker.js",
            dest: "dist/workers",
          },
        ],
        hook: "writeBundle",
      }),
    ],
  },
];
