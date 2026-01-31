import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import babel from "@rollup/plugin-babel";
import typescript from "@rollup/plugin-typescript";
import peerDepsExternal from "rollup-plugin-peer-deps-external";
import url from "@rollup/plugin-url";
import { codecovRollupPlugin } from "@codecov/rollup-plugin";

const isDevelopment = process.env.NODE_ENV === "development";

export default {
  input: "src/index.tsx",
  external: [
    "@livepeer-frameworks/player-core",
    "react",
    "react-dom",
    "react/jsx-runtime",
    /^@radix-ui\//,
    "lucide-react",
    "class-variance-authority",
    "clsx",
    "tailwind-merge",
  ],
  output: [
    {
      dir: "dist",
      format: "cjs",
      sourcemap: !isDevelopment,
      entryFileNames: "cjs/index.js",
      exports: "named",
      inlineDynamicImports: true,
    },
    {
      dir: "dist",
      format: "esm",
      sourcemap: !isDevelopment,
      entryFileNames: "esm/index.js",
      inlineDynamicImports: true,
    },
  ],
  plugins: [
    peerDepsExternal(),
    url({ include: ["**/*.png", "**/*.jpg", "**/*.jpeg", "**/*.svg"], limit: 100000 }),
    commonjs({
      preferBuiltins: false,
      include: /node_modules/,
      requireReturnsDefault: "auto",
      defaultIsModuleExports: "auto",
    }),
    resolve(),
    typescript({
      tsconfig: "./tsconfig.json",
      declaration: true,
      declarationDir: "dist/types",
      rootDir: "src",
    }),
    babel({
      exclude: "node_modules/**",
      babelHelpers: "bundled",
      extensions: [".js", ".jsx", ".ts", ".tsx"],
      presets: ["@babel/preset-env", "@babel/preset-react", "@babel/preset-typescript"],
    }),
    codecovRollupPlugin({
      enableBundleAnalysis: process.env.CODECOV_TOKEN !== undefined,
      bundleName: "player-react",
      uploadToken: process.env.CODECOV_TOKEN,
    }),
  ].filter(Boolean),
};
