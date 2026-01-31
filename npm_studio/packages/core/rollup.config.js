import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";

const isDevelopment = process.env.NODE_ENV === "development";

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
    output: {
      dir: "dist/cjs",
      format: "cjs",
      sourcemap: !isDevelopment,
      exports: "named",
      preserveModules: true,
      preserveModulesRoot: "src",
      entryFileNames: "[name].cjs",
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
  // Encoder Worker bundle (must be bundled - self-contained IIFE)
  {
    input: "src/workers/encoder.worker.ts",
    output: {
      file: "dist/workers/encoder.worker.js",
      format: "iife",
      sourcemap: !isDevelopment,
      name: "EncoderWorker",
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/workers",
      }),
    ],
  },
  // Compositor Worker bundle (must be bundled - self-contained IIFE)
  {
    input: "src/workers/compositor.worker.ts",
    output: {
      file: "dist/workers/compositor.worker.js",
      format: "iife",
      sourcemap: !isDevelopment,
      name: "CompositorWorker",
      inlineDynamicImports: true,
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/workers",
      }),
    ],
  },
  // RTC Transform Worker bundle (must be bundled - self-contained IIFE)
  {
    input: "src/workers/rtcTransform.worker.ts",
    output: {
      file: "dist/workers/rtcTransform.worker.js",
      format: "iife",
      sourcemap: !isDevelopment,
      name: "RTCTransformWorker",
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: "./tsconfig.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/workers",
      }),
    ],
  },
];
