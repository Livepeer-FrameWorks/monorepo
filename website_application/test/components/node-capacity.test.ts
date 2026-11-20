import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import NodeCapacityPanel from "$lib/components/nodes/NodeCapacityPanel.svelte";
import type { NodeCapacity } from "$lib/components/nodes/capacity";

const mocks = vi.hoisted(() => ({ mutate: vi.fn(), error: vi.fn(), success: vi.fn() }));
vi.mock("$houdini", () => ({
  SetNodeCapacityStore: class {
    mutate = mocks.mutate;
  },
}));
vi.mock("$lib/stores/toast", () => ({ toast: { error: mocks.error, success: mocks.success } }));

const capacity: NodeCapacity = {
  egressBudgetBytesPerSec: 125_000_000,
  ingressBudgetBytesPerSec: 125_000_000,
  egressSource: "DEFAULT",
  ingressSource: "DEFAULT",
  revision: 4,
  appliedRevision: 3,
};
beforeEach(() => {
  mocks.mutate.mockReset().mockResolvedValue({
    data: {
      setNodeCapacity: {
        __typename: "InfrastructureNode",
        capacity: { ...capacity, revision: 5 },
      },
    },
  });
  mocks.error.mockReset();
  mocks.success.mockReset();
});
afterEach(cleanup);

it("shows default and pending status and saves decimal Mbit/s as bytes/s with a revision", async () => {
  const refreshed = vi.fn();
  render(NodeCapacityPanel, { nodeId: "edge", capacity, onCapacityChanged: refreshed });
  expect(screen.getByText("Capacity not applied yet.")).toBeTruthy();
  expect(screen.getAllByText(/Mbit\/s · default/)).toHaveLength(2);
  await fireEvent.input(screen.getByLabelText("Egress budget (Mbit/s)"), {
    target: { value: "2000" },
  });
  await fireEvent.input(screen.getByLabelText("Ingress budget (Mbit/s)"), {
    target: { value: "750" },
  });
  await fireEvent.click(screen.getByRole("button", { name: "Save capacity" }));
  await waitFor(() =>
    expect(mocks.mutate).toHaveBeenCalledWith({
      input: {
        nodeId: "edge",
        egressBudgetBytesPerSec: 250_000_000,
        ingressBudgetBytesPerSec: 93_750_000,
        expectedRevision: 4,
      },
    })
  );
  await waitFor(() => expect(refreshed).toHaveBeenCalledOnce());
});

it("restores both defaults with null overrides", async () => {
  render(NodeCapacityPanel, {
    nodeId: "edge",
    capacity: { ...capacity, egressSource: "OPERATOR", ingressSource: "OPERATOR" },
  });
  await fireEvent.click(screen.getByRole("button", { name: "Use defaults" }));
  await fireEvent.click(screen.getByRole("button", { name: "Save capacity" }));
  await waitFor(() =>
    expect(mocks.mutate).toHaveBeenCalledWith({
      input: {
        nodeId: "edge",
        egressBudgetBytesPerSec: null,
        ingressBudgetBytesPerSec: null,
        expectedRevision: 4,
      },
    })
  );
});

it("preserves an edited draft when another operator changes capacity and requires reload", async () => {
  const { rerender } = render(NodeCapacityPanel, { nodeId: "edge", capacity });
  await fireEvent.input(screen.getByLabelText("Egress budget (Mbit/s)"), {
    target: { value: "3000" },
  });
  await rerender({
    nodeId: "edge",
    capacity: {
      ...capacity,
      revision: 5,
      egressSource: "OPERATOR",
      egressBudgetBytesPerSec: 500_000_000,
    },
  });
  expect((screen.getByLabelText("Egress budget (Mbit/s)") as HTMLInputElement).value).toBe("3000");
  expect(
    (screen.getByRole("button", { name: "Save capacity" }) as HTMLButtonElement).disabled
  ).toBe(true);
  expect(mocks.mutate).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole("button", { name: "Reload capacity" }));
  expect((screen.getByLabelText("Egress budget (Mbit/s)") as HTMLInputElement).value).toBe("4000");
});
