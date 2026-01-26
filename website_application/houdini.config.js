/** @type {import('houdini').ConfigFile} */
const config = {
  schemaPath: "../pkg/graphql/schema.graphql",
  runtimeDir: "$houdini",

  // Include shared operations from pkg/graphql and route files
  include: [
    "../pkg/graphql/operations/**/*.gql",
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
    Money: {
      type: "number",
    },
    Currency: {
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
