<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { authAPI } from "$lib/authAPI";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Select, SelectTrigger, SelectContent, SelectItem } from "$lib/components/ui/select";
  import { countryNames } from "$lib/utils/country-names";
  import { getIconComponent } from "$lib/iconUtils";
  import ThemePicker from "$lib/components/ThemePicker.svelte";
  import { themeStore } from "$lib/stores/theme.svelte";
  import { GetBillingDetailsStore, UpdateBillingDetailsStore } from "$houdini";

  let firstName = $state("");

  // Billing Details state
  interface BillingDetailsState {
    customerType: string;
    email: string;
    name: string;
    company: string;
    vatNumber: string;
    street: string;
    city: string;
    state: string;
    postalCode: string;
    country: string;
    isComplete: boolean;
    // Derived by Purser from the billing country; not editable here.
    presentmentCurrency: string;
  }
  let billingDetails = $state<BillingDetailsState>({
    customerType: "unspecified",
    email: "",
    name: "",
    company: "",
    vatNumber: "",
    street: "",
    city: "",
    state: "",
    postalCode: "",
    country: "",
    isComplete: false,
    presentmentCurrency: "",
  });
  let billingDetailsLoading = $state(false);
  let billingDetailsSaving = $state(false);

  // Country options sorted by name for billing address dropdown
  const countryOptions = Object.entries(countryNames)
    .map(([code, name]) => ({ code, name }))
    .sort((a, b) => a.name.localeCompare(b.name));

  let lastName = $state("");
  let newsletter = $state(false);
  let newsletterLoading = $state(true);
  let loading = $state(false);
  let pageLoading = $state(true);

  onMount(async () => {
    if ($auth.user) {
      const user = $auth.user as unknown as {
        first_name?: string;
        last_name?: string;
      };
      firstName = user.first_name || "";
      lastName = user.last_name || "";

      // Load billing details for all users
      try {
        billingDetailsLoading = true;
        const detailsQuery = new GetBillingDetailsStore();
        const detailsResult = await detailsQuery.fetch();
        if (detailsResult.data?.billingDetails) {
          const d = detailsResult.data.billingDetails;
          billingDetails = {
            customerType: d.customerType || "unspecified",
            email: d.email || "",
            name: d.name || "",
            company: d.company || "",
            vatNumber: d.vatNumber || "",
            street: d.address?.street || "",
            city: d.address?.city || "",
            state: d.address?.state || "",
            postalCode: d.address?.postalCode || "",
            country: d.address?.country || "",
            isComplete: d.isComplete,
            presentmentCurrency: d.presentmentCurrency || "",
          };
        }
      } catch {
        // Billing details optional
      } finally {
        billingDetailsLoading = false;
      }

      // Load newsletter status from Listmonk (source of truth)
      try {
        const newsletterResp = await authAPI.get("/me/newsletter");
        newsletter = newsletterResp.data?.subscribed ?? false;
      } catch {
        // Default to false if fetch fails
        newsletter = false;
      } finally {
        newsletterLoading = false;
      }
    }
    pageLoading = false;
  });

  async function handleSaveBillingDetails() {
    billingDetailsSaving = true;
    try {
      if (!["consumer", "business"].includes(billingDetails.customerType)) {
        throw new Error("Choose personal or business billing");
      }
      // Only include address if we have the required fields (street + country)
      const hasAddress = billingDetails.street && billingDetails.country;

      const mutation = new UpdateBillingDetailsStore();
      const result = await mutation.mutate({
        input: {
          customerType: billingDetails.customerType,
          email: billingDetails.email || null,
          name: billingDetails.name || null,
          company: billingDetails.customerType === "consumer" ? "" : billingDetails.company,
          vatNumber: billingDetails.customerType === "consumer" ? "" : billingDetails.vatNumber,
          address: hasAddress
            ? {
                street: billingDetails.street,
                city: billingDetails.city || "",
                state: billingDetails.state || null,
                postalCode: billingDetails.postalCode || "",
                country: billingDetails.country,
              }
            : null,
        },
      });

      // Check for GraphQL errors
      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }

      if (result.data?.updateBillingDetails) {
        const d = result.data.updateBillingDetails;
        billingDetails = {
          customerType: d.customerType || "unspecified",
          email: d.email || "",
          name: d.name || "",
          company: d.company || "",
          vatNumber: d.vatNumber || "",
          street: d.address?.street || "",
          city: d.address?.city || "",
          state: d.address?.state || "",
          postalCode: d.address?.postalCode || "",
          country: d.address?.country || "",
          isComplete: d.isComplete,
          presentmentCurrency: d.presentmentCurrency || "",
        };
        toast.success("Billing details saved");
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save billing details");
    } finally {
      billingDetailsSaving = false;
    }
  }

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
    } catch {
      toast.error("Failed to update profile");
    } finally {
      loading = false;
    }
  }

  async function toggleNewsletter() {
    try {
      await authAPI.post("/me/newsletter", { subscribed: newsletter });
      toast.success("Notification preference updated");
    } catch {
      newsletter = !newsletter;
      toast.error("Failed to update preference");
    }
  }

  // Icons
  const SettingsIcon = getIconComponent("Settings");
  const UserIcon = getIconComponent("User");
  const BellIcon = getIconComponent("Bell");
  const CreditCardIcon = getIconComponent("CreditCard");
  const SecurityIcon = getIconComponent("ShieldCheck");
  const PaletteIcon = getIconComponent("Palette");
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
          <p class="text-sm text-muted-foreground">Manage your profile and preferences</p>
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
                <Input id="firstName" type="text" bind:value={firstName} placeholder="Jane" />
              </div>
              <div class="space-y-2">
                <label for="lastName" class="text-sm font-medium text-muted-foreground">
                  Last Name
                </label>
                <Input id="lastName" type="text" bind:value={lastName} placeholder="Doe" />
              </div>
            </div>

            <div class="flex justify-end">
              <Button onclick={saveProfile} disabled={loading}>
                {loading ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          </div>
        </div>

        <!-- Appearance Section Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <PaletteIcon class="w-4 h-4 text-primary" />
              <h3>Appearance</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="space-y-6">
              <div>
                <h4 class="text-sm font-medium text-foreground mb-3">Theme</h4>
                <ThemePicker />
              </div>
              {#if themeStore.hasMultipleModes}
                <div>
                  <h4 class="text-sm font-medium text-foreground mb-3">Mode</h4>
                  <div class="flex gap-3">
                    <button
                      onclick={() => themeStore.setMode("dark")}
                      class="px-4 py-2 border transition-colors cursor-pointer text-sm
                        {themeStore.isDark
                        ? 'border-[hsl(var(--tn-blue))] bg-[hsl(var(--tn-blue)/0.08)] text-foreground'
                        : 'border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground'}"
                    >
                      Dark
                    </button>
                    <button
                      onclick={() => themeStore.setMode("light")}
                      class="px-4 py-2 border transition-colors cursor-pointer text-sm
                        {themeStore.isLight
                        ? 'border-[hsl(var(--tn-blue))] bg-[hsl(var(--tn-blue)/0.08)] text-foreground'
                        : 'border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground'}"
                    >
                      Light
                    </button>
                  </div>
                </div>
              {/if}
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
                  disabled={newsletterLoading}
                  class="h-4 w-4 text-primary border-input rounded focus:ring-ring bg-background disabled:opacity-50"
                />
              </div>
              <div class="flex-1">
                <label for="newsletter" class="font-medium text-foreground">
                  Product Updates & Newsletter
                </label>
                <p class="text-sm text-muted-foreground mt-1">
                  Receive updates about new features, improvements, and community news. We respect
                  your inbox and never share your email.
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

        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <SecurityIcon class="w-4 h-4 text-primary" />
              <h3>Account Security</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <p class="text-sm text-muted-foreground">
              Manage signed-in sessions, change your password, link an email and manage wallet
              sign-ins.
            </p>
          </div>
          <div class="slab-actions">
            <Button href={resolve("/account/security")} variant="ghost">Open Security</Button>
          </div>
        </div>

        <!-- Billing Details Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <CreditCardIcon class="w-4 h-4 text-muted-foreground" />
              <h3>Billing Details</h3>
              {#if billingDetails.isComplete}
                <span class="text-xs bg-success/10 text-success px-2 py-0.5 rounded">Complete</span>
              {:else}
                <span class="text-xs bg-warning/10 text-warning px-2 py-0.5 rounded"
                  >Incomplete</span
                >
              {/if}
            </div>
          </div>
          <div class="slab-body--padded">
            {#if billingDetailsLoading}
              <p class="text-sm text-muted-foreground">Loading billing details...</p>
            {:else}
              <p class="text-sm text-muted-foreground mb-4">
                Billing email and address are required before funding or enabling postpaid billing.
                Personal billing does not require a company or VAT number. Choose business billing
                to provide your company details and any registered VAT identifier.
              </p>
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div class="md:col-span-2">
                  <label for="billingCustomerType" class="text-sm font-medium text-muted-foreground"
                    >Billing as</label
                  >
                  <Select
                    type="single"
                    value={billingDetails.customerType === "unspecified"
                      ? ""
                      : billingDetails.customerType}
                    onValueChange={(v) => {
                      billingDetails.customerType = v || "unspecified";
                      if (v === "consumer") {
                        billingDetails.company = "";
                        billingDetails.vatNumber = "";
                      }
                    }}
                  >
                    <SelectTrigger id="billingCustomerType" class="w-full">
                      <span
                        >{billingDetails.customerType === "consumer"
                          ? "Personal"
                          : billingDetails.customerType === "business"
                            ? "Business"
                            : "Choose personal or business billing"}</span
                      >
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="consumer">Personal</SelectItem>
                      <SelectItem value="business">Business</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div>
                  <label for="billingName" class="text-sm font-medium text-muted-foreground"
                    >Legal / Customer Name</label
                  >
                  <Input
                    id="billingName"
                    type="text"
                    bind:value={billingDetails.name}
                    placeholder="Jane Doe or Acme B.V."
                  />
                </div>
                <div>
                  <label for="billingEmail" class="text-sm font-medium text-muted-foreground"
                    >Billing Email</label
                  >
                  <Input
                    id="billingEmail"
                    type="email"
                    bind:value={billingDetails.email}
                    placeholder="billing@company.com"
                  />
                </div>
                {#if billingDetails.customerType === "business"}
                  <div>
                    <label for="billingCompany" class="text-sm font-medium text-muted-foreground"
                      >Company Name</label
                    >
                    <Input
                      id="billingCompany"
                      type="text"
                      bind:value={billingDetails.company}
                      placeholder="Acme Inc."
                    />
                  </div>
                  <div>
                    <label for="billingVat" class="text-sm font-medium text-muted-foreground"
                      >VAT / Tax Identifier (optional)</label
                    >
                    <Input
                      id="billingVat"
                      type="text"
                      bind:value={billingDetails.vatNumber}
                      placeholder="DE123456789"
                    />
                    <p class="mt-1 text-xs text-muted-foreground">
                      Leave blank if you are not VAT registered.
                    </p>
                  </div>
                {/if}
                <div>
                  <label for="billingCountry" class="text-sm font-medium text-muted-foreground"
                    >Country</label
                  >
                  <Select
                    type="single"
                    value={billingDetails.country}
                    onValueChange={(v) => (billingDetails.country = v ?? "")}
                  >
                    <SelectTrigger id="billingCountry" class="w-full">
                      <span
                        class={!billingDetails.country ? "text-muted-foreground opacity-60" : ""}
                      >
                        {billingDetails.country
                          ? countryNames[billingDetails.country] || billingDetails.country
                          : "Select country..."}
                      </span>
                    </SelectTrigger>
                    <SelectContent class="max-h-[300px]">
                      {#each countryOptions as { code, name } (code)}
                        <SelectItem value={code}>{name}</SelectItem>
                      {/each}
                    </SelectContent>
                  </Select>
                </div>
                <div class="md:col-span-2">
                  <label for="billingStreet" class="text-sm font-medium text-muted-foreground"
                    >Street Address</label
                  >
                  <Input
                    id="billingStreet"
                    type="text"
                    bind:value={billingDetails.street}
                    placeholder="123 Main Street"
                  />
                </div>
                <div>
                  <label for="billingCity" class="text-sm font-medium text-muted-foreground"
                    >City</label
                  >
                  <Input
                    id="billingCity"
                    type="text"
                    bind:value={billingDetails.city}
                    placeholder="Berlin"
                  />
                </div>
                <div>
                  <label for="billingPostal" class="text-sm font-medium text-muted-foreground"
                    >Postal Code</label
                  >
                  <Input
                    id="billingPostal"
                    type="text"
                    bind:value={billingDetails.postalCode}
                    placeholder="10115"
                  />
                </div>
                <div>
                  <label for="billingCurrency" class="text-sm font-medium text-muted-foreground"
                    >Billing Currency</label
                  >
                  <Input
                    id="billingCurrency"
                    type="text"
                    value={billingDetails.presentmentCurrency || "Set by billing country"}
                    readonly
                    disabled
                  />
                  <p class="mt-1 text-xs text-muted-foreground">
                    Follows your billing country: EUR in the EEA, GBP in the United Kingdom, USD
                    elsewhere. It cannot change while a provider subscription, an open invoice, or a
                    card first payment started in the last day exists. Prices and your prepaid
                    balance are in EUR; charges convert at the ECB reference rate.
                  </p>
                </div>
              </div>
            {/if}
          </div>
          <div class="slab-actions">
            <Button
              onclick={handleSaveBillingDetails}
              disabled={billingDetailsSaving || billingDetailsLoading}
            >
              {billingDetailsSaving ? "Saving..." : "Save Billing Details"}
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
