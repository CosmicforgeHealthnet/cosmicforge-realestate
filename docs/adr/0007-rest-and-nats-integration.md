# ADR-0007: REST for Immediate Calls, NATS for Durable Async Work

**Status:** Accepted

Synchronous cross-service operations use authenticated REST/JSON internal APIs.

Durable asynchronous propagation uses NATS JetStream.

Services publishing business events after database changes use a transactional outbox.

All event consumers are idempotent because delivery is at-least-once.
