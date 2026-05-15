<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Input from '$lib/components/Input.svelte';
  import Button from '$lib/components/Button.svelte';
  import Alert from '$lib/components/Alert.svelte';

  let username = '';
  let password = '';
  let loading = false;
  let error = '';

  async function submit() {
    loading = true;
    error = '';
    try {
      await auth.login(username, password);
      goto('/');
    } catch (e: unknown) {
      const err = e as { message?: string };
      error = err.message || t($locale, 'login.error');
    } finally {
      loading = false;
    }
  }
</script>

<div class="min-h-screen flex items-center justify-center px-4">
  <div class="w-full max-w-md">
    <Card strip padding="lg">
      <div class="mb-6">
        <h1 class="text-xl font-semibold" style="color: var(--text)">
          {t($locale, 'login.title')}
        </h1>
        <p class="mt-1 text-sm" style="color: var(--text-muted)">
          {t($locale, 'app.subtitle')}
        </p>
      </div>

      <form on:submit|preventDefault={submit} class="space-y-4">
        <Input
          bind:value={username}
          label={t($locale, 'login.username')}
          autocomplete="username"
          required
        />
        <Input
          bind:value={password}
          type="password"
          label={t($locale, 'login.password')}
          autocomplete="current-password"
          required
        />
        {#if error}
          <Alert variant="error">{error}</Alert>
        {/if}
        <Button type="submit" fullWidth {loading}>{t($locale, 'login.submit')}</Button>
      </form>
    </Card>
  </div>
</div>
