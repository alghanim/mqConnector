<!--
  /account — the current user's profile + per-tenant role summary.

  The page is a read-mostly surface: the identity claims come from
  SimpleAuth (we can't edit them here), the tenant memberships come
  from /api/v1/tenants. The only actionable item is "Sign out", which
  goes through the auth store so the silent-refresh timer stops
  alongside the cookie clear.

  This page used to be a 404 because the ProfileMenu linked at
  /account without a backing route. Adding it here fixes the link
  the user clicks from the avatar dropdown.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth';
  import { tenants } from '$lib/stores/tenants';
  import { locale, t } from '$lib/stores/locale';
  import type { Role } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Avatar from '$lib/components/Avatar.svelte';
  import Alert from '$lib/components/Alert.svelte';

  let signingOut = false;
  let error = '';

  onMount(async () => {
    // Both stores may be uninitialised if the user deep-links here
    // before the layout's bootstrap ran. Cheap to refresh idempotently.
    if (!$auth.initialised) await auth.refresh();
    if (!$tenants.initialised) await tenants.refresh();
  });

  function roleTone(r: Role): 'success' | 'warning' | 'neutral' {
    if (r === 'owner') return 'success';
    if (r === 'admin') return 'warning';
    return 'neutral';
  }

  async function signOut() {
    signingOut = true;
    error = '';
    try {
      await auth.logout();
      await goto('/login');
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'sign-out failed';
      signingOut = false;
    }
  }

  $: user = $auth.user;
  $: displayName = user?.name || user?.preferred_username || user?.email || '—';
  $: memberships = $tenants.memberships;
</script>

<PageHeader title={t($locale, 'profile.account')} />

{#if error}
  <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
{/if}

{#if !$auth.initialised || $auth.loading}
  <p class="muted">Loading…</p>
{:else if !user}
  <EmptyState
    illustration="tenants"
    title="Not signed in"
    body="Return to the login page to continue."
  >
    <Button slot="action" on:click={() => goto('/login')}>Sign in</Button>
  </EmptyState>
{:else}
  <div class="account-grid">
    <Card>
      <div class="identity">
        <Avatar name={displayName} />
        <div class="identity-text">
          <h2 class="identity-name">{displayName}</h2>
          {#if user.preferred_username && user.preferred_username !== displayName}
            <p class="muted mono">{user.preferred_username}</p>
          {/if}
          {#if user.email}
            <p class="muted">{user.email}</p>
          {/if}
        </div>
      </div>

      <dl class="claims">
        <dt>Subject (sub)</dt>
        <dd class="mono">{user.sub}</dd>
        {#if user.roles && user.roles.length > 0}
          <dt>Global roles</dt>
          <dd>
            {#each user.roles as r (r)}
              <Badge variant="neutral">{r}</Badge>
            {/each}
          </dd>
        {/if}
      </dl>

      <div class="actions">
        <Button variant="danger" on:click={signOut} disabled={signingOut}>
          {signingOut ? 'Signing out…' : 'Sign out'}
        </Button>
      </div>
    </Card>

    <Card>
      <h2 class="card-title">Tenant access</h2>
      {#if memberships.length === 0}
        <p class="muted">
          You have no tenant memberships. The on-prem administrator can grant access from the
          <a href="/tenants">tenants</a> page.
        </p>
      {:else}
        <table class="tenant-table">
          <thead>
            <tr>
              <th>Tenant</th>
              <th>Role</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each memberships as m (m.tenant.id)}
              <tr>
                <td>
                  <div class="tenant-name">{m.tenant.name}</div>
                  <div class="muted mono">{m.tenant.slug}</div>
                </td>
                <td><Badge variant={roleTone(m.role)}>{m.role}</Badge></td>
                <td class="row-actions">
                  {#if m.is_active}
                    <Badge variant="success">active</Badge>
                  {:else}
                    <Button
                      variant="ghost"
                      on:click={() => tenants.switchTo(m.tenant.id)}
                    >
                      Switch
                    </Button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </Card>
  </div>
{/if}

<style>
  .account-grid {
    display: grid;
    gap: 16px;
    grid-template-columns: minmax(0, 1fr);
  }
  @media (min-width: 900px) {
    .account-grid {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    }
  }
  .identity {
    display: flex;
    gap: 16px;
    align-items: center;
    margin-block-end: 16px;
  }
  .identity-text {
    min-inline-size: 0;
  }
  .identity-name {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
  }
  .muted {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
  .mono {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    word-break: break-all;
  }
  .claims {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin-block-end: 16px;
  }
  .claims dt {
    color: var(--text-muted);
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .claims dd {
    margin: 0;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
  }
  .card-title {
    margin: 0 0 12px;
    font-size: 16px;
    font-weight: 600;
  }
  .tenant-table {
    inline-size: 100%;
    border-collapse: collapse;
  }
  .tenant-table th,
  .tenant-table td {
    padding: 8px 10px;
    text-align: start;
    border-block-end: 1px solid var(--border);
  }
  .tenant-table th {
    font-size: 12px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .tenant-name {
    font-weight: 500;
  }
  .row-actions {
    text-align: end;
  }
</style>
