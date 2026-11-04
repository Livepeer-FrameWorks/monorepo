import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { tick } from "svelte";
import GraphQLExplorer from "$lib/components/GraphQLExplorer.svelte";
import { explorerService } from "$lib/graphql/services/explorer";
import { toast } from "$lib/stores/toast";
import { loadSdkCatalog } from "$lib/graphql/services/sdkSnippets";

vi.mock("$lib/components/explorer/QueryEditor.svelte", async () => ({
  default: (await import("./fixtures/ExplorerEditorStub.svelte")).default,
}));

beforeEach(() => {
  vi.stubGlobal("localStorage", {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
  });
  vi.spyOn(explorerService, "getTemplates").mockResolvedValue({
    queries: [],
    mutations: [],
    subscriptions: [],
    fragments: [],
  });
  vi.spyOn(explorerService, "getCatalog").mockResolvedValue([]);
  vi.spyOn(explorerService, "getCachedSchema").mockResolvedValue({
    queryType: { name: "Query", fields: [] },
    types: [],
  });
  vi.spyOn(toast, "warning");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("waits for operation selection without raising a toast while editing", async () => {
  const query = "query First { tenant { id } } query Second { tenant { name } }";
  const execute = vi.spyOn(explorerService, "executeQuery").mockResolvedValue({ data: {} });
  render(GraphQLExplorer, { initialQuery: query });
  await waitFor(() => expect(explorerService.getCachedSchema).toHaveBeenCalled());
  await tick();
  expect(toast.warning).not.toHaveBeenCalled();
  await fireEvent.change(screen.getByRole("combobox"), { target: { value: "Second" } });
  await fireEvent.click(screen.getByRole("button", { name: "Execute" }));
  expect(execute).toHaveBeenCalledWith(query, {}, "query", false, "Second");
});

it("keeps the editor usable while the query has incomplete syntax", async () => {
  const view = render(GraphQLExplorer, { initialQuery: "query Q { tenant { id } }" });
  await waitFor(() => expect(explorerService.getCachedSchema).toHaveBeenCalled());
  await tick();
  await expect(view.rerender({ initialQuery: "query {" })).resolves.toBeUndefined();
  expect((screen.getByRole("textbox", { name: "Query" }) as HTMLTextAreaElement).value).toBe(
    "query {"
  );
  expect(toast.warning).not.toHaveBeenCalled();
});

it("keeps the operation selector usable while definition names are duplicated", async () => {
  const view = render(GraphQLExplorer, { initialQuery: "query Q { tenant { id } }" });
  await tick();
  await expect(
    view.rerender({ initialQuery: "query Q { tenant { id } } query Q { tenant { name } }" })
  ).resolves.toBeUndefined();
  expect(screen.getByRole("combobox")).toBeTruthy();
});

it("shows raw exports after the SDK reports missing variables", async () => {
  const catalog = await loadSdkCatalog();
  vi.spyOn(explorerService, "loadSdkCatalog").mockResolvedValue(catalog);
  render(GraphQLExplorer, { initialQuery: catalog.operations.get("GetStream")!.document });
  await fireEvent.click(screen.getByRole("button", { name: "Code" }));
  await waitFor(() => expect(screen.getByText(/Fill in required variables/)).toBeTruthy());
  await fireEvent.change(screen.getByRole("combobox"), { target: { value: "fetch" } });
  expect(screen.getByText(/await fetch\(/)).toBeTruthy();
  expect(screen.queryByText("Loading SDK operations…")).toBeNull();
});

it.each(["null", "[1,2]", '"abc"', "{"])(
  "shows variable errors in raw tabs: %s",
  async (variables) => {
    render(GraphQLExplorer, { initialQuery: "query Q { tenant { id } }" });
    await fireEvent.input(screen.getByRole("textbox", { name: "Variables" }), {
      target: { value: variables },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Code" }));
    await fireEvent.change(screen.getByRole("combobox"), { target: { value: "fetch" } });
    expect(document.querySelector("pre code")?.textContent).toMatch(
      /JSON object|JSON|property name/
    );
    expect(screen.queryByText("Loading SDK operations…")).toBeNull();
  }
);
