/**
 * Base for the sub-elements that work on a `<fw-streamcrafter>`: the studio
 * given as the `studio` property, the element whose id is in `for`, or the
 * nearest enclosing `<fw-streamcrafter>`.
 *
 * A studio announces each render with a `fw-sc-studio-update` event that
 * bubbles to the document, so a sub-element finds a studio that mounts after it
 * and renders again whenever the studio does.
 */
import { LitElement, type PropertyValues } from "lit";
import { property } from "lit/decorators.js";
import {
  createStudioTranslator,
  type StudioTranslateFn,
} from "@livepeer-frameworks/streamcrafter-core";
import type { FwStreamCrafter } from "./fw-streamcrafter.js";

export const STUDIO_UPDATE_EVENT = "fw-sc-studio-update";

const fallbackTranslator = createStudioTranslator({ locale: "en" });

export class StudioBoundElement extends LitElement {
  /** ID of a `<fw-streamcrafter>` to bind to. */
  @property({ type: String, attribute: "for" }) for = "";
  /** The studio to bind to; set by `<fw-streamcrafter>` for its own sub-elements. */
  @property({ attribute: false }) studio: FwStreamCrafter | null = null;

  private _found: FwStreamCrafter | null = null;

  private _handleStudioUpdate = (event: Event) => {
    const target = event.composedPath()[0];
    if (!this._isMyStudio(target)) return;
    this._found = target as FwStreamCrafter;
    this.requestUpdate();
  };

  /** The bound studio, or null until one is found. */
  protected get boundStudio(): FwStreamCrafter | null {
    return this.studio ?? this._found ?? this._lookUp();
  }

  protected get t(): StudioTranslateFn {
    return this.boundStudio?.t ?? fallbackTranslator;
  }

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener(STUDIO_UPDATE_EVENT, this._handleStudioUpdate);
    this._found = this._lookUp();
  }

  protected willUpdate(changed: PropertyValues) {
    super.willUpdate(changed);
    if (changed.has("for") || changed.has("studio")) this._found = this._lookUp();
  }

  disconnectedCallback() {
    document.removeEventListener(STUDIO_UPDATE_EVENT, this._handleStudioUpdate);
    this._found = null;
    super.disconnectedCallback();
  }

  private _lookUp(): FwStreamCrafter | null {
    if (this.studio) return this.studio;
    if (this.for) {
      const el = document.getElementById(this.for);
      return el?.localName === "fw-streamcrafter" ? (el as FwStreamCrafter) : null;
    }
    return this.closest("fw-streamcrafter") as FwStreamCrafter | null;
  }

  private _isMyStudio(target: EventTarget | undefined): boolean {
    if (!(target instanceof HTMLElement) || target.localName !== "fw-streamcrafter") return false;
    if (this.studio) return target === this.studio;
    if (this.for) return target.id === this.for;
    return target.contains(this);
  }
}
