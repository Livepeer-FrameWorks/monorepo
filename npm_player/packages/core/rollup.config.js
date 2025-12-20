import resolve from '@rollup/plugin-node-resolve';
import commonjs from '@rollup/plugin-commonjs';
import typescript from '@rollup/plugin-typescript';

const isDevelopment = process.env.NODE_ENV === 'development';

const externalDependencies = [
  'dashjs',
  'hls.js',
  'video.js',
  '@videojs/vhs-utils/es/resolve-url.js',
  '@videojs/vhs-utils/es/resolve-url',
  '@videojs/vhs-utils',
  'global/window'
];

export default [
  // Main bundle
  {
    input: 'src/index.ts',
    external: externalDependencies,
    output: [
      {
        dir: 'dist',
        format: 'cjs',
        sourcemap: !isDevelopment,
        entryFileNames: 'cjs/index.js',
        exports: 'named',
        inlineDynamicImports: true
      },
      {
        dir: 'dist',
        format: 'esm',
        sourcemap: !isDevelopment,
        entryFileNames: 'esm/index.js',
        inlineDynamicImports: true
      }
    ],
    plugins: [
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
        requireReturnsDefault: 'auto',
        defaultIsModuleExports: 'auto'
      }),
      resolve(),
      typescript({
        tsconfig: './tsconfig.json',
        declaration: true,
        declarationDir: 'dist/types',
        rootDir: 'src'
      })
    ].filter(Boolean)
  },
  // WebCodecs Worker bundle (separate entry for proper bundling)
  {
    input: 'src/players/WebCodecsPlayer/worker/decoder.worker.ts',
    output: {
      file: 'dist/workers/decoder.worker.js',
      format: 'iife',
      sourcemap: !isDevelopment,
      name: 'DecoderWorker'
    },
    plugins: [
      resolve(),
      typescript({
        tsconfig: './tsconfig.json',
        declaration: false,
        declarationDir: undefined,
        outDir: 'dist/workers'
      })
    ].filter(Boolean)
  }
];
