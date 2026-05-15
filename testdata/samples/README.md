# Sample messages

Two realistic message bodies for demoing the **Sample → filter → deploy**
workflow on a fresh install. Neither file is loaded by the binary; they
exist solely to give an operator (or a reviewer) something concrete to
paste into the editor.

| File | Format | What it represents | Good fields to filter |
|---|---|---|---|
| [`order.json`](./order.json) | JSON | E-commerce order with customer PII + payment data | `customer.ssn`, `customer.email`, `payment.card_token`, `payment.card_last4`, `internal_notes` |
| [`payment.xml`](./payment.xml) | XML | Bank wire payment with originator/beneficiary account details | `Originator.Account.Number`, `Originator.Account.RoutingNumber`, `Originator.Account.Iban`, `Beneficiary.Account.Number`, `InternalAudit` |

## Demo flow (30 seconds)

1. Log in to <https://localhost:8443/> as `admin` / `Changeme1!`.
2. Open the pipeline editor (`/pipelines/<id>`) or the visual builder (`/flow`).
3. Scroll to the **Sample & preview** card (or the **Sample** section in the flow palette).
4. Upload `order.json` (or `payment.xml`).
5. The chip row populates with every dot-path found inside. Click the ones
   you want stripped — they turn filled and land in the filter stage's
   `paths` config automatically. Hit **Use all paths** to add everything at once.
6. Click **Preview** — the right pane shows what the message looks like
   after the pipeline runs. No broker traffic, no DLQ rows.
7. Hit **Save & Deploy**. The Manager hot-reloads; new messages on the
   source queue come out the destination queue with the selected fields
   stripped.

The integration test under
[`internal/pipeline/integration_rabbit_test.go`](../../internal/pipeline/integration_rabbit_test.go)
publishes a similar JSON message through a live RabbitMQ broker and
asserts the filtered output — that's the round-trip equivalent of step 7
above, run automatically when you add `-tags integration`.
