#!/usr/bin/env node

import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const docsRoot = path.resolve("src/content/docs");
const schemaPath = path.resolve("../pkg/graphql/schema.graphql");

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walk(fullPath)));
    } else if (entry.isFile() && entry.name.endsWith(".mdx")) {
      files.push(fullPath);
    }
  }
  return files;
}

function rootFields(schema, typeName) {
  const match = schema.match(new RegExp(`type ${typeName} \\{([\\s\\S]*?)\\n\\}`));
  if (!match) {
    throw new Error(`type ${typeName} not found in schema`);
  }
  const fields = new Set();
  for (const line of match[1].split("\n")) {
    const field = line.match(/^  ([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(|:)/)?.[1];
    if (field) {
      fields.add(field);
    }
  }
  return fields;
}

function fencedGraphqlBlocks(content) {
  const blocks = [];
  const fenceRE = /```(?:graphql|gql)\s*\n([\s\S]*?)^```/gm;
  for (const match of content.matchAll(fenceRE)) {
    blocks.push(match[1]);
  }
  return blocks;
}

function operationType(block) {
  const withoutLeadingComments = block.trimStart().replace(/^(?:#[^\n]*\n\s*)+/, "");
  const firstWord = withoutLeadingComments.match(/^(query|mutation|subscription)\b/)?.[1];
  return firstWord ?? "query";
}

function operationBlocks(block) {
  const opRE = /(?:^|\n)\s*(?:#[^\n]*\n\s*)*(query|mutation|subscription)\b/g;
  const matches = [...block.matchAll(opRE)];
  if (matches.length <= 1) {
    return [block];
  }

  return matches.map((match, index) => {
    const start = match.index ?? 0;
    const end = matches[index + 1]?.index ?? block.length;
    return block.slice(start, end);
  });
}

function topLevelFields(block) {
  const start = block.indexOf("{");
  if (start < 0) {
    return [];
  }

  const fields = [];
  let depth = 0;
  let parenDepth = 0;
  let inString = false;
  let stringDelimiter = "";
  let escaped = false;
  let lineComment = false;

  for (let i = start; i < block.length; i++) {
    const char = block[i];
    const next = block[i + 1];

    if (lineComment) {
      if (char === "\n") {
        lineComment = false;
      }
      continue;
    }
    if (inString) {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === stringDelimiter) {
        inString = false;
      }
      continue;
    }
    if (char === "#") {
      lineComment = true;
      continue;
    }
    if (char === '"' || char === "'") {
      inString = true;
      stringDelimiter = char;
      continue;
    }
    if (char === "{") {
      depth++;
      continue;
    }
    if (char === "}") {
      depth--;
      continue;
    }
    if (char === "(") {
      parenDepth++;
      continue;
    }
    if (char === ")") {
      parenDepth = Math.max(0, parenDepth - 1);
      continue;
    }
    if (depth !== 1 || parenDepth !== 0 || !/[A-Za-z_]/.test(char)) {
      continue;
    }

    const ident = block.slice(i).match(/^([A-Za-z_][A-Za-z0-9_]*)/)?.[1];
    if (!ident || ident === "fragment" || ident === "on") {
      continue;
    }

    let j = i + ident.length;
    while (/\s/.test(block[j] ?? "")) {
      j++;
    }
    if (block[j] === ":" || block[j] === "(" || block[j] === "{") {
      if (block[j] === ":") {
        const actual = block.slice(j + 1).match(/\s*([A-Za-z_][A-Za-z0-9_]*)/)?.[1] ?? ident;
        fields.push(actual);
        i = j + actual.length;
      } else {
        fields.push(ident);
        i += ident.length - 1;
      }
    }
  }

  return fields;
}

const schema = await readFile(schemaPath, "utf8");
const fieldsByType = {
  query: rootFields(schema, "Query"),
  mutation: rootFields(schema, "Mutation"),
  subscription: rootFields(schema, "Subscription"),
};
fieldsByType.query.add("__schema");
fieldsByType.query.add("__type");
const files = await walk(docsRoot);
const failures = [];
let checked = 0;

for (const file of files) {
  const content = await readFile(file, "utf8");
  for (const block of fencedGraphqlBlocks(content)) {
    for (const operation of operationBlocks(block)) {
      checked++;
      const opType = operationType(operation);
      const allowedFields = fieldsByType[opType];
      for (const field of topLevelFields(operation)) {
        if (!allowedFields.has(field)) {
          failures.push({
            source: path.relative(process.cwd(), file),
            type: opType,
            field,
          });
        }
      }
    }
  }
}

if (failures.length > 0) {
  console.error("GraphQL docs examples reference unknown root fields:");
  for (const failure of failures) {
    console.error(`- ${failure.source}: ${failure.type}.${failure.field}`);
  }
  process.exit(1);
}

console.log(`Checked ${checked} fenced GraphQL example(s) against schema root fields.`);
