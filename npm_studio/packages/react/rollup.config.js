import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import babel from "@rollup/plugin-babel";
import typescript from "@rollup/plugin-typescript";
import peerDepsExternal from "rollup-plugin-peer-deps-external";

const isDevelopment = process.env.NODE_ENV === "development";

const external = [
  "@livepeer-frameworks/streamcrafter-core",
  "react",
  "react-dom",
  "react/jsx-runtime",
  /^@radix-ui\//,
  "lucide-react",
];

const commonPlugins = [
  peerDepsExternal(),
  commonjs({
    preferBuiltins: false,
    include: /node_modules/,
    requireReturnsDefault: "auto",
    defaultIsModuleExports: "auto",
  }),
  resolve(),
];

export default [
  // ESM (unbundled for better tree-shaking)
  {
    input: "src/index.tsx",
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
      babel({
        exclude: "node_modules/**",
        babelHelpers: "bundled",
        extensions: [".js", ".jsx", ".ts", ".tsx"],
        presets: ["@babel/preset-env", "@babel/preset-react", "@babel/preset-typescript"],
      }),
    ],
  },
  // CJS (unbundled for better tree-shaking)
  {
    input: "src/index.tsx",
    external,
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
      babel({
        exclude: "node_modules/**",
        babelHelpers: "bundled",
        extensions: [".js", ".jsx", ".ts", ".tsx"],
        presets: ["@babel/preset-env", "@babel/preset-react", "@babel/preset-typescript"],
      }),
    ],
  },
];
