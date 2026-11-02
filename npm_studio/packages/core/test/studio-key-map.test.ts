import { describe, expect, it, vi } from "vitest";
import { adjacentSceneId, handleStudioSceneHotkey } from "../src/StudioKeyMap";

const scenes = [{ id: "a" }, { id: "b" }, { id: "c" }];

function sceneManager(activeId: string) {
  return {
    getAllScenes: () => scenes,
    getActiveScene: () => scenes.find((scene) => scene.id === activeId) ?? null,
    setActiveScene: vi.fn(),
  };
}

function key(k: string, target: Partial<HTMLElement> = { tagName: "DIV" }) {
  return {
    key: k,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    metaKey: false,
    target,
    composedPath: () => [target],
    preventDefault: vi.fn(),
  } as unknown as KeyboardEvent;
}

describe("adjacentSceneId", () => {
  it("steps within the scene list and stops at either end", () => {
    expect(adjacentSceneId(scenes, "b", 1)).toBe("c");
    expect(adjacentSceneId(scenes, "b", -1)).toBe("a");
    expect(adjacentSceneId(scenes, "c", 1)).toBeNull();
    expect(adjacentSceneId(scenes, "a", -1)).toBeNull();
    expect(adjacentSceneId(scenes, "missing", 1)).toBeNull();
  });
});

describe("handleStudioSceneHotkey", () => {
  it("switches to the next and the previous scene", () => {
    const sm = sceneManager("b");
    expect(handleStudioSceneHotkey(key("]"), sm)).toBe(true);
    expect(sm.setActiveScene).toHaveBeenLastCalledWith("c");
    expect(handleStudioSceneHotkey(key("["), sm)).toBe(true);
    expect(sm.setActiveScene).toHaveBeenLastCalledWith("a");
  });

  it("leaves a key typed into a field alone", () => {
    const sm = sceneManager("b");
    expect(handleStudioSceneHotkey(key("]", { tagName: "TEXTAREA" }), sm)).toBe(false);
    expect(sm.setActiveScene).not.toHaveBeenCalled();
  });

  it("does nothing without a compositor or for other keys", () => {
    expect(handleStudioSceneHotkey(key("]"), null)).toBe(false);
    const sm = sceneManager("b");
    expect(handleStudioSceneHotkey(key("x"), sm)).toBe(false);
    expect(sm.setActiveScene).not.toHaveBeenCalled();
  });
});
