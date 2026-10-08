# ADR-0004: Shared Payment Authority

**Status:** Accepted

All payment execution and financial state is centralized in payment-service.

User-domain services define the business reason and amount/context for a payment. payment-service owns payment intents, Paystack integration, ledger/journals, refunds, payouts, settlement, webhooks, idempotency, reconciliation and financial audit.

No other Real Estate service may call Paystack directly.
