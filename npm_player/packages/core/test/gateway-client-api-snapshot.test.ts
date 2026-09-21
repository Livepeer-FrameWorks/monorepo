import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

import * as gatewayModule from "../src/core/GatewayClient";

const root = fileURLToPath(new URL("..", import.meta.url));

/**
 * The declarations TypeScript emits for a source file, restricted to the
 * named declarations when names are given, without comments, imports, or
 * private members: the snapshot guards signatures, not documentation or
 * implementation details.
 */
function declarations(file: string, names?: string[]): string {
  const config = ts.getParsedCommandLineOfConfigFile(
    `${root}tsconfig.main.json`,
    {},
    { ...ts.sys, onUnRecoverableConfigFileDiagnostic: () => undefined }
  );
  if (!config) throw new Error("tsconfig.main.json did not parse");
  const path = `${root}${file}`;
  const program = ts.createProgram([path], {
    ...config.options,
    noEmit: false,
    declaration: true,
    emitDeclarationOnly: true,
  });
  let text = "";
  program.emit(
    program.getSourceFile(path),
    (name, data) => {
      if (name.endsWith(".d.ts")) text = data;
    },
    undefined,
    true
  );
  const source = ts.createSourceFile("out.d.ts", text, ts.ScriptTarget.Latest, true);
  const printer = ts.createPrinter({ removeComments: true });
  const isPrivate = (node: ts.Node) =>
    ts.canHaveModifiers(node) &&
    (ts.getModifiers(node) ?? []).some((m) => m.kind === ts.SyntaxKind.PrivateKeyword);
  const dropPrivate = <T extends ts.Node>(root: T): T =>
    ts.transform(root, [
      (context) => (node) => {
        const visit = (n: ts.Node): ts.Node | undefined =>
          isPrivate(n) ? undefined : ts.visitEachChild(n, visit, context);
        return ts.visitNode(node, visit) as ts.Node;
      },
    ]).transformed[0] as T;
  return source.statements
    .filter((s) => !ts.isImportDeclaration(s))
    .filter((s) => {
      if (!names) return true;
      const name = (s as ts.DeclarationStatement).name;
      return name !== undefined && ts.isIdentifier(name) && names.includes(name.text);
    })
    .map((s) => printer.printNode(ts.EmitHint.Unspecified, dropPrivate(s), source))
    .join("\n");
}

describe("GatewayClient public API", () => {
  it("keeps its declarations", () => {
    expect(declarations("src/core/GatewayClient.ts")).toMatchSnapshot();
  });

  it("keeps the endpoint types it returns", () => {
    expect(
      declarations("src/types.ts", [
        "ContentEndpoints",
        "EndpointInfo",
        "ContentMetadata",
        "ContentType",
        "PlaybackAuth",
        "OutputEndpoint",
        "OutputCapabilities",
        "StreamProtocol",
      ])
    ).toMatchSnapshot();
  });

  it("keeps its module exports", () => {
    expect({
      exports: Object.keys(gatewayModule).sort(),
      defaultIsClass: gatewayModule.default === gatewayModule.GatewayClient,
      DEFAULT_GATEWAY_URL: gatewayModule.DEFAULT_GATEWAY_URL,
    }).toMatchSnapshot();
  });
});
