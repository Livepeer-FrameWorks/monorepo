import fs from "node:fs";
import path from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";
import { studioCodeExamples } from "./studioSnippets";

const studioRoot = path.resolve(__dirname, "../../../../../npm_studio/packages");
const examples = studioCodeExamples("https://api.example.com/graphql");

/** Type-checks one snippet against the package sources and returns its errors. */
function typeErrors(snippet: string, fileName: string): string[] {
  const file = path.join(studioRoot, fileName);
  const cssShim = path.join(studioRoot, "__snippet_css__.d.ts");
  const virtual = new Map([
    [file, snippet],
    [cssShim, 'declare module "*.css";'],
  ]);
  const options: ts.CompilerOptions = {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    jsx: ts.JsxEmit.ReactJSX,
    strict: true,
    noEmit: true,
    skipLibCheck: true,
    lib: ["lib.es2022.d.ts", "lib.dom.d.ts", "lib.dom.iterable.d.ts"],
    paths: {
      "@livepeer-frameworks/streamcrafter-core": [path.join(studioRoot, "core/src/index.ts")],
      "@livepeer-frameworks/streamcrafter-react": [path.join(studioRoot, "react/src/index.tsx")],
    },
  };
  const host = ts.createCompilerHost(options);
  const readFile = host.readFile.bind(host);
  const fileExists = host.fileExists.bind(host);
  const getSourceFile = host.getSourceFile.bind(host);
  host.readFile = (name) => virtual.get(name) ?? readFile(name);
  host.fileExists = (name) => virtual.has(name) || fileExists(name);
  host.getSourceFile = (name, version) => {
    const text = virtual.get(name);
    return text !== undefined
      ? ts.createSourceFile(name, text, version, true)
      : getSourceFile(name, version);
  };
  const program = ts.createProgram([file, cssShim], options, host);
  const source = program.getSourceFile(file)!;
  return [
    ...program.getSyntacticDiagnostics(source),
    ...program.getSemanticDiagnostics(source),
  ].map((d) => ts.flattenDiagnosticMessageText(d.messageText, "\n"));
}

/** Names a snippet imports from a package, and the props or attributes it sets on a tag. */
function importedNames(snippet: string, pkg: string): string[] {
  const match = snippet.match(new RegExp(`import \\{([^}]+)\\} from '${pkg}'`));
  return (match?.[1] ?? "").split(",").map((name) => name.trim());
}

function tagAttributes(snippet: string, tag: string): string[] {
  const match = snippet.match(new RegExp(`<${tag}([^>]*)>`));
  return [...(match?.[1] ?? "").matchAll(/([a-zA-Z-]+)=/g)].map((m) => m[1]);
}

describe("SDK page StreamCrafter examples", () => {
  it("React example type-checks against the React package", () => {
    expect(typeErrors(examples.react, "react/__snippet__.tsx")).toEqual([]);
  }, 60_000);

  it("vanilla example type-checks against the core package", () => {
    expect(typeErrors(examples.vanilla, "core/__snippet__.ts")).toEqual([]);
  }, 60_000);

  it("Svelte example uses an export and props the Svelte component has", () => {
    const index = fs.readFileSync(path.join(studioRoot, "svelte/src/index.ts"), "utf8");
    const component = fs.readFileSync(
      path.join(studioRoot, "svelte/src/StreamCrafter.svelte"),
      "utf8"
    );
    const props = component.slice(component.indexOf("interface Props"), component.indexOf("let {"));

    for (const name of importedNames(
      examples.svelte,
      "@livepeer-frameworks/streamcrafter-svelte"
    )) {
      expect(index).toMatch(new RegExp(`as ${name} \\}`));
    }
    for (const prop of tagAttributes(examples.svelte, "StreamCrafter")) {
      expect(props).toMatch(new RegExp(`\\b${prop}\\?:`));
    }
  });

  it("web component example uses attributes the element declares", () => {
    const element = fs.readFileSync(
      path.join(studioRoot, "wc/src/components/fw-streamcrafter.ts"),
      "utf8"
    );
    for (const attribute of tagAttributes(examples.wc, "fw-streamcrafter")) {
      expect(element).toContain(`attribute: "${attribute}"`);
    }
  });

  it("would catch an example with an export or a prop the package does not have", () => {
    const badExport = examples.react.replace("{ StreamCrafter }", "{ StreamCrafter, Preview }");
    expect(typeErrors(badExport, "react/__snippet__.tsx").join("\n")).toMatch(/Preview/);

    const badProp = examples.react.replace("gatewayUrl=", "ingestUrl=");
    expect(typeErrors(badProp, "react/__snippet__.tsx").join("\n")).toMatch(/ingestUrl/);

    const badCall = examples.vanilla.replace("studio.startCamera()", "studio.addCameraSource()");
    expect(typeErrors(badCall, "core/__snippet__.ts").join("\n")).toMatch(/addCameraSource/);
  }, 60_000);
});
