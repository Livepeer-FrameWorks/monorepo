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

const docsClasses: SkipperMarkdownClasses = {
  blockquote: "docs-skipper-message__quote",
  codeBlock: "docs-skipper-message__code",
  codeWrap: "docs-skipper-message__code-wrap",
  copyButton: "docs-skipper-message__copy",
  heading: {
    1: "docs-skipper-message__heading",
    2: "docs-skipper-message__heading",
    3: "docs-skipper-message__heading",
    4: "docs-skipper-message__heading",
    5: "docs-skipper-message__heading",
    6: "docs-skipper-message__heading",
  },
  hr: "docs-skipper-message__hr",
  inlineCode: "docs-skipper-message__inline",
  link: "docs-skipper-message__link",
  list: "docs-skipper-message__list",
  paragraph: "docs-skipper-message__paragraph",
  table: "docs-skipper-message__table",
  tableCell: "docs-skipper-message__td",
  tableHeader: "docs-skipper-message__th",
  tableWrap: "docs-skipper-message__table-wrap",
};

export function renderSkipperMarkdown(value: string, classes = docsClasses) {
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

    if (/^```(\S*)?\s*$/.test(line)) {
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
      const tableLines: string[] = [lines[i] ?? "", lines[i + 1] ?? ""];
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
  return `<div class="${classes.codeWrap}"><pre class="${classes.codeBlock}"><code>${escapeHtml(code.trim())}</code></pre><button data-copy-index="${index}" class="${classes.copyButton}" type="button">Copy</button></div>`;
}

function renderList(tag: "ol" | "ul", items: string[], classes: SkipperMarkdownClasses) {
  return `<${tag} class="${classes.list}">${items
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
