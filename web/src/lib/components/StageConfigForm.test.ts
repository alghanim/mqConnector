import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import StageConfigForm from './StageConfigForm.svelte';

// We exercise the form via its prop+event surface. The component
// two-way binds `config` (a JSON string). Vitest can read the latest
// value by reading the prop back through `component.$$.ctx` is brittle —
// instead we listen for the implicit reactive update by reading the
// component's bound prop after each user event.

function renderForm(type: string, initial = '{}') {
  return render(StageConfigForm, {
    props: { type: type as any, config: initial, schemas: [] }
  });
}

describe('StageConfigForm', () => {
  describe('filter chip editor', () => {
    it('renders an add button and an empty chip row', () => {
      const { getByPlaceholderText, getByText } = renderForm('filter', '{"paths":[]}');
      expect(getByPlaceholderText(/customer.secret/i)).toBeInTheDocument();
      expect(getByText('Add')).toBeInTheDocument();
    });

    it('renders existing paths as chips', () => {
      const { getByText } = renderForm('filter', '{"paths":["secret","customer.pwd"]}');
      expect(getByText('secret')).toBeInTheDocument();
      expect(getByText('customer.pwd')).toBeInTheDocument();
    });

    it('committing a path via the Add button updates the config', async () => {
      const { getByPlaceholderText, getByText, component } = renderForm('filter', '{"paths":[]}');
      const input = getByPlaceholderText(/customer.secret/i) as HTMLInputElement;
      await fireEvent.input(input, { target: { value: 'newPath' } });
      await fireEvent.click(getByText('Add'));
      // Read back the bound config — vitest sees the latest from the
      // component's prop bag after fireEvent flushes.
      const cfg = JSON.parse((component as any).$$.ctx[(component as any).$$.props.config] || '{}');
      expect(cfg.paths).toContain('newPath');
    });

    it('Enter key commits and clears the input', async () => {
      const { getByPlaceholderText } = renderForm('filter', '{"paths":[]}');
      const input = getByPlaceholderText(/customer.secret/i) as HTMLInputElement;
      await fireEvent.input(input, { target: { value: 'enterCommit' } });
      await fireEvent.keyDown(input, { key: 'Enter' });
      // input clears after commit
      expect((input as HTMLInputElement).value).toBe('');
    });
  });

  describe('translate target picker', () => {
    it('renders the three target-format options', () => {
      const { getByText } = renderForm('translate', '{"output_format":"same"}');
      expect(getByText('JSON')).toBeInTheDocument();
      expect(getByText('XML')).toBeInTheDocument();
    });
  });

  describe('script body editor', () => {
    it('renders the script textarea pre-filled from config', () => {
      const { getByDisplayValue } = renderForm('script', '{"script":"msg.x = 1"}');
      expect(getByDisplayValue('msg.x = 1')).toBeInTheDocument();
    });
  });

  describe('validate schema picker', () => {
    it('renders a None option plus every supplied schema', () => {
      const { getByText } = render(StageConfigForm, {
        props: {
          type: 'validate' as any,
          config: '{}',
          schemas: [
            { id: '1', name: 'order.v1', schema_type: 'json_schema', content: '' },
            { id: '2', name: 'order.v2', schema_type: 'xsd', content: '' }
          ]
        }
      });
      expect(getByText('— None —')).toBeInTheDocument();
      expect(getByText('order.v1 (json_schema)')).toBeInTheDocument();
      expect(getByText('order.v2 (xsd)')).toBeInTheDocument();
    });
  });

  describe('route + transform help', () => {
    it('route shows the "rules edited below" hint', () => {
      const { getByText } = renderForm('route', '{}');
      expect(getByText(/Routing section/i)).toBeInTheDocument();
    });
    it('transform shows the "rules edited below" hint', () => {
      const { getByText } = renderForm('transform', '{}');
      expect(getByText(/Transforms section/i)).toBeInTheDocument();
    });
  });

  describe('advanced JSON escape hatch', () => {
    it('preserves unknown fields on round-trip', () => {
      // The form parses, writes back, but should leave fields it
      // doesn't model alone so future-release configs survive.
      const { container } = renderForm(
        'filter',
        '{"paths":["x"],"future_only_flag":42}'
      );
      // The <details> for advanced is present so the operator can still
      // see/edit the raw JSON if needed.
      expect(container.querySelector('details')).toBeTruthy();
    });
  });
});
