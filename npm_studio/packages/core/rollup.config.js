import { readFileSync } from "fs";
import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";

const isDevelopment = process.env.NODE_ENV === "development";

// Auto-externalize all dependencies and peerDependencies from package.json.
// Library ESM builds must never resolve deps to filesystem paths.
const pkg = JSON.parse(readFileSync("./package.json", "utf-8"));
const allDeps = [
  ...Object.keys(pkg.dependencies || {}),
  ...Object.keys(pkg.peerDependencies || {}),
];
const depsPattern =
  allDeps.length > 0
    ? new RegExp(
        `^(${allDeps.map((d) => d.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})($|/)`
      )
    : null;
const external = (id) => depsPattern !== null && depsPattern.test(id);

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
