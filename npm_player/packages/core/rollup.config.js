import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";

const isDevelopment = process.env.NODE_ENV === "development";

const externalDependencies = [
  "dashjs",
  "hls.js",
  "video.js",
  "@videojs/vhs-utils/es/resolve-url.js",
  "@videojs/vhs-utils/es/resolve-url",
  "@videojs/vhs-utils",
  "global/window",
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
  // Main library - ESM (unbundled for better tree-shaking)
  {
    input: {
      index: "src/index.ts",
      vanilla: "src/vanilla/index.ts",
    },
    external: externalDependencies,
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
  // Main library - CJS (unbundled for better tree-shaking)
  {
    input: {
      index: "src/index.ts",
      vanilla: "src/vanilla/index.ts",
    },
    external: externalDependencies,
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
  // WebCodecs Worker bundle (must be bundled - self-contained IIFE)
  {
    input: "src/players/WebCodecsPlayer/worker/decoder.worker.ts",
    output: {
      file: "dist/workers/decoder.worker.js",
      format: "iife",
      sourcemap: !isDevelopment,
      name: "DecoderWorker",
    },
    plugins: [
      resolve(),
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/workers",
      }),
    ],
  },
];
