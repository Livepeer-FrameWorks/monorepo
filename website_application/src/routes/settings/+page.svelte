<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { authAPI } from "$lib/authAPI";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";

  let firstName = $state("");
  let lastName = $state("");
  let newsletter = $state(true);
  let loading = $state(false);
  let pageLoading = $state(true);

  onMount(() => {
    if ($auth.user) {
      const user = $auth.user as unknown as { first_name?: string; last_name?: string };
      firstName = user.first_name || "";
      lastName = user.last_name || "";
    }
    pageLoading = false;
  });

  async function saveProfile() {
    loading = true;
    try {
      await authAPI.patch("/me", { first_name: firstName, last_name: lastName });
      // Update local storage cache to reflect change
      const storedUserData = localStorage.getItem("user");
      if (storedUserData) {
        const user = JSON.parse(storedUserData);
        user.first_name = firstName;
        user.last_name = lastName;
        localStorage.setItem("user", JSON.stringify(user));
      }
      toast.success("Profile updated successfully");
    } catch (e) {
      toast.error("Failed to update profile");
    } finally {
      loading = false;
    }
  }

  async function toggleNewsletter() {
    try {
      await authAPI.post("/me/newsletter", { subscribe: newsletter });
      toast.success("Notification preference updated");
    } catch (e) {
      newsletter = !newsletter;
      toast.error("Failed to update preference");
    }
  }

  // Icons
  const SettingsIcon = getIconComponent("Settings");
  const UserIcon = getIconComponent("User");
  const BellIcon = getIconComponent("Bell");
  const CreditCardIcon = getIconComponent("CreditCard");
</script>

<svelte:head>
  <title>Settings - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <SettingsIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Account Settings</h1>
          <p class="text-sm text-muted-foreground">
            Manage your profile and preferences
          </p>
        </div>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
  {#if pageLoading}
    <div class="px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    </div>
  {:else}
    <div class="dashboard-grid">
        <!-- Profile Section Slab -->
        <div class="slab xl:col-span-2">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <UserIcon class="w-4 h-4 text-primary" />
              <h3>Profile Information</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
              <div class="space-y-2">
                <label for="firstName" class="text-sm font-medium text-muted-foreground">
                  First Name
                </label>
                <Input
                  id="firstName"
                  type="text"
                  bind:value={firstName}
                  placeholder="Jane"
                />
              </div>
              <div class="space-y-2">
                <label for="lastName" class="text-sm font-medium text-muted-foreground">
                  Last Name
                </label>
                <Input
                  id="lastName"
                  type="text"
                  bind:value={lastName}
                  placeholder="Doe"
                />
              </div>
            </div>

            <div class="flex justify-end">
              <Button
                onclick={saveProfile}
                disabled={loading}
              >
                {loading ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          </div>
        </div>

        <!-- Notifications Section Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <BellIcon class="w-4 h-4 text-warning" />
              <h3>Notifications</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="flex items-start space-x-4">
              <div class="flex items-center h-5 mt-1">
                <input
                  id="newsletter"
                  type="checkbox"
                  bind:checked={newsletter}
                  onchange={toggleNewsletter}
                  class="h-4 w-4 text-primary border-input rounded focus:ring-ring bg-background"
                />
              </div>
              <div class="flex-1">
                <label for="newsletter" class="font-medium text-foreground">
                  Product Updates & Newsletter
                </label>
                <p class="text-sm text-muted-foreground mt-1">
                  Receive updates about new features, improvements, and community news.
                  We respect your inbox and never share your email.
                </p>
              </div>
            </div>
          </div>
          <div class="slab-actions">
            <Button href={resolve("/account/billing")} variant="ghost" class="gap-2">
              <CreditCardIcon class="w-4 h-4" />
              Billing & Plans
            </Button>
          </div>
        </div>

        <!-- Account Info Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <SettingsIcon class="w-4 h-4 text-muted-foreground" />
              <h3>Account Information</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
              <div>
                <p class="text-sm text-muted-foreground mb-1">Email</p>
                <p class="text-foreground font-medium">
                  {$auth.user?.email || "Not available"}
                </p>
              </div>
              <div>
                <p class="text-sm text-muted-foreground mb-1">Account ID</p>
                <p class="text-foreground font-mono text-sm">
                  {$auth.user?.id ? $auth.user.id.slice(0, 8) + "..." : "N/A"}
                </p>
              </div>
              <div>
                <p class="text-sm text-muted-foreground mb-1">Member Since</p>
                <p class="text-foreground">
                  {$auth.user?.created_at
                    ? new Date($auth.user.created_at).toLocaleDateString()
                    : "N/A"}
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>
