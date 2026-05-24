// StudioHeader tests — pin every state→chip mapping plus the
// Validate/Deploy event surface. The header is the visual contract for
// the Studio's state machine; getting this right matters because every
// subsequent task assumes the chip + buttons behave as documented.
import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import StudioHeader from './StudioHeader.svelte';
import type { StudioState } from '$lib/stores/studio';

type RenderProps = {
  pipelineId?: string;
  name?: string;
  enabled?: boolean;
  dirtyCount?: number;
  state: StudioState;
  latestRev?: number | null;
  deployedRev?: number | null;
  comparisonFrom?: number | null;
  comparisonTo?: number | null;
};

function renderHeader(over: RenderProps) {
  return render(StudioHeader, {
    props: {
      pipelineId: 'p1',
      name: 'My Pipeline',
      enabled: true,
      dirtyCount: 0,
      latestRev: null,
      deployedRev: null,
      comparisonFrom: null,
      comparisonTo: null,
      ...over
    }
  });
}

describe('StudioHeader chip text', () => {
  const cases: Array<{
    state: StudioState;
    extra?: Partial<RenderProps>;
    expect: RegExp;
  }> = [
    { state: 'empty', expect: /New draft/i },
    { state: 'building', expect: /Editing/i },
    { state: 'dirty', extra: { dirtyCount: 3 }, expect: /Unsaved changes \(3\)/i },
    { state: 'validating', expect: /Validating/i },
    {
      state: 'deploying',
      extra: { latestRev: 7 },
      expect: /Deploying rev #7/i
    },
    { state: 'error', expect: /Build failed/i },
    {
      state: 'simulating',
      extra: { latestRev: 4 },
      expect: /Dry-run #4/i
    },
    {
      state: 'version-comparing',
      extra: { comparisonFrom: 5, comparisonTo: 8 },
      expect: /Comparing rev #5 → #8/i
    }
  ];

  for (const c of cases) {
    it(`shows the right chip for state='${c.state}'`, () => {
      const { container } = renderHeader({ state: c.state, ...c.extra });
      const chip = container.querySelector('.studio-header-chip');
      expect(chip).not.toBeNull();
      expect(chip?.textContent || '').toMatch(c.expect);
    });
  }

  it("dirty state shows 'Unsaved changes (N)' with the provided count", () => {
    const { container } = renderHeader({ state: 'dirty', dirtyCount: 12 });
    expect(container.querySelector('.studio-header-chip')?.textContent || '').toMatch(
      /Unsaved changes \(12\)/
    );
  });
});

describe('StudioHeader actions', () => {
  it('Deploy button is disabled when state=error', () => {
    const { getByText } = renderHeader({ state: 'error' });
    const deploy = getByText('Deploy').closest('button')!;
    expect(deploy).toBeDisabled();
  });

  it('Deploy button is enabled in the regular building state', () => {
    const { getByText } = renderHeader({ state: 'building' });
    const deploy = getByText('Deploy').closest('button')!;
    expect(deploy).not.toBeDisabled();
  });

  it('Validate and Deploy emit their events on click', async () => {
    let validated = false;
    let deployed = false;
    const { getByText } = render(StudioHeader, {
      props: {
        pipelineId: 'p1',
        name: 'P',
        enabled: true,
        dirtyCount: 0,
        state: 'building' as StudioState,
        latestRev: null,
        deployedRev: null
      },
      events: {
        validate: () => (validated = true),
        deploy: () => (deployed = true)
      }
    });
    await fireEvent.click(getByText('Validate'));
    await fireEvent.click(getByText('Deploy'));
    expect(validated).toBe(true);
    expect(deployed).toBe(true);
  });

  it('omits the progress strip in non-deploying states', () => {
    const { container } = renderHeader({ state: 'building' });
    expect(container.querySelector('.studio-header-progress')).toBeNull();
  });

  it("renders the cyan progress strip when state='deploying'", () => {
    const { container } = renderHeader({ state: 'deploying', latestRev: 7 });
    expect(container.querySelector('.studio-header-progress')).not.toBeNull();
  });
});
