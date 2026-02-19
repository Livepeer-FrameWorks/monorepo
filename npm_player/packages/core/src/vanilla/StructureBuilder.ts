/**
 * StructureBuilder — Recursive DOM builder from StructureDescriptors.
 *
 * Walks the descriptor tree, looks up each `type` in the blueprint map,
 * calls the factory, and nests the results.
 */

import type { StructureDescriptor, BlueprintMap, BlueprintContext } from "./Blueprint";

/**
 * Build a DOM tree from a structure descriptor using the provided blueprints.
 *
 * @param descriptor - The layout descriptor (JSON tree)
 * @param blueprints - Named map of blueprint factories
 * @param ctx - The blueprint context (player API, state, etc.)
 * @returns The root HTMLElement, or null if the descriptor produced nothing
 */
export function buildStructure(
  descriptor: StructureDescriptor,
  blueprints: BlueprintMap,
  ctx: BlueprintContext
): HTMLElement | null {
  // Handle conditional descriptors
  if (descriptor.if) {
    const condition = descriptor.if(ctx);
    if (condition && descriptor.then) {
      return buildStructure(descriptor.then, blueprints, ctx);
    }
    if (!condition && descriptor.else) {
      return buildStructure(descriptor.else, blueprints, ctx);
    }
    if (!condition) return null;
  }

  // Look up the blueprint factory
  const factory = blueprints[descriptor.type];
  if (!factory) {
    ctx.log(`Blueprint not found: "${descriptor.type}"`);
    return null;
  }

  // Call the factory to create the element
  const el = factory(ctx);
  if (!el) return null;

  // Apply extra classes
  if (descriptor.classes) {
    for (const cls of descriptor.classes) {
      el.classList.add(cls);
    }
  }

  // Apply inline styles
  if (descriptor.style) {
    for (const [prop, value] of Object.entries(descriptor.style)) {
      el.style.setProperty(prop, value);
    }
  }

  // Recursively build and append children
  if (descriptor.children) {
    for (const child of descriptor.children) {
      const childEl = buildStructure(child, blueprints, ctx);
      if (childEl) {
        el.appendChild(childEl);
      }
    }
  }

  return el;
}
