import { readFileSync } from "fs";
import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import babel from "@rollup/plugin-babel";
import typescript from "@rollup/plugin-typescript";
import url from "@rollup/plugin-url";

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
  url({ include: ["**/*.png", "**/*.jpg", "**/*.jpeg", "**/*.svg"], limit: 100000 }),
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
];
