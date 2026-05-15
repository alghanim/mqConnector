import { describe, it, expect } from 'vitest';
// Node built-ins — used to cross-check inline fixtures against the on-disk
// copies in testdata/samples. @types/node isn't a project dep (web is a
// browser bundle), so the imports are typed via the suppression below.
// @ts-expect-error — node built-in, untyped in this project
import { readFileSync, existsSync } from 'node:fs';
// @ts-expect-error — node built-in, untyped in this project
import { resolve, dirname } from 'node:path';
// @ts-expect-error — node built-in, untyped in this project
import { fileURLToPath } from 'node:url';
import { SAMPLE_ORDER_JSON, SAMPLE_PAYMENT_XML, SAMPLE_FIXTURES } from './sample-fixtures';

// The inline fixtures here are the "Try a sample" payloads the editor
// loads in one click. testdata/samples/{order.json,payment.xml} are the
// same content as files an operator could open from disk. Pin that they
// stay equivalent: a copy-paste mistake or a forgotten update to one
// side breaks the test.

// ESM-friendly __dirname equivalent — Vitest runs as ESM modules.
const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '..', '..', '..');
const orderPath = resolve(repoRoot, 'testdata', 'samples', 'order.json');
const paymentPath = resolve(repoRoot, 'testdata', 'samples', 'payment.xml');

describe('SAMPLE_FIXTURES', () => {
  it('exposes both order.json and payment.xml', () => {
    expect(SAMPLE_FIXTURES.map((f) => f.label).sort()).toEqual(
      ['order.json', 'payment.xml']
    );
  });

  it('inline order JSON parses cleanly', () => {
    const parsed = JSON.parse(SAMPLE_ORDER_JSON);
    expect(parsed.customer.ssn).toBeDefined();
    expect(parsed.payment.card_token).toBeDefined();
    expect(parsed.items).toHaveLength(2);
  });

  it('inline payment XML carries the fields the demo strips', () => {
    expect(SAMPLE_PAYMENT_XML).toContain('<Originator>');
    expect(SAMPLE_PAYMENT_XML).toContain('<RoutingNumber>');
    expect(SAMPLE_PAYMENT_XML).toContain('<Iban>');
  });

  // Equivalence pin: same logical content as the on-disk fixtures.
  // We compare JSON structurally (whitespace differs because the inline
  // is `JSON.stringify(obj, null, 2)`) and XML byte-for-byte after
  // trimming trailing newlines (file may carry one).
  it('inline order.json matches testdata/samples/order.json structurally', () => {
    if (!existsSync(orderPath)) {
      throw new Error('testdata/samples/order.json missing — fixtures out of sync');
    }
    const onDisk = JSON.parse(readFileSync(orderPath, 'utf8'));
    const inline = JSON.parse(SAMPLE_ORDER_JSON);
    expect(inline).toEqual(onDisk);
  });

  it('inline payment.xml matches testdata/samples/payment.xml byte-for-byte (ignoring trailing whitespace)', () => {
    if (!existsSync(paymentPath)) {
      throw new Error('testdata/samples/payment.xml missing — fixtures out of sync');
    }
    const onDisk = readFileSync(paymentPath, 'utf8').replace(/\s+$/, '');
    const inline = SAMPLE_PAYMENT_XML.replace(/\s+$/, '');
    expect(inline).toEqual(onDisk);
  });
});
