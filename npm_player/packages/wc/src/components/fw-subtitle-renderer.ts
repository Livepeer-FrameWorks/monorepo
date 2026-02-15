import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";

interface SubtitleCue {
  id?: string;
  startTime: number;
  endTime: number;
  text: string;
}

@customElement("fw-subtitle-renderer")
export class FwSubtitleRenderer extends LitElement {
  @property({ type: Number }) currentTime = 0;
  @property({ type: Boolean }) enabled = false;
  @property({ attribute: false }) cues: SubtitleCue[] = [];

  static styles = css`
    :host {
      display: contents;
    }
    .subtitle {
      position: absolute;
      bottom: 5%;
      left: 50%;
      transform: translateX(-50%);
      display: inline-block;
      max-width: 90%;
      padding: 0.5em 1em;
      border-radius: 4px;
      background: rgb(0 0 0 / 0.75);
      color: white;
      font-size: 1.5rem;
      font-family:
        system-ui,
        -apple-system,
        sans-serif;
      text-shadow: 2px 2px 4px rgb(0 0 0 / 0.5);
      white-space: pre-wrap;
      text-align: center;
      z-index: 15;
      pointer-events: none;
    }
  `;

  private get _activeCue(): string | null {
    if (!this.enabled || !this.cues.length) return null;
    const t = this.currentTime * 1000; // Convert to ms
    for (const cue of this.cues) {
      if (t >= cue.startTime && t <= cue.endTime) return cue.text;
    }
    return null;
  }

  protected render() {
    const text = this._activeCue;
    if (!text) return nothing;
    return html`<span class="subtitle" aria-live="polite">${text}</span>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-subtitle-renderer": FwSubtitleRenderer;
  }
}
