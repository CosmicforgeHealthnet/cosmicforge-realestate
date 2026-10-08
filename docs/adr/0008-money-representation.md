# ADR-0008: Money Representation

**Status:** Accepted

Money is represented as integer minor units plus ISO 4217 currency code.

Floating-point monetary values are forbidden in persistent financial/domain contracts.

payment-service is the authority for ledger and payment execution state.
