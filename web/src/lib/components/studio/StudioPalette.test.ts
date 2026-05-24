// StudioPalette tests — verify the drag-source contract and the click
// fallback. These two interactions are the entire palette API; the
// Studio shell + canvas both depend on them behaving exactly as
// documented.
//
// Four tests:
//   1. The seven stage type cards all render.
//   2. Click emits `select` with the stage type.
//   3. dragstart sets the correct mime + payload on dataTransfer.
//   4. Each card is keyboard-reachable + carries an aria-label.
import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import StudioPalette from './StudioPalette.svelte';
import type { StudioStageType } from '$lib/stores/studio';

const ALL_TYPES: StudioStageType[] = [
  'filter',
  'transform',
  'translate',
  'validate',
  'route',
  'script',
  'wasm'
];

describe('StudioPalette', () => {
  it('renders one card per stage type (7 total)', () => {
    const { container } = render(StudioPalette);
    const cards = container.querySelectorAll('.studio-palette-card');
    expect(cards.length).toBe(ALL_TYPES.length);
    // Each card carries a data-stage-type attribute. Collect them and
    // assert the set matches the documented stage types.
    const types = Array.from(cards).map((c) => c.getAttribute('data-stage-type'));
    expect(new Set(types)).toEqual(new Set(ALL_TYPES));
  });

  it('emits select with the right stage type on click', async () => {
    const got: StudioStageType[] = [];
    const { container } = render(StudioPalette, {
      events: {
        select: (e: CustomEvent<StudioStageType>) => got.push(e.detail)
      }
    });
    const filterCard = container.querySelector(
      '[data-stage-type="filter"]'
    ) as HTMLElement | null;
    expect(filterCard).not.toBeNull();
    await fireEvent.click(filterCard!);
    expect(got).toEqual(['filter']);
  });

  it("sets 'application/x-mqc-stage-type' on dragstart with the stage payload", async () => {
    const { container } = render(StudioPalette);
    const routeCard = container.querySelector(
      '[data-stage-type="route"]'
    ) as HTMLElement | null;
    expect(routeCard).not.toBeNull();

    // jsdom doesn't synthesise a DataTransfer for fireEvent.dragStart;
    // build one ourselves and assert that the component called setData
    // with the expected mime + payload.
    const recorded: Array<{ type: string; data: string }> = [];
    const fakeDT = {
      effectAllowed: '',
      types: [] as string[],
      setData(type: string, data: string) {
        recorded.push({ type, data });
      },
      getData(type: string) {
        return recorded.find((r) => r.type === type)?.data ?? '';
      }
    };

    const evt = new Event('dragstart', { bubbles: true, cancelable: true });
    Object.defineProperty(evt, 'dataTransfer', { value: fakeDT });
    routeCard!.dispatchEvent(evt);

    expect(recorded).toEqual([{ type: 'application/x-mqc-stage-type', data: 'route' }]);
    expect(fakeDT.effectAllowed).toBe('copy');
  });

  it('cards are keyboard-focusable and carry an aria-label', () => {
    const { container } = render(StudioPalette);
    const cards = container.querySelectorAll<HTMLElement>('.studio-palette-card');
    for (const card of Array.from(cards)) {
      expect(card.getAttribute('tabindex')).toBe('0');
      expect(card.getAttribute('aria-label')).toBeTruthy();
      expect(card.getAttribute('role')).toBe('button');
    }
  });
});
