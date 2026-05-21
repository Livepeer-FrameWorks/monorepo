type SkipperMarkdownClasses = {
  blockquote: string;
  codeBlock: string;
  codeWrap: string;
  copyButton: string;
  heading: Record<number, string>;
  hr: string;
  inlineCode: string;
  link: string;
  list: string;
  paragraph: string;
  table: string;
  tableCell: string;
  tableHeader: string;
  tableWrap: string;
};

const defaultClasses: SkipperMarkdownClasses = {
  blockquote: "my-2 border-l-2 border-border pl-3 text-muted-foreground",
  codeBlock:
    "overflow-x-auto rounded-md border border-border bg-muted/40 p-3 pr-10 text-xs text-foreground",
  codeWrap: "group/code relative mt-3",
  copyButton:
    "absolute right-2 top-2 rounded-md border border-border bg-background/80 p-1 text-muted-foreground opacity-0 transition hover:text-foreground group-hover/code:opacity-100",
  heading: {
    1: "mt-4 mb-1 text-base font-semibold text-foreground",
    2: "mt-3 mb-1 font-semibold text-foreground",
    3: "mt-3 mb-1 text-sm font-semibold text-foreground",
    4: "mt-3 mb-1 text-xs font-semibold text-foreground",
    5: "mt-3 mb-1 text-xs font-semibold text-foreground",
    6: "mt-3 mb-1 text-xs font-semibold text-foreground",
  },
  hr: "my-3 border-border",
  inlineCode: "rounded bg-muted/60 px-1 py-0.5 text-xs",
  link: "text-primary underline underline-offset-4 hover:text-primary/80",
  list: "my-2 space-y-1 pl-5",
  paragraph: "my-2 first:mt-0 last:mb-0",
  table: "min-w-full border-collapse text-xs",
  tableCell: "border border-border px-2 py-1 align-top",
  tableHeader: "border border-border bg-muted/50 px-2 py-1 text-left font-semibold align-top",
  tableWrap: "my-3 overflow-x-auto rounded-md border border-border",
};

export function renderSkipperMarkdown(value: string, classes = defaultClasses) {
  const lines = value.replace(/\r\n/g, "\n").split("\n");
  const html: string[] = [];
  let i = 0;
  let copyIndex = 0;

  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      i++;
      continue;
    }

    const fence = line.match(/^```(\S*)?\s*$/);
    if (fence) {
      const code: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i] ?? "")) {
        code.push(lines[i] ?? "");
        i++;
      }
      if (i < lines.length) i++;
      html.push(renderCodeBlock(code.join("\n"), copyIndex++, classes));
      continue;
    }

    if (isTableStart(lines, i)) {
      const tableLines: string[] = [];
      tableLines.push(lines[i] ?? "", lines[i + 1] ?? "");
      i += 2;
      while (i < lines.length && isTableRow(lines[i] ?? "")) {
        tableLines.push(lines[i] ?? "");
        i++;
      }
      html.push(renderTable(tableLines, classes));
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      const level = heading[1].length;
      html.push(
        `<h${level} class="${classes.heading[level]}">${renderInline(heading[2], classes)}</h${level}>`
      );
      i++;
      continue;
    }

    if (/^\s*---+\s*$/.test(line)) {
      html.push(`<hr class="${classes.hr}">`);
      i++;
      continue;
    }

    if (/^\s*>\s?/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i] ?? "")) {
        quote.push((lines[i] ?? "").replace(/^\s*>\s?/, ""));
        i++;
      }
      html.push(
        `<blockquote class="${classes.blockquote}">${renderBlocksAsParagraphs(quote, classes)}</blockquote>`
      );
      continue;
    }

    if (/^\s*[-*+]\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i] ?? "")) {
        items.push((lines[i] ?? "").replace(/^\s*[-*+]\s+/, ""));
        i++;
      }
      html.push(renderList("ul", items, classes));
      continue;
    }

    if (/^\s*\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i] ?? "")) {
        items.push((lines[i] ?? "").replace(/^\s*\d+\.\s+/, ""));
        i++;
      }
      html.push(renderList("ol", items, classes));
      continue;
    }

    const paragraph: string[] = [line];
    i++;
    while (i < lines.length && lines[i]?.trim() !== "" && !isBlockStart(lines, i)) {
      paragraph.push(lines[i] ?? "");
      i++;
    }
    html.push(
      `<p class="${classes.paragraph}">${renderInline(paragraph.join("\n"), classes).replace(/\n/g, "<br>")}</p>`
    );
  }

  return html.join("");
}

function renderBlocksAsParagraphs(lines: string[], classes: SkipperMarkdownClasses) {
  return lines
    .join("\n")
    .split(/\n{2,}/)
    .map(
      (block) =>
        `<p class="${classes.paragraph}">${renderInline(block, classes).replace(/\n/g, "<br>")}</p>`
    )
    .join("");
}

function renderCodeBlock(code: string, index: number, classes: SkipperMarkdownClasses) {
  return `<div class="${classes.codeWrap}"><pre class="${classes.codeBlock}"><code>${escapeHtml(
    code.trim()
  )}</code></pre><button data-copy-index="${index}" class="${classes.copyButton}" aria-label="Copy code"><svg class="h-3.5 w-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg></button></div>`;
}

