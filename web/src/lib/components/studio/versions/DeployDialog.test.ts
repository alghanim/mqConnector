// DeployDialog tests — five cases cover the dialog's contract:
//
//   1. kind='deploy' POSTs /api/v1/pipelines/{id}/deploy with the
//      revision_number + change_summary.
//   2. kind='rollback' POSTs /api/v1/pipelines/{id}/revisions/{rev}/rollback.
//   3. Approver gate: when requiresApproval is true, the confirm
//      button is disabled while the approver field is empty.
//   4. 409 response surfaces "Approver required" under the field and
//      keeps the dialog open.
//   5. Success calls studio.hydrate AND emits the `done` event.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import DeployDialog from './DeployDialog.svelte';
import { studio } from '$lib/stores/studio';

beforeEach(() => {
  studio.reset();
});

afterEach(() => {
  studio.reset();
  vi.restoreAllMocks();
});

describe('DeployDialog', () => {
  it("kind='deploy' POSTs /v1/pipelines/{id}/deploy with the target rev", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      calls.push({ url: u, init });
      if (u.endsWith('/v1/pipelines/p1/deploy')) {
        return new Response(JSON.stringify({ revision_number: 4 }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      // Refuse the hydrate cascade — the test only cares about the
      // POST. Returning 5xx makes the hydrate-on-success path bail
      // gracefully without breaking the assertion.
      return new Response('nf', { status: 500 });
    }) as unknown as typeof fetch;

    const { getByText } = render(DeployDialog, {
      props: {
        kind: 'deploy',
        pipelineId: 'p1',
        targetRev: 4,
        liveRev: null,
        diff: null,
        requiresApproval: false,
        open: true
      }
    });

    await fireEvent.click(getByText('Deploy'));
    await waitFor(() => {
      const found = calls.find((c) => c.url.endsWith('/v1/pipelines/p1/deploy'));
      expect(found).toBeTruthy();
      const body = JSON.parse((found!.init?.body as string) ?? '{}');
      expect(body.revision_number).toBe(4);
    });
  });

  it("kind='rollback' POSTs /v1/pipelines/{id}/revisions/{rev}/rollback", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      calls.push({ url: u, init });
      if (u.endsWith('/v1/pipelines/p1/revisions/2/rollback')) {
        return new Response(JSON.stringify({ revision_number: 9 }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      return new Response('nf', { status: 500 });
    }) as unknown as typeof fetch;

    const { getByText, getByPlaceholderText } = render(DeployDialog, {
      props: {
        kind: 'rollback',
        pipelineId: 'p1',
        targetRev: 2,
        liveRev: null,
        diff: null,
        requiresApproval: false,
        open: true
      }
    });
    const textarea = getByPlaceholderText(/Why are you rolling back/i) as HTMLTextAreaElement;
    await fireEvent.input(textarea, { target: { value: 'broke prod' } });
    await fireEvent.click(getByText('Rollback'));
    await waitFor(() => {
      const found = calls.find((c) => c.url.endsWith('/v1/pipelines/p1/revisions/2/rollback'));
      expect(found).toBeTruthy();
      const body = JSON.parse((found!.init?.body as string) ?? '{}');
      expect(body.change_summary).toBe('broke prod');
    });
  });

  it('Approver gate disables the confirm button while approver is empty', async () => {
    globalThis.fetch = vi.fn(async () => new Response('{}', { status: 200 })) as unknown as typeof fetch;
    const { getByText } = render(DeployDialog, {
      props: {
        kind: 'deploy',
        pipelineId: 'p1',
        targetRev: 4,
        liveRev: null,
        diff: null,
        requiresApproval: true,
        open: true
      }
    });
    const confirm = getByText('Deploy').closest('button') as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
  });

  it('409 response surfaces the approver error and keeps the dialog open', async () => {
    globalThis.fetch = vi.fn(async (url: string | URL | Request) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      if (u.endsWith('/v1/pipelines/p1/deploy')) {
        return new Response(JSON.stringify({ error: 'approver required' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      return new Response('nf', { status: 500 });
    }) as unknown as typeof fetch;

    const { getByText, container } = render(DeployDialog, {
      props: {
        kind: 'deploy',
        pipelineId: 'p1',
        targetRev: 4,
        liveRev: null,
        diff: null,
        requiresApproval: true,
        open: true
      }
    });
    // Fill the approver so the button becomes enabled, then click.
    const approver = container.querySelector('input[type="text"]') as HTMLInputElement;
    await fireEvent.input(approver, { target: { value: 'manager' } });
    await fireEvent.click(getByText('Deploy'));
    await waitFor(() => {
      // The dialog stays open — the dialog primitive renders the
      // title; if it had closed the title would no longer be present.
      expect(getByText(/Deploy revision/i)).toBeInTheDocument();
      // The approver-error chunk surfaces beneath the field.
      expect(container.querySelector('#approver-err')).not.toBeNull();
    });
  });

  it('success calls studio.hydrate and emits the done event', async () => {
    const hydrateSpy = vi.spyOn(studio, 'hydrate').mockResolvedValue(undefined);
    globalThis.fetch = vi.fn(async (url: string | URL | Request) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      if (u.endsWith('/v1/pipelines/p1/deploy')) {
        return new Response(JSON.stringify({ revision_number: 11 }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      return new Response('nf', { status: 404 });
    }) as unknown as typeof fetch;

    const dones: Array<{ revision: { revision_number?: number } }> = [];
    const { getByText } = render(DeployDialog, {
      props: {
        kind: 'deploy',
        pipelineId: 'p1',
        targetRev: 7,
        liveRev: null,
        diff: null,
        requiresApproval: false,
        open: true
      },
      events: {
        done: (e: CustomEvent<{ revision: { revision_number?: number } }>) =>
          dones.push(e.detail)
      }
    });
    await fireEvent.click(getByText('Deploy'));
    await waitFor(() => {
      expect(hydrateSpy).toHaveBeenCalledWith('p1');
      expect(dones.length).toBe(1);
      expect(dones[0].revision.revision_number).toBe(11);
    });
  });
});
