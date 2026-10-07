# Repository Structure

The project uses a monorepo with domain-oriented microservices.

## Principles

1. A service exists because it owns a business domain, not because a feature needs an endpoint.
2. Every service owns its persistence boundary.
3. Services communicate through versioned HTTP/gRPC contracts and durable events.
4. CF Meet owns human-to-human chat, voice and video communication.
5. The payment service owns real-estate transaction payment state, platform ledger, Paystack orchestration, refunds, settlement and reconciliation.
6. Shared packages cannot become hidden business-domain owners.
7. Staging and production infrastructure are isolated.
