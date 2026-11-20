import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import IngestStopDialog from "$lib/components/stream-details/IngestStopDialog.svelte";

afterEach(async () => {
  cleanup();
  await new Promise((resolve) => setTimeout(resolve, 50));
});

it("key rotation keeps the publisher unless ending it is explicitly selected", async () => {
  const onConfirm = vi.fn();
  render(IngestStopDialog, { open: true, action: "rotate", onConfirm });
  const checkbox = await screen.findByRole("checkbox");
  expect((checkbox as HTMLInputElement).checked).toBe(false);
  await fireEvent.click(screen.getByRole("button", { name: "Rotate key" }));
  expect(onConfirm).toHaveBeenLastCalledWith(false);
  await fireEvent.click(checkbox);
  await fireEvent.click(screen.getByRole("button", { name: "Rotate key" }));
  expect(onConfirm).toHaveBeenLastCalledWith(true);
});

it("disconnect confirms ending the current publisher", async () => {
  const onConfirm = vi.fn();
  render(IngestStopDialog, { open: true, action: "disconnect", onConfirm });
  await fireEvent.click(await screen.findByRole("button", { name: "Disconnect encoder" }));
  expect(onConfirm).toHaveBeenCalledExactlyOnceWith(true);
});

it("reopening rotation clears an earlier disconnect selection", async () => {
  const onConfirm = vi.fn();
  const view = render(IngestStopDialog, { open: true, action: "rotate", onConfirm });
  await fireEvent.click(await screen.findByRole("checkbox"));
  await view.rerender({ open: false, action: "rotate", onConfirm });
  await view.rerender({ open: true, action: "rotate", onConfirm });
  expect(((await screen.findByRole("checkbox")) as HTMLInputElement).checked).toBe(false);
});
