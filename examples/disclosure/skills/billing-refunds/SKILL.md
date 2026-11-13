---
name: billing-refunds
description: Issue a refund the way finance expects, using the billing tools
---

# Billing refunds

1. Look the invoice up with `billing_invoice_get` and confirm it is paid.
2. Refund with `billing_refund_create`. Pass the invoice ID and the amount in cents.
3. Partial refunds need a reason. Full refunds don't.
4. Never refund an invoice older than 180 days. Open a ticket with finance instead.
