import { DEFAULT_GATEWAY_URL } from "@livepeer-frameworks/player-core";
import type { PlayerInput } from "./source";

export const SNIPPET_FRAMEWORKS = ["React", "Svelte", "Web Component", "JavaScript"] as const;
export type SnippetFramework = (typeof SNIPPET_FRAMEWORKS)[number];

type Option = [name: string, value: string];

/** The player options a page needs to play this input, in source order. */
function playerOptions(input: PlayerInput): Option[] {
  const options: Option[] = [];
  if (input.gatewayUrl && input.gatewayUrl !== DEFAULT_GATEWAY_URL) {
    options.push(["gatewayUrl", input.gatewayUrl]);
  }
  if (input.viewerProtocol) options.push(["viewerProtocol", input.viewerProtocol]);
  if (input.mistUrl) options.push(["mistUrl", input.mistUrl]);
  if (input.forceType) options.push(["forceType", input.forceType]);
  return options;
}

const kebab = (name: string) => name.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);
const indent = (text: string, spaces: number) =>
  text
    .split("\n")
    .map((line) => (line ? " ".repeat(spaces) + line : line))
    .join("\n");

const objectLiteral = (options: Option[], spaces: number) =>
  `{\n${options.map(([k, v]) => `${" ".repeat(spaces + 2)}${k}: ${JSON.stringify(v)},`).join("\n")}\n${" ".repeat(spaces)}}`;

const endpointsLiteral = (input: PlayerInput) => JSON.stringify(input.endpoints, null, 2);

/** Embed code that plays the same source the tester is playing. */
export function embedSnippet(framework: SnippetFramework, input: PlayerInput): string {
  const options = playerOptions(input);
  const contentType = input.contentType ? input.contentType : undefined;

  if (framework === "React") {
    const props = [`contentId=${JSON.stringify(input.contentId)}`];
    if (contentType) props.push(`contentType="${contentType}"`);
    if (input.endpoints)
      props.push(`endpoints={${indent(endpointsLiteral(input), 2).trimStart()}}`);
    if (options.length) props.push(`options={${objectLiteral(options, 2)}}`);
    return [
      'import { Player } from "@livepeer-frameworks/player-react";',
      'import "@livepeer-frameworks/player-react/player.css";',
      "",
      "export function StreamPlayer() {",
      "  return (",
      "    <Player",
      ...props.map((p) => indent(p, 6)),
      "    />",
      "  );",
      "}",
    ].join("\n");
  }

  if (framework === "Svelte") {
    const props = [`contentId=${JSON.stringify(input.contentId)}`];
    if (contentType) props.push(`contentType="${contentType}"`);
    if (input.endpoints) props.push("{endpoints}");
    if (options.length) props.push(`options={${objectLiteral(options, 2)}}`);
    return [
      "<script>",
      '  import { Player } from "@livepeer-frameworks/player-svelte";',
      '  import "@livepeer-frameworks/player-svelte/player.css";',
      ...(input.endpoints
        ? [`  const endpoints = ${indent(endpointsLiteral(input), 2).trimStart()};`]
        : []),
      "</script>",
      "",
      "<Player",
      ...props.map((p) => indent(p, 2)),
      "/>",
    ].join("\n");
  }

  if (framework === "Web Component") {
    const attrs = [`content-id=${JSON.stringify(input.contentId)}`];
    if (contentType) attrs.push(`content-type="${contentType}"`);
    for (const [name, value] of options) attrs.push(`${kebab(name)}=${JSON.stringify(value)}`);
    const lines = [
      '<script type="module">',
      '  import "@livepeer-frameworks/player-wc/define";',
      ...(input.endpoints
        ? [
            `  document.querySelector("fw-player").endpoints = ${indent(endpointsLiteral(input), 2).trimStart()};`,
          ]
        : []),
      "</script>",
      "",
      "<fw-player",
      ...attrs.map((a) => indent(a, 2)),
      "></fw-player>",
    ];
    return lines.join("\n");
  }

  const config: string[] = ['target: "#player"', `contentId: ${JSON.stringify(input.contentId)}`];
  if (contentType) config.push(`contentType: "${contentType}"`);
  if (input.endpoints) config.push(`endpoints: ${indent(endpointsLiteral(input), 2).trimStart()}`);
  for (const [name, value] of options) config.push(`${name}: ${JSON.stringify(value)}`);
  return [
    'import { createPlayer } from "@livepeer-frameworks/player-core";',
    'import "@livepeer-frameworks/player-core/player.css";',
    "",
    "createPlayer({",
    ...config.map((c) => `  ${c},`),
    "});",
  ].join("\n");
}
