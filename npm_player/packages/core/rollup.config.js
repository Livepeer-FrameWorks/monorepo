import { existsSync, mkdirSync, readdirSync, copyFileSync, readFileSync } from "fs";
import { join } from "path";
import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";

const isDevelopment = process.env.NODE_ENV === "development";

/** Copy prebuilt WASM decoders (hevc, av1, vp9) to dist/wasm/ if they exist */
function wasmCopyPlugin() {
  return {
    name: "wasm-copy",
    writeBundle() {
      const src = join("src", "wasm", "decoders", "prebuilt");
      const dest = join("dist", "wasm");
      if (!existsSync(src)) return;
      const files = readdirSync(src).filter((f) => f.endsWith(".wasm"));
      if (files.length === 0) return;
      mkdirSync(dest, { recursive: true });
      for (const file of files) {
        copyFileSync(join(src, file), join(dest, file));
      }
    },
  };
}

// Auto-externalize all dependencies and peerDependencies from package.json.
// Library ESM builds must never resolve deps to filesystem paths.
const pkg = JSON.parse(readFileSync("./package.json", "utf-8"));
const allDeps = [
  ...Object.keys(pkg.dependencies || {}),
  ...Object.keys(pkg.peerDependencies || {}),
];
const depsPattern = new RegExp(
  `^(${allDeps.map((d) => d.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})($|/)`
);
const external = (id) => depsPattern.test(id);

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
        tsconfig: "./tsconfig.main.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/esm",
      }),
      wasmCopyPlugin(),
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
        tsconfig: "./tsconfig.worker.json",
        declaration: false,
        declarationDir: undefined,
        outDir: "dist/workers",
      }),
    ],
  },
];
