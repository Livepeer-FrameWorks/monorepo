import { useId, useState } from "react";
import { CopyButton } from "./CopyButton";
import { SNIPPET_FRAMEWORKS, embedSnippet, type SnippetFramework } from "./snippets";
import type { PlayerInput } from "./source";
import { tabListKeyDown } from "./tabs";

export function EmbedSnippets({ input }: { input: PlayerInput }) {
  const id = useId();
  const [framework, setFramework] = useState<SnippetFramework>("React");
  const code = embedSnippet(framework, input);
  const titleId = `${id}-title`;
  const panelId = `${id}-panel`;
  const tabId = (name: SnippetFramework) => `${id}-${name.replace(/\s+/g, "-")}-tab`;

  return (
    <section className="fwt-panel" aria-labelledby={titleId}>
      <div className="fwt-panel__header">
        <h3 id={titleId} className="fwt-panel__title">
          Embed this player
        </h3>
        <CopyButton text={code} label="Copy code" />
      </div>
      <div
        className="fwt-tabs fwt-tabs--small"
        role="tablist"
        aria-label="Framework"
        onKeyDown={tabListKeyDown(SNIPPET_FRAMEWORKS, framework, setFramework)}
      >
        {SNIPPET_FRAMEWORKS.map((name) => (
          <button
            key={name}
            id={tabId(name)}
            type="button"
            role="tab"
            aria-selected={framework === name}
            aria-controls={panelId}
            tabIndex={framework === name ? 0 : -1}
            className="fwt-tabs__tab"
            onClick={() => setFramework(name)}
          >
            {name}
          </button>
        ))}
      </div>
      <pre id={panelId} className="fwt-code" role="tabpanel" aria-labelledby={tabId(framework)}>
        <code>{code}</code>
      </pre>
    </section>
  );
}
