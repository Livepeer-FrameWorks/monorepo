import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildStructure } from "../src/vanilla/StructureBuilder";
import type { StructureDescriptor, BlueprintMap, BlueprintContext } from "../src/vanilla/Blueprint";

function mockElement(tag = "div") {
  const classSet = new Set<string>();
  const styles = new Map<string, string>();
  const children: any[] = [];

  return {
    tagName: tag.toUpperCase(),
    classList: {
      add: vi.fn((cls: string) => classSet.add(cls)),
      _set: classSet,
    },
    style: {
      setProperty: vi.fn((prop: string, val: string) => styles.set(prop, val)),
      _map: styles,
    },
    appendChild: vi.fn((child: any) => children.push(child)),
    children,
  };
}

function makeCtx(overrides: Partial<BlueprintContext> = {}): BlueprintContext {
  return {
    video: null,
    subscribe: { on: vi.fn(() => () => {}), get: vi.fn(), off: vi.fn() },
    api: {} as any,
    fullscreen: {
      supported: false,
      active: false,
      toggle: vi.fn(),
      request: vi.fn(),
      exit: vi.fn(),
    },
    pip: { supported: false, active: false, toggle: vi.fn() },
    info: null,
    options: {} as any,
    container: {} as any,
    translate: vi.fn((key: string, fallback?: string) => fallback ?? key),
    buildIcon: vi.fn(() => null),
    log: vi.fn(),
    timers: {
      setTimeout: vi.fn(),
      clearTimeout: vi.fn(),
      setInterval: vi.fn(),
      clearInterval: vi.fn(),
    },
    ...overrides,
  };
}

describe("buildStructure", () => {
  let ctx: BlueprintContext;

  beforeEach(() => {
    ctx = makeCtx();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("looks up blueprint factory and returns element", () => {
    const el = mockElement();
    const blueprints: BlueprintMap = {
      myWidget: vi.fn(() => el as any),
    };

    const result = buildStructure({ type: "myWidget" }, blueprints, ctx);
    expect(blueprints.myWidget).toHaveBeenCalledWith(ctx);
    expect(result).toBe(el);
  });

  it("returns null when blueprint not found", () => {
    const logFn = vi.fn();
    ctx = makeCtx({ log: logFn });

    const result = buildStructure({ type: "unknown" }, {}, ctx);
    expect(result).toBeNull();
    expect(logFn).toHaveBeenCalledWith(expect.stringContaining("unknown"));
  });

  it("returns null when factory returns null", () => {
    const blueprints: BlueprintMap = {
      empty: vi.fn(() => null),
    };

    const result = buildStructure({ type: "empty" }, blueprints, ctx);
    expect(result).toBeNull();
  });

  it("applies extra classes", () => {
    const el = mockElement();
    const blueprints: BlueprintMap = {
      widget: () => el as any,
    };

    const descriptor: StructureDescriptor = {
      type: "widget",
      classes: ["class-a", "class-b"],
    };

    buildStructure(descriptor, blueprints, ctx);
    expect(el.classList.add).toHaveBeenCalledWith("class-a");
    expect(el.classList.add).toHaveBeenCalledWith("class-b");
  });

  it("applies inline styles", () => {
    const el = mockElement();
    const blueprints: BlueprintMap = {
      widget: () => el as any,
    };

    const descriptor: StructureDescriptor = {
      type: "widget",
      style: { color: "red", "font-size": "14px" },
    };

    buildStructure(descriptor, blueprints, ctx);
    expect(el.style.setProperty).toHaveBeenCalledWith("color", "red");
    expect(el.style.setProperty).toHaveBeenCalledWith("font-size", "14px");
  });

  it("recursively builds and appends children", () => {
    const parent = mockElement();
    const child1 = mockElement();
    const child2 = mockElement();

    const blueprints: BlueprintMap = {
      parent: () => parent as any,
      child1: () => child1 as any,
      child2: () => child2 as any,
    };

    const descriptor: StructureDescriptor = {
      type: "parent",
      children: [{ type: "child1" }, { type: "child2" }],
    };

    buildStructure(descriptor, blueprints, ctx);
    expect(parent.appendChild).toHaveBeenCalledTimes(2);
    expect(parent.appendChild).toHaveBeenCalledWith(child1);
    expect(parent.appendChild).toHaveBeenCalledWith(child2);
  });

  it("skips null children", () => {
    const parent = mockElement();
    const blueprints: BlueprintMap = {
      parent: () => parent as any,
      missing: () => null,
    };

    const descriptor: StructureDescriptor = {
      type: "parent",
      children: [{ type: "missing" }],
    };

    buildStructure(descriptor, blueprints, ctx);
    expect(parent.appendChild).not.toHaveBeenCalled();
  });

  describe("conditional rendering", () => {
    it("renders then descriptor when if returns true", () => {
      const thenEl = mockElement();
      const blueprints: BlueprintMap = {
        then: () => thenEl as any,
      };

      const descriptor: StructureDescriptor = {
        type: "unused",
        if: () => true,
        then: { type: "then" },
      };

      const result = buildStructure(descriptor, blueprints, ctx);
      expect(result).toBe(thenEl);
    });

    it("renders else descriptor when if returns false", () => {
      const elseEl = mockElement();
      const blueprints: BlueprintMap = {
        fallback: () => elseEl as any,
      };

      const descriptor: StructureDescriptor = {
        type: "unused",
        if: () => false,
        else: { type: "fallback" },
      };

      const result = buildStructure(descriptor, blueprints, ctx);
      expect(result).toBe(elseEl);
    });

    it("returns null when if is false and no else", () => {
      const descriptor: StructureDescriptor = {
        type: "unused",
        if: () => false,
      };

      const result = buildStructure(descriptor, {}, ctx);
      expect(result).toBeNull();
    });

    it("proceeds normally when if is true but no then descriptor", () => {
      const el = mockElement();
      const blueprints: BlueprintMap = {
        widget: () => el as any,
      };

      const descriptor: StructureDescriptor = {
        type: "widget",
        if: () => true,
      };

      const result = buildStructure(descriptor, blueprints, ctx);
      expect(result).toBe(el);
    });
  });
});
