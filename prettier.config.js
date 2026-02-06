/** @type {import("prettier").Config} */
export default {
  semi: true,
  singleQuote: false,
  tabWidth: 2,
  useTabs: false,
  trailingComma: "es5",
  printWidth: 100,
  plugins: ["prettier-plugin-svelte", "prettier-plugin-astro"],
  overrides: [
    {
      files: "*.svelte",
      options: {
        parser: "svelte",
      },
    },
    {
      files: "*.astro",
      options: {
        parser: "astro",
      },
    },
  ],
};