function renderList(tag: "ol" | "ul", items: string[], classes: SkipperMarkdownClasses) {
  const marker = tag === "ol" ? "list-decimal" : "list-disc";
  return `<${tag} class="${classes.list} ${marker}">${items
    .map((item) => `<li>${renderInline(item, classes)}</li>`)
    .join("")}</${tag}>`;
}

function isBlockStart(lines: string[], index: number) {
  const line = lines[index] ?? "";
  return (
    /^```/.test(line) ||
    isTableStart(lines, index) ||
    /^(#{1,6})\s+/.test(line) ||
    /^\s*---+\s*$/.test(line) ||
    /^\s*>\s?/.test(line) ||
    /^\s*[-*+]\s+/.test(line) ||
    /^\s*\d+\.\s+/.test(line)
  );
}

function isTableStart(lines: string[], index: number) {
  return isTableRow(lines[index] ?? "") && isTableSeparator(lines[index + 1] ?? "");
}

function isTableRow(line: string) {
  return line.includes("|") && line.trim().replaceAll("|", "").trim() !== "";
}

function isTableSeparator(line: string) {
  const cells = splitTableRow(line);
  return cells.length > 1 && cells.every((cell) => /^:?-{3,}:?$/.test(cell.trim()));
}

function splitTableRow(line: string) {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function renderTable(lines: string[], classes: SkipperMarkdownClasses) {
  const headers = splitTableRow(lines[0] ?? "");
  const rows = lines.slice(2).map(splitTableRow);
  return `<div class="${classes.tableWrap}"><table class="${classes.table}"><thead><tr>${headers
    .map((cell) => `<th class="${classes.tableHeader}">${renderInline(cell, classes)}</th>`)
    .join("")}</tr></thead><tbody>${rows
    .map(
      (row) =>
        `<tr>${headers
          .map(
            (_, index) =>
              `<td class="${classes.tableCell}">${renderInline(row[index] ?? "", classes)}</td>`
          )
          .join("")}</tr>`
    )
    .join("")}</tbody></table></div>`;
}

function renderInline(value: string, classes: SkipperMarkdownClasses) {
  const codeSpans: string[] = [];
  let working = value.replace(/`([^`\n]+)`/g, (_match, code) => {
    const index = codeSpans.length;
    codeSpans.push(`<code class="${classes.inlineCode}">${escapeHtml(code)}</code>`);
    return `\u0000CODE_${index}\u0000`;
  });

  working = escapeHtml(working);
  working = working.replace(
    /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
    (_match, label, href) =>
      `<a class="${classes.link}" href="${href}" target="_blank" rel="noreferrer">${label}</a>`
  );
  working = working.replace(/\*\*([^*\n][\s\S]*?[^*\n])\*\*/g, "<strong>$1</strong>");
  working = working.replace(/__([^_\n][\s\S]*?[^_\n])__/g, "<strong>$1</strong>");
  working = working.replace(/\*([^*\n]+)\*/g, "<em>$1</em>");
  working = working.replace(/_([^_\n]+)_/g, "<em>$1</em>");

  codeSpans.forEach((span, index) => {
    working = working.replace(`\u0000CODE_${index}\u0000`, span);
  });

  return working;
}

function escapeHtml(value: string) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}
