<!--
  /tokens — API token management.

  Tokens are headless credentials for CI and automation: the operator
  mints a token, copies the secret once, and uses it as
  `Authorization: Bearer mqct_…` from outside the browser.

  Lifecycle:
    1. POST /api/v1/tokens with {name, role, expires_in?} →
       returns {token, secret, warning}. The plaintext `secret` is
       displayed exactly once in a modal with a Copy button + a
       prominent warning. After dismissing the modal it's gone.
    2. GET /api/v1/tokens lists every token (no secrets, only prefix
       for identification). Rows show last-used + expiry + status.
    3. DELETE /api/v1/tokens/{id} revokes — anything authenticating
       with that secret starts returning 401 immediately.

  The page is deliberately conservative: no in-place edit, no
  rotation (revoke + recreate is the supported pattern).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type APIToken, type APITokenCreateResponse, type Role } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Plus, Copy, Check, Trash2 } from 'lucide-svelte';

  // ─── State ────────────────────────────────────────────────────────
  let tokens: APIToken[] = [];
  let loading = true;

  // Create-form state
  let creating = false;
  let saving = false;
  let formName = '';
  let formRole: Role = 'admin';
  let formExpiresInStr = '0'; // seconds as string for the Select bind; 0 = never

  // One-shot reveal modal — only populated immediately after a create
  let justCreated: APITokenCreateResponse | null = null;
  let copied = false;

  // Confirm-revoke modal
  let pendingRevoke: APIToken | null = null;
  let revoking = false;

  // ─── Data ─────────────────────────────────────────────────────────
  async function refresh() {
    loading = true;
    try {
      const res = await api.get<{ items: APIToken[] }>('/v1/tokens');
      tokens = res.items ?? [];
    } catch (e: unknown) {
      toasts.error('Failed to load tokens', (e as { message?: string }).message ?? '');
    } finally {
      loading = false;
    }
  }
  onMount(refresh);

  // ─── Create ───────────────────────────────────────────────────────
  function openCreate() {
    formName = '';
    formRole = 'admin';
    formExpiresInStr = '0';
    creating = true;
  }
  async function submitCreate() {
    saving = true;
    try {
      const body: Record<string, unknown> = { name: formName.trim(), role: formRole };
      const seconds = Number(formExpiresInStr);
      if (seconds > 0) body.expires_in_seconds = seconds;
      const res = await api.post<APITokenCreateResponse>('/v1/tokens', body);
      justCreated = res;
      copied = false;
      creating = false;
      tokens = [res.token, ...tokens];
      toasts.success(t($locale, 'tokens.create.success'));
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message ?? 'create failed';
      toasts.error('Could not create token', msg);
    } finally {
      saving = false;
    }
  }
  async function copySecret() {
    if (!justCreated?.secret) return;
    try {
      await navigator.clipboard.writeText(justCreated.secret);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      // Clipboard API unavailable (e.g. http context); leave the
      // secret visible for manual selection.
    }
  }

  // ─── Revoke ───────────────────────────────────────────────────────
  function askRevoke(tok: APIToken) {
    pendingRevoke = tok;
  }
  async function confirmRevoke() {
    if (!pendingRevoke) return;
    revoking = true;
    try {
      await api.del(`/v1/tokens/${pendingRevoke.id}`);
      // Optimistic update: mark as revoked in place so the row's status
      // flips immediately without a refetch.
      const id = pendingRevoke.id;
      tokens = tokens.map((t) => (t.id === id ? { ...t, revoked_at: new Date().toISOString() } : t));
      pendingRevoke = null;
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message ?? 'revoke failed';
      toasts.error('Could not revoke token', msg);
    } finally {
      revoking = false;
    }
  }

  // ─── Derived ──────────────────────────────────────────────────────
  function statusOf(tk: APIToken): 'active' | 'revoked' | 'expired' {
    if (tk.revoked_at) return 'revoked';
    if (tk.expires_at && new Date(tk.expires_at) <= new Date()) return 'expired';
    return 'active';
  }
  $: stats = tokens.reduce(
    (acc, t) => {
      acc[statusOf(t)]++;
      return acc;
    },
    { active: 0, revoked: 0, expired: 0 }
  );

  $: roleOptions = [
    { value: 'viewer', label: 'viewer' },
    { value: 'operator', label: 'operator' },
    { value: 'admin', label: 'admin' },
    { value: 'owner', label: 'owner' }
  ];
  $: expiryOptions = [
    { value: '0', label: t($locale, 'tokens.expiresIn.never') },
    { value: String(7 * 86400), label: t($locale, 'tokens.expiresIn.7d') },
    { value: String(30 * 86400), label: t($locale, 'tokens.expiresIn.30d') },
    { value: String(90 * 86400), label: t($locale, 'tokens.expiresIn.90d') },
    { value: String(365 * 86400), label: t($locale, 'tokens.expiresIn.365d') }
  ];

  function fmtDate(s: string | null | undefined): string {
    if (!s) return t($locale, 'tokens.never');
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }
</script>

