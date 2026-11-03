import { createDefaultStudioLayout, type StudioLayout } from "./studioLayout";

/** The parts of an ingest controller the keeper uses. */
export interface StudioLayoutHost {
  exportLayout(): Promise<StudioLayout | null>;
  importLayout(layout: StudioLayout): Promise<void>;
  isCompositorEnabled(): boolean;
}

/**
 * Which layout a studio shows, for a studio that switches between streams
 * without rebuilding its controller. The layout key names the stream (or
 * whatever the layout belongs to), independent of the layout object:
 *
 * - A compositor that starts shows its own last layout when it held the same
 *   key, also from another controller, and otherwise the layout given for the
 *   key.
 * - A new key replaces the layout of the running compositor in place with the
 *   layout given for it, or with the default layout when there is none or it
 *   does not apply. Sources stay.
 * - A report carries the key its layout was exported under, and a report for
 *   an older key is dropped, also when its export is already in flight.
 */
export class StudioLayoutKeeper<C extends StudioLayoutHost = StudioLayoutHost> {
  private key: string | undefined = undefined;
  private requested: StudioLayout | null = null;
  /** Bumped on every key change. */
  private epoch = 0;
  /** The controller whose compositor last held a layout, and that layout's key. */
  private holder: { controller: C; key: string | undefined } | null = null;
  /** Starts and switches still applying a layout; reports wait for them. */
  private applying: Promise<void> = Promise.resolve();
  private reportTimer: ReturnType<typeof setTimeout> | null = null;

  /** @param reportDelayMs Drags and typing settle for this long before a report. */
  constructor(private readonly reportDelayMs = 300) {}

  get layoutKey(): string | undefined {
    return this.key;
  }

  /**
   * The key and the layout to show for it; `layout` is only read when a
   * compositor starts or the key changes. Returns whether the key changed,
   * after which `apply` brings a running compositor to it.
   */
  setLayout(key: string | undefined, layout: StudioLayout | null | undefined): boolean {
    this.requested = layout ?? null;
    if (key === this.key) return false;
    this.key = key;
    this.epoch++;
    this.cancelReport();
    return true;
  }

  /**
   * Start the compositor of `controller` through `enable`, then apply the
   * layout for the current key. A stored layout that does not apply leaves
   * the started compositor as it is.
   */
  start(controller: C, enable: () => Promise<void>): Promise<void> {
    const key = this.key;
    const requested = this.requested;
    const last = this.holder;
    // Read before enable: on the same controller, the new compositor replaces
    // the stopped one whose layout this is.
    const carried =
      last && last.key === key ? last.controller.exportLayout().catch(() => null) : null;
    this.holder = { controller, key };
    const run = (async () => {
      await enable();
      const layout = (await carried) ?? requested;
      if (this.holder?.controller !== controller) return;
      if (this.key !== key) {
        await this.applyCurrent(controller);
        return;
      }
      if (layout) {
        await controller.importLayout(layout).catch((error: unknown) => {
          console.warn("Could not apply the studio layout:", error);
        });
      }
    })();
    this.track(run);
    return run;
  }

  /** Bring the running compositor of `controller` to the current key's layout. */
  apply(controller: C | null): Promise<void> {
    if (!controller) return Promise.resolve();
    const run = this.applying.then(() => this.applyCurrent(controller));
    this.track(run);
    return run;
  }

  /**
   * Report the layout of `controller` once changes have settled. A newer
   * report replaces this one, and a key change drops it.
   */
  scheduleReport(controller: C, report: (layout: StudioLayout, key: string | undefined) => void) {
    this.cancelReport();
    const epoch = this.epoch;
    const key = this.key;
    const current = () =>
      epoch === this.epoch && this.holder?.controller === controller && this.holder.key === key;
    this.reportTimer = setTimeout(() => {
      this.reportTimer = null;
      this.applying
        .then(() => (current() ? controller.exportLayout() : null))
        .then(
          (layout) => {
            if (!layout || !current()) return;
            try {
              report(layout, key);
            } catch (error) {
              console.warn("onLayoutChange failed:", error);
            }
          },
          (error: unknown) => console.warn("Could not export the studio layout:", error)
        );
    }, this.reportDelayMs);
  }

  /** Drop a report that is still waiting for changes to settle. */
  cancelReport(): void {
    if (this.reportTimer) clearTimeout(this.reportTimer);
    this.reportTimer = null;
  }

  private async applyCurrent(controller: C): Promise<void> {
    if (!controller.isCompositorEnabled()) return;
    if (this.holder?.controller === controller && this.holder.key === this.key) return;
    this.holder = { controller, key: this.key };
    const requested = this.requested;
    if (requested) {
      try {
        await controller.importLayout(requested);
        return;
      } catch (error) {
        console.warn("Could not apply the studio layout:", error);
      }
    }
    await controller.importLayout(createDefaultStudioLayout()).catch((error: unknown) => {
      console.warn("Could not reset the studio layout:", error);
    });
  }

  private track(run: Promise<void>): void {
    this.applying = Promise.all([this.applying, run.catch(() => {})]).then(() => {});
  }
}
