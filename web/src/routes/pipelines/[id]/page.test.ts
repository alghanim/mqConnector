// Tests for /pipelines/[id] — the legacy form view that Wave 1 / Task 14
// gated behind ?legacy=1.
//
// The interesting behaviour here lives in +page.ts:
//
//   • plain visit                → throws a redirect into the Studio
//   • ?legacy=1                  → returns { pipelineId } and the page renders
//
// SvelteKit's `redirect(status, location)` returns an object that
// satisfies the `Redirect` shape (`status` + `location`). In a load
// function it's `throw`n; here we catch and assert on the shape.
//
// The third test renders the legacy +page.svelte and asserts the
// demotion banner is visible with the right link. The page mounts and
// kicks off fetches against /v1/pipelines/* — we stub fetch so those
// resolve cleanly and the page reaches its post-load state.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';

import { load } from './+page';
import LegacyPipelinePage from './+page.svelte';

// Minimal stand-in for SvelteKit's `LoadEvent`. The load fn only
// reaches into `params` and `url`, so a partial cast keeps strict TS
// happy without dragging in the full Kit type surface.
type LoadArgs = Parameters<typeof load>[0];

function makeLoadEvent(id: string, search: string): LoadArgs {
  const url = new URL(`http://localhost/pipelines/${id}${search}`);
  return { params: { id }, url } as unknown as LoadArgs;
}

function stubFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
  const spy = vi.fn(async (urlIn: string | URL | Request, init: RequestInit | undefined) => {
    const url = typeof urlIn === 'string' ? urlIn : urlIn.toString();
    return handler(url, init ?? {});
  });
  globalThis.fetch = spy as unknown as typeof fetch;
  return spy;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('/pipelines/[id] +page.ts load', () => {
  it('redirects to /pipelines/{id}/studio when ?legacy=1 is absent', async () => {
    let thrown: unknown = null;
    try {
      // SvelteKit's `redirect(...)` returns a value the runtime
      // recognises when the load function `throw`s it. Awaiting a
      // sync function works either way — sync return or sync throw.
      await load(makeLoadEvent('pipe-123', ''));
    } catch (e) {
      thrown = e;
    }
    expect(thrown).not.toBeNull();
    // Redirect shape: { status: 307, location: '/pipelines/pipe-123/studio' }.
    // We don't require the exact constructor identity (SvelteKit may
    // change it across minor versions), just the contract.
    const r = thrown as { status?: number; location?: string };
    expect(r.status).toBe(307);
    expect(r.location).toBe('/pipelines/pipe-123/studio');
  });

  it('redirects even when other query params are present', async () => {
    let thrown: unknown = null;
    try {
      await load(makeLoadEvent('pipe-9', '?from=palette&utm=x'));
    } catch (e) {
      thrown = e;
    }
    // The redirect itself doesn't preserve the query string — Wave 1
    // doesn't have a use-case for it and the Studio doesn't read
    // anything off the URL beyond the id. The contract is "no legacy
    // flag → go to studio"; that's what we assert.
    expect((thrown as { status?: number })?.status).toBe(307);
    expect((thrown as { location?: string })?.location).toBe('/pipelines/pipe-9/studio');
  });

  it('returns pipelineId without redirecting when ?legacy=1 is present', async () => {
    const result = await load(makeLoadEvent('pipe-abc', '?legacy=1'));
    expect(result).toEqual({ pipelineId: 'pipe-abc' });
  });

  it('does not treat ?legacy=0 or ?legacy=true as the escape hatch', async () => {
    // Strict equality with '1' — anything else is a redirect. This
    // keeps the surface tight: there's exactly one way to opt into
    // the legacy view and accidents (legacy=true, legacy=) all fall
    // through to the Studio.
    let thrown: unknown = null;
    try {
      await load(makeLoadEvent('p', '?legacy=0'));
    } catch (e) {
      thrown = e;
    }
    expect((thrown as { status?: number })?.status).toBe(307);

    thrown = null;
    try {
      await load(makeLoadEvent('p', '?legacy=true'));
    } catch (e) {
      thrown = e;
    }
    expect((thrown as { status?: number })?.status).toBe(307);
  });
});

describe('/pipelines/[id] legacy +page.svelte', () => {
  beforeEach(() => {
    // The legacy page kicks off six concurrent GETs on mount. Stub
    // every one with an empty-but-valid payload so the page reaches
    // its rendered state without throwing — we're only here to assert
    // the demotion banner, not to exercise the form.
    stubFetch(async (url) => {
      if (url.endsWith('/api/v1/pipelines/pipe-banner')) {
        return jsonResponse({
          id: 'pipe-banner',
          name: 'banner-test',
          source_id: '',
          destination_id: '',
          output_format: 'same',
          filter_paths: [],
          enabled: true
        });
      }
      if (url.endsWith('/api/v1/connections')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/pipe-banner/stages')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/pipe-banner/transforms')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/pipe-banner/routing-rules')) return jsonResponse([]);
      if (url.endsWith('/api/v1/schemas')) return jsonResponse([]);
      // Anything else (grants probes, metrics SSE) — fall through to
      // an empty 200 so the page doesn't crash. The banner doesn't
      // depend on any of it.
      return jsonResponse([]);
    });
  });

  it('renders the demotion banner with an Open in Studio link', async () => {
    const { findByText, container } = render(LegacyPipelinePage, {
      props: { data: { pipelineId: 'pipe-banner' } }
    });

    // The banner copy from locale: "Legacy form view." + body + CTA.
    expect(await findByText('Legacy form view.')).toBeInTheDocument();
    expect(await findByText('Open in Studio')).toBeInTheDocument();

    // The Open-in-Studio anchor must point at the Studio route with
    // NO ?legacy=1 — otherwise the redirect chain becomes a loop.
    await waitFor(() => {
      const link = Array.from(container.querySelectorAll('a')).find(
        (a) => (a.textContent ?? '').trim() === 'Open in Studio'
      );
      expect(link).toBeTruthy();
      expect(link!.getAttribute('href')).toBe('/pipelines/pipe-banner/studio');
    });
  });
});
