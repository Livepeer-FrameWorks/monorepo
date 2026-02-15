import { LitElement, css, html, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { styleMap } from "lit/directives/style-map.js";

interface SubtitleCue {
  id?: string;
  text: string;
  startTime: number;
  endTime: number;
  lang?: string;
}

interface MetaTrackEvent {
  type: string;
  data: unknown;
}

interface SubtitleStyle {
  fontSize?: string;
  fontFamily?: string;
  color?: string;
  backgroundColor?: string;
  textShadow?: string;
  bottom?: string;
  maxWidth?: string;
  padding?: string;
  borderRadius?: string;
}

const DEFAULT_STYLE: Required<SubtitleStyle> = {
  fontSize: "1.5rem",
  fontFamily: "system-ui, -apple-system, sans-serif",
  color: "white",
  backgroundColor: "rgba(0, 0, 0, 0.75)",
  textShadow: "2px 2px 4px rgba(0, 0, 0, 0.5)",
  bottom: "5%",
  maxWidth: "90%",
  padding: "0.5em 1em",
  borderRadius: "4px",
};

@customElement("fw-subtitle-renderer")
export class FwSubtitleRenderer extends LitElement {
  @property({ type: Number }) currentTime = 0;
  @property({ type: Boolean }) enabled = true;
  @property({ attribute: false }) cues: SubtitleCue[] = [];
  @property({ attribute: false })
  subscribeToMetaTrack?: (trackId: string, callback: (event: MetaTrackEvent) => void) => () => void;
  @property({ type: String, attribute: "meta-track-id" }) metaTrackId?: string;
  @property({ attribute: false }) subtitleStyle?: SubtitleStyle;
  @property({ type: String, attribute: "class-name" }) className = "";

  @state() private _liveCues: SubtitleCue[] = [];
  @state() private _displayedText = "";

  private _unsubscribe: (() => void) | null = null;

  static styles = css`
    :host {
      display: contents;
    }

    .subtitle-container {
      position: absolute;
      left: 50%;
      transform: translateX(-50%);
      z-index: 30;
      text-align: center;
      pointer-events: none;
    }

    .subtitle-text {
      display: inline-block;
      white-space: pre-wrap;
    }
  `;

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._teardownSubscription();
  }

  protected updated(changed: Map<string, unknown>): void {
    if (
      changed.has("enabled") ||
      changed.has("subscribeToMetaTrack") ||
      changed.has("metaTrackId")
    ) {
      this._syncSubscription();
    }

    if (changed.has("currentTime") || changed.has("_liveCues")) {
      this._pruneExpiredLiveCues();
    }

    if (
      changed.has("enabled") ||
      changed.has("currentTime") ||
      changed.has("cues") ||
      changed.has("_liveCues")
    ) {
      this._syncDisplayedCue();
    }
  }

  private _syncSubscription(): void {
    this._teardownSubscription();

    if (!this.enabled || !this.subscribeToMetaTrack || !this.metaTrackId) {
      return;
    }

    this._unsubscribe = this.subscribeToMetaTrack(this.metaTrackId, this._handleMetaEvent);
  }

  private _teardownSubscription(): void {
    if (this._unsubscribe) {
      this._unsubscribe();
      this._unsubscribe = null;
    }
  }

  private _handleMetaEvent = (event: MetaTrackEvent): void => {
    if (event.type !== "subtitle") {
      return;
    }

    const cue = this._parseSubtitleCue(event.data);
    if (!cue) {
      return;
    }

    this._liveCues = (() => {
      const existing = this._liveCues.find((value) => value.id === cue.id);
      if (existing) {
        return this._liveCues;
      }
      return [...this._liveCues, cue].slice(-50);
    })();
  };

  private _parseSubtitleCue(data: unknown): SubtitleCue | null {
    if (typeof data !== "object" || data === null) {
      return null;
    }

    const obj = data as Record<string, unknown>;
    const text = typeof obj.text === "string" ? obj.text : String(obj.text ?? "");
    if (!text) {
      return null;
    }

    const rawStart =
      "startTime" in obj ? Number(obj.startTime) : "start" in obj ? Number(obj.start) : 0;
    const rawEnd =
      "endTime" in obj ? Number(obj.endTime) : "end" in obj ? Number(obj.end) : Infinity;
    const startTime = Number.isFinite(rawStart) ? rawStart : 0;
    const endTime = Number.isFinite(rawEnd) ? rawEnd : Infinity;
    const id = typeof obj.id === "string" ? obj.id : String(Date.now() + Math.random());

    return {
      id,
      text,
      startTime,
      endTime,
      lang: typeof obj.lang === "string" ? obj.lang : undefined,
    };
  }

  private _getAllCues(): SubtitleCue[] {
    return [...(this.cues ?? []), ...this._liveCues];
  }

  private _syncDisplayedCue(): void {
    if (!this.enabled) {
      if (this._displayedText) {
        this._displayedText = "";
      }
      return;
    }

    const currentTimeMs = this.currentTime * 1000;
    const activeCue = this._getAllCues().find(
      (cue) => currentTimeMs >= cue.startTime && currentTimeMs < cue.endTime
    );
    const nextText = activeCue?.text ?? "";

    if (nextText !== this._displayedText) {
      this._displayedText = nextText;
    }
  }

  private _pruneExpiredLiveCues(): void {
    if (this._liveCues.length === 0) {
      return;
    }

    const currentTimeMs = this.currentTime * 1000;
    const filtered = this._liveCues.filter((cue) => {
      const endTime = cue.endTime === Infinity ? cue.startTime + 10000 : cue.endTime;
      return endTime >= currentTimeMs - 30000;
    });

    if (filtered.length !== this._liveCues.length) {
      this._liveCues = filtered;
    }
  }

  protected render() {
    if (!this.enabled || !this._displayedText) {
      return nothing;
    }

    const mergedStyle: Required<SubtitleStyle> = {
      ...DEFAULT_STYLE,
      ...(this.subtitleStyle ?? {}),
    };

    return html`
      <div
        class="subtitle-container ${this.className}"
        style=${styleMap({
          bottom: mergedStyle.bottom,
          maxWidth: mergedStyle.maxWidth,
        })}
        role="region"
        aria-live="polite"
        aria-label="Subtitles"
      >
        <span
          class="subtitle-text"
          style=${styleMap({
            fontSize: mergedStyle.fontSize,
            fontFamily: mergedStyle.fontFamily,
            color: mergedStyle.color,
            backgroundColor: mergedStyle.backgroundColor,
            textShadow: mergedStyle.textShadow,
            padding: mergedStyle.padding,
            borderRadius: mergedStyle.borderRadius,
          })}
        >
          ${this._displayedText}
        </span>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-subtitle-renderer": FwSubtitleRenderer;
  }
}
