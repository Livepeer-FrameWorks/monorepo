import { readFileSync } from "fs";
import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import typescript from "@rollup/plugin-typescript";
import terser from "@rollup/plugin-terser";

const isDevelopment = process.env.NODE_ENV === "development";

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
