<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { authAPI } from "$lib/authAPI";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Select, SelectTrigger, SelectContent, SelectItem } from "$lib/components/ui/select";
  import { countryNames } from "$lib/utils/country-names";
  import { getIconComponent } from "$lib/iconUtils";
  import { connect, getConnectors } from "wagmi/actions";
  import { wagmiConfig } from "$lib/wallet/config";
  import {
    setupWalletWatcher,
    cleanupWalletWatcher,
    signAuthMessage,
    disconnectWallet,
  } from "$lib/wallet/store.svelte";
  import {
    LinkWalletStore,
    UnlinkWalletStore,
    LinkEmailStore,
    GetBillingDetailsStore,
    UpdateBillingDetailsStore,
  } from "$houdini";

  let firstName = $state("");

  // Link Email state (for wallet-only users)
  let linkEmailEmail = $state("");
  let linkEmailPassword = $state("");
  let linkEmailConfirm = $state("");
  let linkEmailLoading = $state(false);

  // Billing Details state
  interface BillingDetailsState {
    email: string;
    company: string;
    vatNumber: string;
    street: string;
    city: string;
    state: string;
    postalCode: string;
    country: string;
    isComplete: boolean;
  }
  let billingDetails = $state<BillingDetailsState>({
    email: "",
    company: "",
    vatNumber: "",
    street: "",
    city: "",
    state: "",
    postalCode: "",
    country: "",
    isComplete: false,
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

  // Wallet state
  interface LinkedWallet {
    id: string;
    address: string;
    createdAt: string;
    lastAuthAt?: string;
  }
  let linkedWallets = $state<LinkedWallet[]>([]);
  let walletLoading = $state(false);
  let walletConnectors = $state<ReturnType<typeof getConnectors>>([]);

  onMount(async () => {
    if ($auth.user) {
      const user = $auth.user as unknown as {
        first_name?: string;
        last_name?: string;
        wallets?: LinkedWallet[];
      };
      firstName = user.first_name || "";
      lastName = user.last_name || "";
      linkedWallets = user.wallets || [];

      // Load billing details for all users
      try {
        billingDetailsLoading = true;
        const detailsQuery = new GetBillingDetailsStore();
        const detailsResult = await detailsQuery.fetch();
        if (detailsResult.data?.billingDetails) {
          const d = detailsResult.data.billingDetails;
          billingDetails = {
            email: d.email || "",
            company: d.company || "",
            vatNumber: d.vatNumber || "",
            street: d.address?.street || "",
            city: d.address?.city || "",
            state: d.address?.state || "",
            postalCode: d.address?.postalCode || "",
            country: d.address?.country || "",
            isComplete: d.isComplete,
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
    setupWalletWatcher();
    walletConnectors = getConnectors(wagmiConfig);
  });

  onDestroy(() => {
    cleanupWalletWatcher();
  });

  async function handleLinkEmail() {
    if (!linkEmailEmail || !linkEmailPassword) {
      toast.error("Email and password are required");
      return;
    }
    if (linkEmailPassword !== linkEmailConfirm) {
      toast.error("Passwords do not match");
      return;
    }
    if (linkEmailPassword.length < 8) {
      toast.error("Password must be at least 8 characters");
      return;
    }

    linkEmailLoading = true;
    try {
      const linkEmailMutation = new LinkEmailStore();
      const result = await linkEmailMutation.mutate({
        input: { email: linkEmailEmail, password: linkEmailPassword },
      });

      const data = result.data?.linkEmail;
      if (data && "success" in data && data.success) {
        toast.success(data.message || "Email linked successfully");
        if (data.verificationSent) {
          toast.success("Verification email sent - please check your inbox");
        }
        // Clear form
        linkEmailEmail = "";
        linkEmailPassword = "";
        linkEmailConfirm = "";
      } else if (data && "message" in data) {
        throw new Error(data.message);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to link email");
    } finally {
      linkEmailLoading = false;
    }
  }

  async function handleSaveBillingDetails() {
    billingDetailsSaving = true;
    try {
      // Only include address if we have the required fields (street + country)
      const hasAddress = billingDetails.street && billingDetails.country;

      const mutation = new UpdateBillingDetailsStore();
      const result = await mutation.mutate({
        input: {
          email: billingDetails.email || null,
          company: billingDetails.company || null,
          vatNumber: billingDetails.vatNumber || null,
          address: hasAddress
            ? {
                street: billingDetails.street,
                city: billingDetails.city || null,
                state: billingDetails.state || null,
                postalCode: billingDetails.postalCode || null,
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
          email: d.email || "",
          company: d.company || "",
          vatNumber: d.vatNumber || "",
          street: d.address?.street || "",
          city: d.address?.city || "",
          state: d.address?.state || "",
          postalCode: d.address?.postalCode || "",
          country: d.address?.country || "",
          isComplete: d.isComplete,
        };
        toast.success("Billing details saved");
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save billing details");
    } finally {
      billingDetailsSaving = false;
    }
  }

  async function linkNewWallet(connector: (typeof walletConnectors)[0]) {
    walletLoading = true;
    try {
      const result = await connect(wagmiConfig, { connector });
      if (!result.accounts[0]) throw new Error("No account connected");

      const address = result.accounts[0];
      const signed = await signAuthMessage();
      if (!signed) throw new Error("Failed to sign message");

      const linkWallet = new LinkWalletStore();
      const response = await linkWallet.mutate({
        input: { address, message: signed.message, signature: signed.signature },
      });

      const data = response.data?.linkWallet;
      if (data && "id" in data) {
        linkedWallets = [
          ...linkedWallets,
          {
            id: data.id,
            address: data.address,
            createdAt: data.createdAt,
            lastAuthAt: data.lastAuthAt || undefined,
          },
        ];
        toast.success("Wallet linked successfully");
      } else if (data && "message" in data) {
        throw new Error(data.message);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to link wallet");
      await disconnectWallet();
    } finally {
      walletLoading = false;
    }
  }

  async function unlinkWallet(walletId: string) {
    walletLoading = true;
    try {
      const unlinkWalletStore = new UnlinkWalletStore();
      const response = await unlinkWalletStore.mutate({ walletId });

      const data = response.data?.unlinkWallet;
      if (data && "success" in data && data.success) {
        linkedWallets = linkedWallets.filter((w) => w.id !== walletId);
        toast.success("Wallet unlinked");
      } else if (data && "message" in data) {
        throw new Error(data.message);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to unlink wallet");
    } finally {
      walletLoading = false;
    }
  }

  function formatAddress(address: string): string {
    return `${address.slice(0, 6)}...${address.slice(-4)}`;
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
  const WalletIcon = getIconComponent("Wallet");
  const TrashIcon = getIconComponent("Trash2");
  const MailIcon = getIconComponent("Mail");
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

        <!-- Linked Wallets Section Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <WalletIcon class="w-4 h-4 text-primary" />
              <h3>Linked Wallets</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if linkedWallets.length === 0}
              <p class="text-sm text-muted-foreground mb-4">
                No wallets linked. Connect a wallet to enable wallet-based authentication.
              </p>
            {:else}
              <div class="space-y-3 mb-4">
                {#each linkedWallets as wallet (wallet.id)}
                  <div class="flex items-center justify-between p-3 bg-muted/30 rounded-md">
                    <div>
                      <p class="font-mono text-sm font-medium">{formatAddress(wallet.address)}</p>
                      <p class="text-xs text-muted-foreground">
                        Added {new Date(wallet.createdAt).toLocaleDateString()}
                      </p>
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onclick={() => unlinkWallet(wallet.id)}
                      disabled={walletLoading || (linkedWallets.length === 1 && !$auth.user?.email)}
                      title={linkedWallets.length === 1 && !$auth.user?.email
                        ? "Cannot unlink last wallet without email"
                        : "Unlink wallet"}
                    >
                      <TrashIcon class="w-4 h-4 text-destructive" />
                    </Button>
                  </div>
                {/each}
              </div>
            {/if}

            {#if walletConnectors.length > 0}
              <div class="space-y-2">
                <p class="text-sm text-muted-foreground">Add a wallet:</p>
                {#each walletConnectors as connector (connector.id)}
                  <Button
                    variant="outline"
                    class="w-full justify-start"
                    disabled={walletLoading}
                    onclick={() => linkNewWallet(connector)}
                  >
                    {walletLoading ? "Connecting..." : connector.name}
                  </Button>
                {/each}
              </div>
            {:else}
              <p class="text-sm text-muted-foreground">
                No wallet extensions detected. Install MetaMask or another wallet to link.
              </p>
            {/if}
          </div>
        </div>

        <!-- Link Email Section (for wallet-only users) -->
        {#if !$auth.user?.email}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <MailIcon class="w-4 h-4 text-primary" />
                <h3>Link Email</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <p class="text-sm text-muted-foreground mb-4">
                Add an email address to enable email-based login and unlock postpaid billing.
              </p>
              <div class="space-y-3">
                <div>
                  <label for="linkEmail" class="text-sm font-medium text-muted-foreground"
                    >Email</label
                  >
                  <Input
                    id="linkEmail"
                    type="email"
                    bind:value={linkEmailEmail}
                    placeholder="you@example.com"
                  />
                </div>
                <div>
                  <label for="linkPassword" class="text-sm font-medium text-muted-foreground"
                    >Password</label
                  >
                  <Input
                    id="linkPassword"
                    type="password"
                    bind:value={linkEmailPassword}
                    placeholder="At least 8 characters"
                  />
                </div>
                <div>
                  <label for="linkConfirm" class="text-sm font-medium text-muted-foreground"
                    >Confirm Password</label
                  >
                  <Input
                    id="linkConfirm"
                    type="password"
                    bind:value={linkEmailConfirm}
                    placeholder="Confirm password"
                  />
                </div>
              </div>
            </div>
            <div class="slab-actions">
              <Button onclick={handleLinkEmail} disabled={linkEmailLoading}>
                {linkEmailLoading ? "Linking..." : "Link Email"}
              </Button>
            </div>
          </div>
        {/if}

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
                Required for VAT invoicing on payments. Complete these details before making any
                top-ups.
              </p>
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
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
                    >VAT Number</label
                  >
                  <Input
                    id="billingVat"
                    type="text"
                    bind:value={billingDetails.vatNumber}
                    placeholder="DE123456789"
                  />
                </div>
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
