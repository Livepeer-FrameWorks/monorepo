import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import type { CameraChoice, MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import CameraPicker from "../src/components/CameraPicker.svelte";

const front: CameraChoice = { deviceId: "front", label: "Front camera", inUse: true };
const rear: CameraChoice = { deviceId: "rear", label: "Rear camera", inUse: false };
const camera = { id: "camera-1", type: "camera" } as MediaSource;

function renderPicker(sources: MediaSource[], choices: CameraChoice[]) {
  const onPick = vi.fn();
  render(CameraPicker, { sources, onPick, getChoices: async () => choices });
  return onPick;
}

describe("CameraPicker", () => {
  it("stays available once a camera is live", () => {
    renderPicker([camera], [front, rear]);

    expect((screen.getByTitle("Add Camera") as HTMLButtonElement).disabled).toBe(false);
  });

  it("starts the only camera straight away", async () => {
    const onPick = renderPicker([], [{ ...rear, inUse: false }]);

    await fireEvent.click(screen.getByTitle("Add Camera"));

    await waitFor(() => expect(onPick).toHaveBeenCalledWith("rear"));
  });

  it("asks the browser for a camera before it has shown any", async () => {
    const onPick = renderPicker([], []);

    await fireEvent.click(screen.getByTitle("Add Camera"));

    await waitFor(() => expect(onPick).toHaveBeenCalledWith(undefined));
  });

  it("lets the broadcaster pick a second camera", async () => {
    const onPick = renderPicker([camera], [front, rear]);

    await fireEvent.click(screen.getByTitle("Add Camera"));
    await fireEvent.click(await screen.findByRole("menuitem", { name: /Rear camera/ }));

    expect(onPick).toHaveBeenCalledWith("rear");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  });

  it("says so when the cameras cannot be listed", async () => {
    const onPick = vi.fn();
    render(CameraPicker, {
      sources: [],
      onPick,
      getChoices: async () => {
        throw new Error("enumerateDevices not supported");
      },
    });

    await fireEvent.click(screen.getByTitle("Add Camera"));

    expect((await screen.findByRole("alert")).textContent?.trim()).toBe(
      "Could not list the cameras"
    );
    expect(onPick).not.toHaveBeenCalled();
  });

  it("clears that message on the next try", async () => {
    const getChoices = vi
      .fn()
      .mockRejectedValueOnce(new Error("enumerateDevices not supported"))
      .mockResolvedValueOnce([rear]);
    render(CameraPicker, { sources: [], onPick: vi.fn(), getChoices });

    await fireEvent.click(screen.getByTitle("Add Camera"));
    await screen.findByRole("alert");
    await fireEvent.click(screen.getByTitle("Add Camera"));

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });

  it("shows a camera already in use without offering it again", async () => {
    renderPicker([camera], [front, rear]);

    await fireEvent.click(screen.getByTitle("Add Camera"));

    const item = (await screen.findByRole("menuitem", {
      name: /Front camera/,
    })) as HTMLButtonElement;
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain("Camera active");
  });
});
