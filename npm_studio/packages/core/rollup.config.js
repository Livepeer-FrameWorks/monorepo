import resolve from '@rollup/plugin-node-resolve';
import commonjs from '@rollup/plugin-commonjs';
import typescript from '@rollup/plugin-typescript';

const isDevelopment = process.env.NODE_ENV === 'development';

export default [
  // Main library bundle
  {
    input: {
      index: 'src/index.ts',
      vanilla: 'src/vanilla/index.ts',
    },
    output: [
      {
        dir: 'dist',
        format: 'cjs',
        sourcemap: !isDevelopment,
        exports: 'named',
        entryFileNames: 'cjs/[name].cjs',
        chunkFileNames: 'cjs/[name]-[hash].cjs',
      },
      {
        dir: 'dist',
        format: 'esm',
        sourcemap: !isDevelopment,
        entryFileNames: 'esm/[name].js',
        chunkFileNames: 'esm/[name]-[hash].js',
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
  // Encoder Worker bundle (separate entry for proper bundling)
  {
    input: 'src/workers/encoder.worker.ts',
    output: {
      file: 'dist/workers/encoder.worker.js',
      format: 'iife',
      sourcemap: !isDevelopment,
      name: 'EncoderWorker'
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: './tsconfig.json',
        declaration: false,
        declarationDir: undefined,
        outDir: 'dist/workers'
      })
    ].filter(Boolean)
  },
  // Compositor Worker bundle (separate entry for proper bundling)
  // Uses inlineDynamicImports because the worker imports from other modules
  {
    input: 'src/workers/compositor.worker.ts',
    output: {
      file: 'dist/workers/compositor.worker.js',
      format: 'iife',
      sourcemap: !isDevelopment,
      name: 'CompositorWorker',
      inlineDynamicImports: true
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: './tsconfig.json',
        declaration: false,
        declarationDir: undefined,
        outDir: 'dist/workers'
      })
    ].filter(Boolean)
  },
  // RTC Transform Worker bundle (for RTCRtpScriptTransform / WebCodecs encoding)
  {
    input: 'src/workers/rtcTransform.worker.ts',
    output: {
      file: 'dist/workers/rtcTransform.worker.js',
      format: 'iife',
      sourcemap: !isDevelopment,
      name: 'RTCTransformWorker'
    },
    plugins: [
      resolve(),
      commonjs({
        preferBuiltins: false,
        include: /node_modules/,
      }),
      typescript({
        tsconfig: './tsconfig.json',
        declaration: false,
        declarationDir: undefined,
        outDir: 'dist/workers'
      })
    ].filter(Boolean)
  }
];
