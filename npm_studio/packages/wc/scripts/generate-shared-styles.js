/**
 * Reads streamcrafter.css from streamcrafter-core and generates a Lit CSSResult module.
 * This makes all .fw-sc-* classes available inside Shadow DOM via constructable stylesheets.
 */
import { readFileSync, writeFileSync, mkdirSync } from "fs";
import { resolve, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));

const cssPath = resolve(__dirname, "../../core/src/styles/streamcrafter.css");
const outPath = resolve(__dirname, "../src/styles/shared-styles.ts");

const css = readFileSync(cssPath, "utf-8");

// Escape backticks and ${} in the CSS so the template literal is safe
const escaped = css.replace(/\\/g, "\\\\").replace(/`/g, "\\`").replace(/\$\{/g, "\\${");

const output = `// AUTO-GENERATED — do not edit. Run \`pnpm run build:css\` to regenerate.
// Source: packages/core/src/styles/streamcrafter.css
import { css } from "lit";

export const sharedStyles = css\`
${escaped}
\`;
`;

mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, output, "utf-8");
console.log(`Generated shared-styles.ts (${css.length} bytes of CSS)`);
