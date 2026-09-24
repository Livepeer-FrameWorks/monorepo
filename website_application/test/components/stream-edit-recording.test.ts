import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import StreamEditModal from "$lib/components/stream-details/StreamEditModal.svelte";

// Unmounting the dialog schedules bits-ui's body scroll-lock restore 24ms
// later; it must run before the file's jsdom environment is torn down.
afterEach(async () => {
  cleanup();
  await new Promise((resolve) => setTimeout(resolve, 50));
});

function streamWith(mode: "WINDOW_SIZED" | "FIXED_INTERVAL" | "NONE" | null, interval?: number) {
  return {
    id: "stream-1",
    name: "Studio",
    description: "",
    record: true,
    ingestMode: "PUSH",
    dvrChapterMode: mode,
    dvrChapterIntervalSeconds: interval ?? null,
  };
}

async function submit() {
  const form = document.getElementById("edit-stream-form");
  if (!form) throw new Error("edit form not rendered");
  await fireEvent.submit(form);
}

describe("stream edit recording settings", () => {
  it("keeps an unsaved choice when the page refreshes the stream", async () => {
    const onSave = vi.fn();
    const view = render(StreamEditModal, {
      open: true,
      stream: streamWith("WINDOW_SIZED"),
      onSave,
    });

    const select = (await screen.findByLabelText(
      "Split saved recordings into"
    )) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: "FIXED_INTERVAL" } });
    const hours = (await screen.findByLabelText("Hours per part")) as HTMLInputElement;
    await fireEvent.input(hours, { target: { value: "2" } });

    // The page re-emits the same stream on its poll and subscriptions.
    await view.rerender({ open: true, stream: streamWith("WINDOW_SIZED"), onSave });

    expect((screen.getByLabelText("Split saved recordings into") as HTMLSelectElement).value).toBe(
      "FIXED_INTERVAL"
    );
    await submit();
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onSave.mock.calls[0][0]).toMatchObject({
      dvrChapterMode: "FIXED_INTERVAL",
      dvrChapterIntervalSeconds: 7200,
    });
  });

  it("sends NONE explicitly and clears the interval", async () => {
    const onSave = vi.fn();
    render(StreamEditModal, { open: true, stream: streamWith("NONE"), onSave });

    expect(
      (screen.getByLabelText(/Don't save — live rewind only/) as HTMLButtonElement).getAttribute(
        "data-state"
      )
    ).toBe("checked");
    await submit();
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onSave.mock.calls[0][0]).toMatchObject({
      dvrChapterMode: "NONE",
      dvrChapterIntervalSeconds: 0,
    });
  });

  it("sends interval 0 for window-sized parts even when a fixed interval was stored", async () => {
    const onSave = vi.fn();
    render(StreamEditModal, { open: true, stream: streamWith("WINDOW_SIZED", 7200), onSave });

    await submit();
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onSave.mock.calls[0][0]).toMatchObject({
      dvrChapterMode: "WINDOW_SIZED",
      dvrChapterIntervalSeconds: 0,
    });
  });
});
