import resolve from "@rollup/plugin-node-resolve";
import commonjs from "@rollup/plugin-commonjs";
import terser from "@rollup/plugin-terser";
import babel from "@rollup/plugin-babel";
import typescript from "@rollup/plugin-typescript";
import peerDepsExternal from "rollup-plugin-peer-deps-external";
import url from "@rollup/plugin-url";
import { copy } from "@web/rollup-plugin-copy";
import postcss from "rollup-plugin-postcss";

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

const normalizeId = (id) => id.split("\\").join("/");

const manualChunkForId = (id) => {
  const normalized = normalizeId(id);

  if (normalized.includes("/components/players/DashJsPlayer")) return "player-dash";
  if (normalized.includes("/components/players/HlsJsPlayer")) return "player-hls";
  if (normalized.includes("/components/players/VideoJsPlayer")) return "player-video";
  if (normalized.includes("/components/players/Html5NativePlayer")) return "player-html5";
  if (normalized.includes("/components/players/MistPlayer")) return "player-mist";
  if (normalized.includes("/components/players/MewsWsPlayer")) return "player-mews";
  if (normalized.includes("/components/Player")) return "player-shell";
  if (normalized.includes("/core/")) return "player-core";

  return undefined;
};

export default {
  input: "src/library.ts",
  external: externalDependencies,
  output: [
    {
      dir: "dist",
      format: "cjs",
      sourcemap: !isDevelopment,
      inlineDynamicImports: true,
      entryFileNames: "cjs/index.js",
      exports: "named",
    },
    {
      dir: "dist",
      format: "esm",
      sourcemap: !isDevelopment,
      entryFileNames: "esm/index.js",
      chunkFileNames: "esm/chunks/[name]-[hash].js",
      manualChunks: manualChunkForId,
    },
  ],
  preserveEntrySignatures: "exports-only",
  plugins: [
    peerDepsExternal(),
    postcss({
      extensions: [".css"],
      inject: true,
      extract: false,
      modules: false,
      minimize: !isDevelopment,
      sourceMap: !isDevelopment,
      config: {
        path: "./postcss.config.cjs",
      },
    }),
    url({ include: ["**/*.png", "**/*.jpg", "**/*.jpeg", "**/*.svg"], limit: 100000 }),
    copy({
      targets: [{ src: "public/**/*.{png,jpg,jpeg}", dest: "dist/public" }],
    }),
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
    !isDevelopment &&
      terser({
        format: { comments: false },
        compress: { passes: 2 },
        module: true,
        maxWorkers: 1,
      }),
  ],
};
