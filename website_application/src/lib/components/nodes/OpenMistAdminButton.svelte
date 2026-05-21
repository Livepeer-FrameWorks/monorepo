<script lang="ts">
  import { OpenMistAdminSessionStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { toast } from "$lib/stores/toast";
  import { Terminal } from "lucide-svelte";

  // Opens the MistServer admin UI on this edge node in a new tab. The
  // mutation returns a short-TTL session token plus the per-edge POST URL
  // the browser must submit to. We send the token via a hidden POST form
  // (target=_blank) so it never lands in URLs, referrers, browser
  // history, or access logs. Helmsman validates the token, sets an
  // HttpOnly cookie scoped to /_mist, and redirects to /_mist/.
  //
  // Authority is node ownership, not just role — the resolver + Commodore
  // both deny when the caller doesn't own the node, so the button just
  // surfaces errors and never gates visibility itself.

  interface Props {
    nodeId: string;
    nodeName: string;
  }

  let { nodeId, nodeName }: Props = $props();

  const openSession = new OpenMistAdminSessionStore();

  let pending = $state(false);

  async function open() {
    pending = true;
    try {
      const result = await openSession.mutate({ input: { nodeId } });
      const data = result.data?.openMistAdminSession;
      if (!data) {
        toast.error("No response from server");
        return;
      }
      switch (data.__typename) {
        case "MistAdminSession":
          submitTokenForm(data.postUrl, data.sessionToken);
          toast.success(`Opening Mist admin on ${nodeName}…`);
          break;
        case "ValidationError":
          toast.error(`${data.message}${data.field ? ` (${data.field})` : ""}`);
          break;
        case "NotFoundError":
          toast.error(data.message || "Node not found");
          break;
        case "AuthError":
          toast.error(data.message || "Not authorised to admin this node");
          break;
      }
    } catch (err) {
      toast.error(`Failed to open Mist admin: ${(err as Error).message}`);
    } finally {
      pending = false;
    }
  }

  // Build a hidden form and submit it. POST-via-form is the only way to
  // get the browser to navigate a new tab with a request body — fetch()
  // can't open a window, and a GET would leak the token to the URL.
  function submitTokenForm(postUrl: string, sessionToken: string) {
    const form = document.createElement("form");
    form.method = "POST";
    form.action = postUrl;
    form.target = "_blank";
    form.rel = "noopener";
    form.style.display = "none";

    const input = document.createElement("input");
    input.type = "hidden";
    input.name = "session_token";
    input.value = sessionToken;
    form.appendChild(input);

    document.body.appendChild(form);
    form.submit();
    document.body.removeChild(form);
  }
</script>

<section class="border border-border rounded-md p-4">
  <div class="flex items-center justify-between mb-3">
    <div>
      <h3 class="text-base font-semibold">MistServer admin</h3>
      <p class="text-xs text-muted-foreground mt-0.5">
        Direct access to the LSP control panel on this edge node. Session expires in ~5 minutes, and
        is bound to this node only.
      </p>
    </div>
  </div>

  <div
    class="border border-warning/30 bg-warning/5 rounded p-2 mb-3 text-xs flex items-start gap-2"
  >
    <Terminal class="w-3.5 h-3.5 text-warning shrink-0 mt-0.5" />
    <div>
      MistServer admin lets you reconfigure protocols, triggers, and processes on this machine.
      Treat this like SSH access.
    </div>
  </div>

  <Button onclick={open} disabled={pending} class="w-full justify-center">
    <Terminal class="w-4 h-4 mr-2" />
    {pending ? "Opening…" : "Open Mist admin"}
  </Button>
</section>
