/**
 * Blueprint System — Declarative, composable UI for the vanilla player.
 *
 * Blueprints are factory functions that receive a `BlueprintContext` and return
 * an HTMLElement (or null to skip). They wire reactivity via `ctx.subscribe.on()`.
 *
 * Structure descriptors define the control layout as a JSON tree.
 * The StructureBuilder walks the tree and calls the matching blueprint for each node.
 */

import type { ReactiveState } from "./ReactiveState";
import type { PlayerInstance } from "./createPlayer";
import type { StreamInfo } from "../core/PlayerInterface";
import type { CreatePlayerConfig } from "./createPlayer";

export interface BlueprintContext {
  /** The underlying <video> element (null until ready) */
  video: HTMLVideoElement | null;
  /** Per-property reactive subscriptions */
  subscribe: ReactiveState;
  /** Full player API (Q/M/S) */
  api: PlayerInstance;
  /** Fullscreen helpers */
  fullscreen: {
    readonly supported: boolean;
    readonly active: boolean;
    toggle(): Promise<void>;
    request(): Promise<void>;
    exit(): Promise<void>;
  };
  /** Picture-in-Picture helpers */
  pip: {
    readonly supported: boolean;
    readonly active: boolean;
    toggle(): Promise<void>;
  };
  /** Stream info (sources, tracks) — null until resolved */
  info: StreamInfo | null;
  /** The original config passed to createPlayer */
  options: CreatePlayerConfig;
  /** The player's container element */
  container: HTMLElement;
  /** i18n translation helper */
  translate(key: string, fallback?: string): string;
  /** Build an SVG icon by name */
  buildIcon(name: string, size?: number): SVGElement | null;
  /** Debug logger (no-op when debug is off) */
  log(msg: string): void;
  /** Timer utilities (auto-cleaned on destroy) */
  timers: {
    setTimeout(fn: () => void, ms: number): number;
    clearTimeout(id: number): void;
    setInterval(fn: () => void, ms: number): number;
    clearInterval(id: number): void;
  };
}

/** A blueprint factory returns an HTMLElement (or null to skip this slot) */
export type BlueprintFactory = (ctx: BlueprintContext) => HTMLElement | null;

/** Named collection of blueprint factories */
export type BlueprintMap = Record<string, BlueprintFactory>;

/**
 * Declarative layout descriptor for building the player UI.
 *
 * Each node has a `type` matching a blueprint name.
 * Nodes can have static children, conditional rendering, and CSS classes.
 */
export interface StructureDescriptor {
  /** Blueprint type name (e.g. "play", "progress", "controls") */
  type: string;
  /** Extra CSS classes to add to the element */
  classes?: string[];
  /** Inline style overrides */
  style?: Record<string, string>;
  /** Static child descriptors */
  children?: StructureDescriptor[];
  /** Conditional: only render if this returns true */
  if?: (ctx: BlueprintContext) => boolean;
  /** Render this descriptor when `if` is true */
  then?: StructureDescriptor;
  /** Render this descriptor when `if` is false */
  else?: StructureDescriptor;
}
