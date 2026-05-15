/**
 * Inline copies of the testdata/samples/ fixtures so an operator can
 * load a representative message into the editor with one click,
 * without having to find the files on disk. Kept in sync with
 * testdata/samples/{order.json,payment.xml}; pinned by the test below.
 */

export const SAMPLE_ORDER_JSON = JSON.stringify(
  {
    order_id: 'ORD-2026-0042',
    placed_at: '2026-05-15T14:23:17Z',
    currency: 'USD',
    customer: {
      id: 'C-91823',
      name: 'Alice Johnson',
      email: 'alice@example.com',
      ssn: '123-45-6789',
      phone: '+1-555-0142',
      shipping_address: {
        line1: '742 Evergreen Terrace',
        city: 'Springfield',
        postal_code: '97477',
        country: 'US'
      }
    },
    payment: {
      method: 'card',
      card_last4: '4242',
      card_token: 'tok_live_AbCdEfGhIjKlMnOpQrStUvWx',
      billing_cvv: '***'
    },
    items: [
      { sku: 'BOOK-001', title: 'The Pragmatic Programmer', qty: 1, unit_price: 39.99 },
      { sku: 'MUG-007', title: 'Sarcasm fuel', qty: 2, unit_price: 12.5 }
    ],
    total: 64.99,
    internal_notes: 'Flagged for fraud review — manual approval required.'
  },
  null,
  2
);

export const SAMPLE_PAYMENT_XML = `<?xml version="1.0" encoding="UTF-8"?>
<Payment>
  <Header>
    <MessageId>PAY-2026-05-15-000042</MessageId>
    <Timestamp>2026-05-15T14:23:17Z</Timestamp>
    <Source>online-banking</Source>
  </Header>
  <Originator>
    <Name>Alice Johnson</Name>
    <Account>
      <Number>1234567890</Number>
      <RoutingNumber>021000021</RoutingNumber>
      <Iban>US12ABCD0000001234567890</Iban>
    </Account>
  </Originator>
  <Beneficiary>
    <Name>Coffee Roasters Inc</Name>
    <Account>
      <Number>9876543210</Number>
      <RoutingNumber>121000358</RoutingNumber>
    </Account>
    <Address>
      <Line1>500 5th Ave</Line1>
      <City>Seattle</City>
      <PostalCode>98104</PostalCode>
      <Country>US</Country>
    </Address>
  </Beneficiary>
  <Amount>
    <Currency>USD</Currency>
    <Value>1500.00</Value>
  </Amount>
  <Reference>Invoice INV-7781</Reference>
  <InternalAudit>
    <ReviewedBy>compliance-bot</ReviewedBy>
    <SanctionScreening>cleared</SanctionScreening>
    <RiskScore>0.07</RiskScore>
  </InternalAudit>
</Payment>
`;

/** Catalogue used by the editor's "Try a sample" affordance. */
export const SAMPLE_FIXTURES = [
  { label: 'order.json', body: SAMPLE_ORDER_JSON },
  { label: 'payment.xml', body: SAMPLE_PAYMENT_XML }
];