<div class="space-y-6 max-w-6xl">
  <PageHeader
    title={t($locale, 'tokens.title')}
    subtitle={t($locale, 'tokens.subtitle')}
  >
    <svelte:fragment slot="stats">
      <StatChip label={t($locale, 'tokens.stat.active')} value={String(stats.active)} tone="success" />
      <StatChip label={t($locale, 'tokens.stat.revoked')} value={String(stats.revoked)} />
      <StatChip label={t($locale, 'tokens.stat.expired')} value={String(stats.expired)} />
    </svelte:fragment>
    <svelte:fragment slot="primary">
      <Button on:click={openCreate}>
        <Plus size={14} strokeWidth={1.75} />
        <span>{t($locale, 'tokens.new')}</span>
      </Button>
    </svelte:fragment>
  </PageHeader>

  <Card>
    {#if loading}
      <p class="muted">{t($locale, 'common.loading')}</p>
    {:else if tokens.length === 0}
      <EmptyState
        illustration="tenants"
        title={t($locale, 'empty.tokens.title')}
        body={t($locale, 'empty.tokens.body')}
      >
        <svelte:fragment slot="action">
          <Button on:click={openCreate}>
            <Plus size={14} strokeWidth={1.75} />
            <span>{t($locale, 'tokens.new')}</span>
          </Button>
        </svelte:fragment>
      </EmptyState>
    {:else}
      <table class="table tokens-table" aria-label={t($locale, 'tokens.title')}>
        <thead>
          <tr>
            <th>{t($locale, 'tokens.name')}</th>
            <th>{t($locale, 'tokens.prefix')}</th>
            <th>{t($locale, 'tokens.role')}</th>
            <th>{t($locale, 'tokens.lastUsed')}</th>
            <th>{t($locale, 'tokens.expires')}</th>
            <th>{t($locale, 'common.status')}</th>
            <th><span class="sr-only">{t($locale, 'common.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each tokens as tk (tk.id)}
            {@const s = statusOf(tk)}
            <tr>
              <td class="tokens-name">{tk.name}</td>
              <td>
                <code class="tokens-prefix">mqct_{tk.prefix}…</code>
              </td>
              <td>{tk.role}</td>
              <td class="muted">{fmtDate(tk.last_used_at)}</td>
              <td class="muted">{tk.expires_at ? fmtDate(tk.expires_at) : t($locale, 'tokens.never')}</td>
              <td>
                <Badge variant={s === 'active' ? 'success' : s === 'expired' ? 'warning' : 'neutral'}>
                  {t($locale, 'tokens.status.' + s)}
                </Badge>
              </td>
              <td>
                <div class="flex justify-end gap-2">
                  {#if s === 'active'}
                    <Button variant="outline" on:click={() => askRevoke(tk)}>
                      <Trash2 size={14} strokeWidth={1.75} />
                      <span>{t($locale, 'tokens.revoke')}</span>
                    </Button>
                  {/if}
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>

<!-- ─── Create dialog ─────────────────────────────────────────────── -->
<Dialog
  open={creating}
  title={t($locale, 'tokens.create.title')}
  confirmLabel={t($locale, 'tokens.create.button')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={saving}
  on:confirm={submitCreate}
  on:cancel={() => (creating = false)}
>
  <div class="space-y-3">
    <Input bind:value={formName} label={t($locale, 'tokens.name')} placeholder={t($locale, 'tokens.namePlaceholder')} />
    <Select bind:value={formRole} options={roleOptions} label={t($locale, 'tokens.role')} />
    <p class="hint">{t($locale, 'tokens.roleHint')}</p>
    <Select bind:value={formExpiresInStr} options={expiryOptions} label={t($locale, 'tokens.expiresIn')} />
  </div>
</Dialog>

<!-- ─── One-shot secret reveal ───────────────────────────────────── -->
{#if justCreated}
  <div class="reveal-scrim" role="presentation" on:click|self={() => (justCreated = null)}>
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
    <div class="reveal-panel" role="dialog" aria-modal="true" aria-labelledby="reveal-title" on:click|stopPropagation>
      <h2 id="reveal-title" class="reveal-title">{t($locale, 'tokens.create.success')}</h2>
      <p class="reveal-warning">{t($locale, 'tokens.secret.warning')}</p>
      <div class="reveal-secret-row">
        <code class="reveal-secret">{justCreated.secret}</code>
        <button type="button" class="reveal-copy" on:click={copySecret} aria-live="polite">
          {#if copied}
            <Check size={14} strokeWidth={2} />
            <span>{t($locale, 'tokens.secret.copied')}</span>
          {:else}
            <Copy size={14} strokeWidth={1.75} />
            <span>{t($locale, 'tokens.secret.copy')}</span>
          {/if}
        </button>
      </div>
      <div class="reveal-foot">
        <Button on:click={() => (justCreated = null)}>
          {t($locale, 'tokens.secret.dismiss')}
        </Button>
      </div>
    </div>
  </div>
{/if}

<!-- ─── Revoke confirm ───────────────────────────────────────────── -->
<Dialog
  open={pendingRevoke !== null}
  title={t($locale, 'tokens.revoke.confirm.title')}
  confirmLabel={t($locale, 'tokens.revoke')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={revoking}
  on:confirm={confirmRevoke}
  on:cancel={() => (pendingRevoke = null)}
>
  <p>{t($locale, 'tokens.revoke.confirm.body')}</p>
  {#if pendingRevoke}
    <p class="hint mt-2">
      <strong>{pendingRevoke.name}</strong>
      &middot; <code>mqct_{pendingRevoke.prefix}…</code>
    </p>
  {/if}
</Dialog>

<style>
  .muted {
    color: var(--text-muted);
  }
  .hint {
    color: var(--text-tertiary);
    font-size: 12px;
    line-height: 1.45;
  }
  .tokens-table tbody tr {
    content-visibility: auto;
    contain-intrinsic-size: auto 44px;
  }
  .tokens-name {
    color: var(--text);
    font-weight: 500;
  }
  .tokens-prefix {
    /* Labeled chip per design system §5.5 / §7 rule 10 → 12dp. The
       prefix string is a stable visual identifier; treat as a
       category chip rather than a free-form code span. */
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    background: var(--surface-2);
    padding: 2px 6px;
    border-radius: 12px;
    color: var(--text);
  }

  /* ─── One-shot reveal modal ─────────────────────────────────────
   * The secret is displayed exactly once. We use a bespoke modal
   * (not Dialog.svelte) so the copy interaction and the dismiss
   * button feel distinct from a confirm-flow dialog.
   */
  .reveal-scrim {
    position: fixed;
    inset: 0;
    background: var(--dialog-scrim);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: 16px;
  }
  .reveal-panel {
    background: var(--dialog-bg);
    border: 1px solid var(--card-border);
    border-radius: 16px; /* container per §7 rule 10 */
    padding: 24px;
    inline-size: 100%;
    max-inline-size: 600px;
    box-shadow: var(--dialog-shadow); /* §5.14 + §5.2 elevation step */
  }
  .reveal-title {
    margin: 0 0 8px;
    color: var(--text);
    font-size: 16px;
    font-weight: 600;
  }
  .reveal-warning {
    /* §5.10 alert spec → 12dp corner radius. */
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--warning) 30%, transparent);
    border-radius: 12px;
    padding: 8px 12px;
    font-size: 13px;
    margin-block-end: 12px;
  }
  .reveal-secret-row {
    /* Interactive surface (user select / copy target) → 12dp per
       §7 rule 10. */
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--bg);
    border: 1px solid var(--card-border);
    border-radius: 12px;
    padding: 10px 12px;
  }
  .reveal-secret {
    flex: 1;
    min-inline-size: 0;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 13px;
    word-break: break-all;
    user-select: all;
  }
  .reveal-copy {
    /* Interactive button → 12dp per §7 rule 10. */
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    color: var(--text);
    font-size: 12px;
    cursor: pointer;
  }
  .reveal-copy:hover {
    border-color: var(--secondary);
    color: var(--secondary);
  }
  :global([data-theme='light']) .reveal-copy:hover {
    border-color: var(--primary);
    color: var(--primary);
  }
  .reveal-foot {
    display: flex;
    justify-content: flex-end;
    margin-block-start: 16px;
  }
</style>
