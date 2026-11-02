import { useState } from "react";
import { CopyButton } from "./CopyButton";
import { SNIPPET_FRAMEWORKS, embedSnippet, type SnippetFramework } from "./snippets";
import type { PlayerInput } from "./source";

export function EmbedSnippets({ input }: { input: PlayerInput }) {
  const [framework, setFramework] = useState<SnippetFramework>("React");
  const code = embedSnippet(framework, input);

  return (
    <section className="fwt-panel" aria-labelledby="fwt-embed-title">
      <div className="fwt-panel__header">
        <h3 id="fwt-embed-title" className="fwt-panel__title">
          Embed this player
        </h3>
        <CopyButton text={code} label="Copy code" />
      </div>
      <div className="fwt-tabs fwt-tabs--small" role="tablist" aria-label="Framework">
        {SNIPPET_FRAMEWORKS.map((name) => (
          <button
            key={name}
            type="button"
            role="tab"
            aria-selected={framework === name}
            className="fwt-tabs__tab"
            onClick={() => setFramework(name)}
          >
            {name}
          </button>
        ))}
      </div>
      <pre className="fwt-code" role="tabpanel">
        <code>{code}</code>
      </pre>
    </section>
  );
}
