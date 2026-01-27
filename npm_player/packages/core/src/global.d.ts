/**
 * Global type declarations for player-core
 */

// Web Worker inline imports (rollup-plugin-web-worker-loader)
declare module "web-worker:*" {
  const WorkerFactory: {
    new (): Worker;
  };
  export default WorkerFactory;
}
