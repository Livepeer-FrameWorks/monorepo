import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import babel from "@rollup/plugin-babel";
import typescript from "@rollup/plugin-typescript";
import peerDepsExternal from "rollup-plugin-peer-deps-external";
import { codecovRollupPlugin } from "@codecov/rollup-plugin";

const isDevelopment = process.env.NODE_ENV === "development";

export default {
  input: "src/index.tsx",
  external: ["@livepeer-frameworks/streamcrafter-core", "react", "react-dom", "react/jsx-runtime"],
  output: [
    {
      dir: "dist",
      format: "cjs",
      sourcemap: !isDevelopment,
      entryFileNames: "cjs/index.cjs",
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
      bundleName: "streamcrafter-react",
      uploadToken: process.env.CODECOV_TOKEN,
    }),
  ].filter(Boolean),
};
