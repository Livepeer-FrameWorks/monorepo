/** @type {import('houdini').ConfigFile} */
const config = {
  schemaPath: "../pkg/graphql/schema.graphql",
  runtimeDir: "$houdini",

  // Only include Houdini-specific files, exclude old Apollo graphql files
  include: [
    "src/lib/houdini/**/*.gql",
    "src/routes/**/*.svelte",
    "src/routes/**/*.js",
    "src/routes/**/*.ts",
  ],
  exclude: ["src/lib/graphql/**/*"],

  // Custom scalars matching existing types
  scalars: {
    Time: {
      type: "string",
    },
    JSON: {
      type: "Record<string, unknown>",
    },
    ID: {
      type: "string",
    },
  },

  // Default cache behavior
  defaultCachePolicy: "CacheOrNetwork",

  // Default list operations
  defaultListPosition: "first",

  plugins: {
    "houdini-svelte": {
      client: "./src/lib/houdini/client",
    },
  },
};

export default config;
