import resolve from '@rollup/plugin-node-resolve';
import commonjs from '@rollup/plugin-commonjs';
import terser from '@rollup/plugin-terser';
import babel from '@rollup/plugin-babel';
import typescript from '@rollup/plugin-typescript';
import peerDepsExternal from 'rollup-plugin-peer-deps-external';
import url from '@rollup/plugin-url';

const isDevelopment = process.env.NODE_ENV === 'development';

export default {
  input: 'src/library.ts',
  output: [
    {
      file: 'dist/index.cjs.js',
      format: 'cjs',
      sourcemap: !isDevelopment,
      inlineDynamicImports: true,
    },
    {
      file: 'dist/index.esm.js',
      format: 'esm',
      sourcemap: !isDevelopment,
      inlineDynamicImports: true,
    },
  ],
  plugins: [
    peerDepsExternal(),
    url({ include: ['**/*.png', '**/*.jpg', '**/*.jpeg', '**/*.svg'], limit: 0 }),
    commonjs({
      preferBuiltins: false,
      include: 'node_modules/**',
    }),
    resolve(),
    typescript({
      tsconfig: './tsconfig.json',
      declaration: true,
      declarationDir: 'dist',
      rootDir: 'src',
    }),
    babel({
      exclude: 'node_modules/**',
      babelHelpers: 'bundled',
      extensions: ['.js', '.jsx', '.ts', '.tsx'],
      presets: ['@babel/preset-env', '@babel/preset-react', '@babel/preset-typescript'],
    }),
    !isDevelopment && terser(),
  ],
}; 