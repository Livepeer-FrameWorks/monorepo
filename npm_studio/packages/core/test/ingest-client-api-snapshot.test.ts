import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

import * as ingestModule from "../src/core/IngestClient";

const root = fileURLToPath(new URL("..", import.meta.url));

/**
 * The declarations TypeScript emits for a source file, restricted to the
 * named declarations when names are given, without comments, imports, or
 * private members: the snapshot guards signatures, not documentation or
 * implementation details.
 */
function declarations(file: string, names?: string[]): string {
  const config = ts.getParsedCommandLineOfConfigFile(
    `${root}tsconfig.json`,
    {},
    { ...ts.sys, onUnRecoverableConfigFileDiagnostic: () => undefined }
  );
  if (!config) throw new Error("tsconfig.json did not parse");
  const path = `${root}${file}`;
  const input = ts.sys.readFile(path);
  if (input === undefined) throw new Error(`Source file not found: ${file}`);
  // Signature snapshots need declaration emit; the type-check target checks the dependency graph.
  const emitted = ts.transpileDeclaration(input, {
    fileName: path,
    compilerOptions: {
      ...config.options,
      noEmit: false,
      declaration: true,
      emitDeclarationOnly: true,
    },
    reportDiagnostics: true,
  });
  const errors = emitted.diagnostics?.filter(
    (diagnostic) => diagnostic.category === ts.DiagnosticCategory.Error
  );
  if (errors?.length) {
    throw new Error(
      errors
        .map((diagnostic) => ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"))
        .join("\n")
    );
  }
  const source = ts.createSourceFile("out.d.ts", emitted.outputText, ts.ScriptTarget.Latest, true);
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

describe("IngestClient public API", () => {
  it("keeps its declarations", () => {
    expect(declarations("src/core/IngestClient.ts")).toMatchSnapshot();
  });

  it("keeps the configuration, event, and endpoint types", () => {
    expect(
      declarations("src/types.ts", [
        "IngestEndpoint",
        "IngestMetadata",
        "IngestEndpoints",
        "IngestClientConfig",
        "IngestClientStatus",
        "IngestClientEvents",
      ])
    ).toMatchSnapshot();
  });

  it("keeps its module exports", () => {
    expect(Object.keys(ingestModule).sort()).toMatchSnapshot();
  });
});
